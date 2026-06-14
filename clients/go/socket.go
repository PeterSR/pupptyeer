package client

import (
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
)

// DefaultSocketPath resolves the daemon socket location the same way the
// pupptyeer CLI does: $PUPPTYEER_SOCK, else $XDG_RUNTIME_DIR/pupptyeer/daemon.sock,
// else $TMPDIR/pupptyeer-<user>/daemon.sock (where <user> is the numeric uid on
// Unix or the user SID on Windows, so the dir is per-user everywhere). Callers
// that have no explicit socket path should Dial(DefaultSocketPath()).
func DefaultSocketPath() string {
	if p := os.Getenv("PUPPTYEER_SOCK"); p != "" {
		return p
	}
	if d := os.Getenv("XDG_RUNTIME_DIR"); d != "" {
		return filepath.Join(d, "pupptyeer", "daemon.sock")
	}
	return filepath.Join(os.TempDir(), "pupptyeer-"+userToken(), "daemon.sock")
}

// userToken is a per-user, filesystem-safe identifier used to namespace the
// default socket directory under a shared temp dir. Mirrors the CLI's resolver.
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
