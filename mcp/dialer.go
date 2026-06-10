package main

import (
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	client "github.com/PeterSR/pupptyeer/clients/go"
)

// socketPath resolves the daemon socket location, mirroring the daemon's
// own resolution so pupptyeer-mcp finds the same socket. Keep in sync with
// cmd/pupptyeer/main.go socketPath (it is a separate module).
func socketPath() string {
	if p := os.Getenv("PUPPTYEER_SOCK"); p != "" {
		return p
	}
	if d := os.Getenv("XDG_RUNTIME_DIR"); d != "" {
		return filepath.Join(d, "pupptyeer", "daemon.sock")
	}
	return filepath.Join(os.TempDir(), "pupptyeer-"+userToken(), "daemon.sock")
}

// userToken mirrors cmd/pupptyeer/main.go: a per-user namespace for the
// default socket dir (numeric uid on Unix, user SID on Windows, since
// os.Getuid returns -1 there).
func userToken() string {
	if u, err := user.Current(); err == nil && u.Uid != "" {
		return sanitizeToken(u.Uid)
	}
	if id := os.Getuid(); id >= 0 {
		return strconv.Itoa(id)
	}
	return "shared"
}

func sanitizeToken(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '.', r == '_', r == '-':
			return r
		default:
			return '_'
		}
	}, s)
}

// daemonDialer lazily dials the daemon and reuses the connection across
// tool calls. A failed dial is NOT cached: each call retries until one
// succeeds, so an MCP process started before the daemon recovers once the
// daemon comes up.
type daemonDialer struct {
	sock string
	mu   sync.Mutex
	cl   *client.Client
}

func newDaemonDialer(sock string) *daemonDialer { return &daemonDialer{sock: sock} }

func (d *daemonDialer) get() (*client.Client, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.cl != nil {
		return d.cl, nil
	}
	cl, err := client.Dial(d.sock)
	if err != nil {
		return nil, err
	}
	d.cl = cl
	return d.cl, nil
}

func (d *daemonDialer) close() {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.cl != nil {
		d.cl.Close()
	}
}
