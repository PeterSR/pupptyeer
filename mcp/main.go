// Command pupptyeer-mcp exposes a running pupptyeer daemon's verbs as an MCP
// server. It is a thin front-end that talks to the daemon over the unix
// socket via the Go client, so it carries no daemon internals.
//
// Transports:
//
//	pupptyeer-mcp                          stdio (default)
//	pupptyeer-mcp -transport http          Streamable HTTP on 127.0.0.1:8765/mcp
//
// HTTP auth (-auth): none (loopback only), token (static bearer), or oauth
// (OAuth 2.1 resource server; validates external IdP bearer JWTs).
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/mark3labs/mcp-go/server"
)

var version = "dev"

func main() {
	var (
		transport   = flag.String("transport", "stdio", "transport: stdio | http")
		addr        = flag.String("addr", "127.0.0.1:8765", "http listen address")
		auth        = flag.String("auth", "none", "http auth: none | token | oauth")
		token       = flag.String("token", os.Getenv("PUPPTYEER_MCP_TOKEN"), "static bearer token (auth=token); env PUPPTYEER_MCP_TOKEN")
		issuer      = flag.String("oauth-issuer", "", "OIDC issuer URL for discovery + JWKS (auth=oauth)")
		audience    = flag.String("oauth-audience", "", "expected token audience, i.e. this resource's URL (auth=oauth)")
		scope       = flag.String("oauth-scope", "", "required scope; empty means any valid token (auth=oauth)")
		resourceURL = flag.String("resource-url", "", "public URL of this MCP resource for RFC 9728 metadata (default derived from -addr)")
		allowRemote = flag.Bool("insecure-allow-remote", false, "allow a non-loopback -addr with auth=none")
	)
	flag.Parse()

	d := newDaemonDialer(socketPath())
	defer d.close()
	s := buildServer(version, d)

	switch *transport {
	case "stdio":
		if err := server.ServeStdio(s); err != nil {
			fail(err)
		}
	case "http":
		if err := serveHTTP(s, httpConfig{
			addr:        *addr,
			auth:        *auth,
			token:       *token,
			issuer:      *issuer,
			audience:    *audience,
			scope:       *scope,
			resourceURL: *resourceURL,
			allowRemote: *allowRemote,
		}); err != nil {
			fail(err)
		}
	default:
		fail(fmt.Errorf("unknown transport %q (want stdio|http)", *transport))
	}
}

func fail(err error) {
	fmt.Fprintf(os.Stderr, "pupptyeer-mcp: %v\n", err)
	os.Exit(1)
}
