package main

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestNewDaemonRefusesLiveSocket pins the guard that stops a second daemon
// from unlinking, and thereby orphaning, a daemon already listening on the
// socket. Regression for the foot-gun where running `pupptyeer daemon` by hand
// while the managed service is up silently stole its socket: the running
// daemon kept its sessions but became permanently unreachable.
func TestNewDaemonRefusesLiveSocket(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "daemon.sock")
	t.Setenv("PUPPTYEER_SOCK", sock)

	// Stand in for a live daemon: a real listener on the socket path.
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	if _, _, err := newDaemon(); err == nil {
		t.Fatal("newDaemon should refuse to start over a live socket, got nil error")
	} else if !strings.Contains(err.Error(), "already listening") {
		t.Errorf("error should name the conflict, got: %v", err)
	}

	// Refusing means NOT unlinking: the live socket must survive untouched.
	if _, err := os.Stat(sock); err != nil {
		t.Errorf("guard must leave the live socket in place, but stat failed: %v", err)
	}
}

// TestNewDaemonStartsWhenSocketFree confirms the guard is a no-op on the normal
// path: with nothing listening, newDaemon binds and returns a server.
func TestNewDaemonStartsWhenSocketFree(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "daemon.sock")
	t.Setenv("PUPPTYEER_SOCK", sock)

	srv, got, err := newDaemon()
	if err != nil {
		t.Fatalf("newDaemon on a free socket: %v", err)
	}
	defer srv.Close()
	if got != sock {
		t.Errorf("sock = %q, want %q", got, sock)
	}
	if _, err := os.Stat(sock); err != nil {
		t.Errorf("daemon should have bound the socket: %v", err)
	}
}
