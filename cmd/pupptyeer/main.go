// Command pupptyeer is a local daemon that owns persistent PTY sessions,
// plus a CLI to drive them. The MCP front-end ships as a separate binary,
// pupptyeer-mcp (see the mcp/ module).
//
// Subcommands:
//
//	pupptyeer daemon           run the daemon (unix socket)
//	pupptyeer daemon install   install + start it as a per-user managed service
//	pupptyeer ctl ...          drive the daemon from the CLI (list/new/send/...)
//	pupptyeer version          print the build version
package main

import (
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
)

var version = "dev"

const usage = `pupptyeer: local PTY session manager

Usage:
  pupptyeer daemon                 run the daemon in the foreground
  pupptyeer daemon install         install + start as a per-user service (auto-start at login)
  pupptyeer daemon uninstall       stop + remove the service
  pupptyeer daemon start|stop|restart|status   manage the installed service
  pupptyeer ctl <cmd> [args...]    drive the daemon (list|new|send|capture|attach|resize|kill)
  pupptyeer version

The MCP server is a separate binary: pupptyeer-mcp (stdio or http).

Socket path: $PUPPTYEER_SOCK, else $XDG_RUNTIME_DIR/pupptyeer/daemon.sock,
else $TMPDIR/pupptyeer-<uid>/daemon.sock

Config (optional): $PUPPTYEER_CONFIG, else <user-config-dir>/pupptyeer/config.toml.
Customizes the detach key (detach_key) and new-session defaults; ignored if absent.
`

func main() {
	args := os.Args[1:]
	if len(args) == 0 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	switch args[0] {
	case "daemon":
		if err := runDaemon(args[1:]); err != nil {
			fmt.Fprintf(os.Stderr, "pupptyeer daemon: %v\n", err)
			os.Exit(1)
		}
	case "ctl":
		if err := runCtl(args[1:]); err != nil {
			fmt.Fprintf(os.Stderr, "pupptyeer ctl: %v\n", err)
			os.Exit(1)
		}
	case "version", "--version", "-v":
		fmt.Println(version)
	case "help", "--help", "-h":
		fmt.Print(usage)
	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand %q\n\n%s", args[0], usage)
		os.Exit(2)
	}
}

// socketPath resolves the daemon socket location.
func socketPath() string {
	if p := os.Getenv("PUPPTYEER_SOCK"); p != "" {
		return p
	}
	if d := os.Getenv("XDG_RUNTIME_DIR"); d != "" {
		return filepath.Join(d, "pupptyeer", "daemon.sock")
	}
	return filepath.Join(os.TempDir(), "pupptyeer-"+userToken(), "daemon.sock")
}

// userToken is a per-user, filesystem-safe identifier used to namespace
// the default socket directory under a shared temp dir. On Unix it is the
// numeric uid; on Windows it is the user SID (os.Getuid returns -1 there,
// which would otherwise collide across users on a shared temp dir). Falls
// back to "shared" only when the OS cannot identify the user.
func userToken() string {
	if u, err := user.Current(); err == nil && u.Uid != "" {
		return sanitizeToken(u.Uid)
	}
	if id := os.Getuid(); id >= 0 {
		return strconv.Itoa(id)
	}
	return "shared"
}

// sanitizeToken keeps the token safe as a single path element across
// filesystems: SIDs ("S-1-5-21-...") and uids pass through unchanged;
// anything outside [A-Za-z0-9._-] becomes '_'.
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
