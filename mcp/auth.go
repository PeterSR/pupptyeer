package main

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/mark3labs/mcp-go/server"
)

// protectedResourcePath is fixed by RFC 9728; MCP clients probe exactly this
// path to discover where to get a token.
const protectedResourcePath = "/.well-known/oauth-protected-resource"

// mcpEndpointPath is where the Streamable HTTP transport is mounted.
const mcpEndpointPath = "/mcp"

type httpConfig struct {
	addr        string
	auth        string
	token       string
	issuer      string
	audience    string
	scope       string
	resourceURL string
	allowRemote bool
}

// serveHTTP runs the MCP server over Streamable HTTP, gated by the configured
// auth middleware. When auth=oauth it also publishes the RFC 9728 protected
// resource metadata so clients can discover the authorization server.
func serveHTTP(s *server.MCPServer, cfg httpConfig) error {
	if cfg.resourceURL == "" {
		cfg.resourceURL = "http://" + cfg.addr
	}

	mw, err := newAuthMiddleware(cfg)
	if err != nil {
		return err
	}

	streamable := server.NewStreamableHTTPServer(s, server.WithEndpointPath(mcpEndpointPath))

	mux := http.NewServeMux()
	if cfg.auth == "oauth" {
		mux.HandleFunc(protectedResourcePath, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"resource":                 cfg.resourceURL,
				"authorization_servers":    []string{cfg.issuer},
				"bearer_methods_supported": []string{"header"},
			})
		})
	}
	mux.Handle(mcpEndpointPath, mw(streamable))

	fmt.Fprintf(os.Stderr, "pupptyeer-mcp: serving http on %s%s (auth=%s)\n", cfg.addr, mcpEndpointPath, cfg.auth)
	return (&http.Server{Addr: cfg.addr, Handler: mux}).ListenAndServe()
}

type middleware func(http.Handler) http.Handler

// newAuthMiddleware builds the request gate for the chosen auth mode and
// fails fast on misconfiguration (so a typo never silently runs open).
func newAuthMiddleware(cfg httpConfig) (middleware, error) {
	switch cfg.auth {
	case "none":
		if !cfg.allowRemote && !isLoopbackAddr(cfg.addr) {
			return nil, fmt.Errorf("auth=none requires a loopback -addr (got %q); pass -insecure-allow-remote to override", cfg.addr)
		}
		return func(next http.Handler) http.Handler { return next }, nil

	case "token":
		if cfg.token == "" {
			return nil, errors.New("auth=token requires -token or PUPPTYEER_MCP_TOKEN")
		}
		want := []byte(cfg.token)
		return func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				got := bearerToken(r)
				if got == "" || subtle.ConstantTimeCompare([]byte(got), want) != 1 {
					unauthorized(w, "", "invalid or missing bearer token")
					return
				}
				next.ServeHTTP(w, r)
			})
		}, nil

	case "oauth":
		if cfg.issuer == "" || cfg.audience == "" {
			return nil, errors.New("auth=oauth requires -oauth-issuer and -oauth-audience")
		}
		v := &oauthVerifier{issuer: cfg.issuer, audience: cfg.audience, scope: cfg.scope}
		metaURL := strings.TrimRight(cfg.resourceURL, "/") + protectedResourcePath
		return func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				raw := bearerToken(r)
				if raw == "" {
					unauthorized(w, metaURL, "missing bearer token")
					return
				}
				switch err := v.verify(r.Context(), raw); {
				case err == nil:
					next.ServeHTTP(w, r)
				case errors.Is(err, errInsufficientScope):
					forbidden(w, metaURL)
				default:
					unauthorized(w, metaURL, "invalid token")
				}
			})
		}, nil

	default:
		return nil, fmt.Errorf("unknown auth mode %q (want none|token|oauth)", cfg.auth)
	}
}

// bearerToken extracts the token from an "Authorization: Bearer <token>"
// header, or "" if absent/malformed.
func bearerToken(r *http.Request) string {
	const prefix = "Bearer "
	h := r.Header.Get("Authorization")
	if len(h) > len(prefix) && strings.EqualFold(h[:len(prefix)], prefix) {
		return strings.TrimSpace(h[len(prefix):])
	}
	return ""
}

// unauthorized writes a 401 with a WWW-Authenticate header. When metaURL is
// set (oauth mode) it points clients at the RFC 9728 metadata per RFC 9728 §5.1.
func unauthorized(w http.ResponseWriter, metaURL, desc string) {
	challenge := "Bearer"
	if metaURL != "" {
		challenge = fmt.Sprintf("Bearer resource_metadata=%q", metaURL)
	}
	w.Header().Set("WWW-Authenticate", challenge)
	http.Error(w, desc, http.StatusUnauthorized)
}

// forbidden writes a 403 for a valid token that lacks the required scope.
func forbidden(w http.ResponseWriter, metaURL string) {
	w.Header().Set("WWW-Authenticate", fmt.Sprintf("Bearer error=%q, resource_metadata=%q", "insufficient_scope", metaURL))
	http.Error(w, "insufficient_scope", http.StatusForbidden)
}

func isLoopbackAddr(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	if host == "" || host == "localhost" {
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	return false
}

var errInsufficientScope = errors.New("insufficient scope")

// oauthVerifier validates bearer JWTs as an OAuth 2.1 resource server. The
// OIDC provider (and its JWKS) is discovered lazily on first use so the server
// can start before the authorization server is reachable.
type oauthVerifier struct {
	issuer   string
	audience string
	scope    string

	mu       sync.Mutex
	verifier *oidc.IDTokenVerifier
}

func (o *oauthVerifier) ensure() (*oidc.IDTokenVerifier, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.verifier != nil {
		return o.verifier, nil
	}
	provider, err := oidc.NewProvider(context.Background(), o.issuer)
	if err != nil {
		return nil, err
	}
	o.verifier = provider.Verifier(&oidc.Config{ClientID: o.audience})
	return o.verifier, nil
}

// verify checks signature, issuer, audience and expiry (via go-oidc), then the
// required scope if one is configured.
func (o *oauthVerifier) verify(ctx context.Context, raw string) error {
	v, err := o.ensure()
	if err != nil {
		return err
	}
	tok, err := v.Verify(ctx, raw)
	if err != nil {
		return err
	}
	if o.scope == "" {
		return nil
	}
	var claims struct {
		Scope string   `json:"scope"`
		Scp   []string `json:"scp"`
	}
	if err := tok.Claims(&claims); err != nil {
		return err
	}
	for _, s := range append(strings.Fields(claims.Scope), claims.Scp...) {
		if s == o.scope {
			return nil
		}
	}
	return errInsufficientScope
}
