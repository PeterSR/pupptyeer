package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/kardianos/service"

	"github.com/PeterSR/pupptyeer/internal/server"
)

// svcConfig describes pupptyeer as a per-user managed service: systemd
// --user on Linux, a launchd LaunchAgent on macOS, a Windows service. It
// runs "pupptyeer daemon" (no verb) and starts at login. Any PUPPTYEER_SOCK
// or PUPPTYEER_CONFIG set at install time is baked into the service
// environment so the service and the CLI resolve the same socket.
func svcConfig() *service.Config {
	return &service.Config{
		Name:        "pupptyeer",
		DisplayName: "pupptyeer PTY session daemon",
		Description: "Local daemon that owns persistent PTY sessions (pupptyeer).",
		Arguments:   []string{"daemon"},
		Option: service.KeyValue{
			"UserService": true, // per-user: systemd --user / launchd LaunchAgent
			"RunAtLoad":   true, // launchd: start at login
			"KeepAlive":   true, // launchd: restart if it exits
		},
		EnvVars: svcEnv(),
	}
}

// svcEnv carries the socket/config overrides from the installing shell into
// the service definition, so the service does not silently bind a different
// socket than the CLI talks to. Returns nil when neither is set.
func svcEnv() map[string]string {
	env := map[string]string{}
	for _, k := range []string{"PUPPTYEER_SOCK", "PUPPTYEER_CONFIG"} {
		if v := os.Getenv(k); v != "" {
			env[k] = v
		}
	}
	if len(env) == 0 {
		return nil
	}
	return env
}

// daemonProgram implements kardianos service.Interface. Start must not block
// (the service manager expects it to return), so the daemon serves on a
// goroutine and Stop closes it.
type daemonProgram struct {
	srv  *server.Server
	sock string
}

func (p *daemonProgram) Start(s service.Service) error {
	srv, sock, err := newDaemon()
	if err != nil {
		return err
	}
	p.srv, p.sock = srv, sock
	if service.Interactive() {
		fmt.Fprintf(os.Stderr, "pupptyeer daemon listening on %s (version %s)\n", sock, version)
	}
	go func() {
		if err := srv.Serve(); err != nil {
			fmt.Fprintf(os.Stderr, "pupptyeer daemon: serve: %v\n", err)
		}
		_ = os.Remove(sock)
		_ = os.Remove(server.RawSocketPath(sock))
	}()
	return nil
}

func (p *daemonProgram) Stop(s service.Service) error {
	if p.srv != nil {
		_ = p.srv.Close()
	}
	return nil
}

// newDaemon prepares the per-user socket directory and returns a listening
// server. It does not serve - the caller drives Serve. Shared by the
// foreground run and the service run.
func newDaemon() (*server.Server, string, error) {
	sock := socketPath()
	dir := filepath.Dir(sock)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, "", fmt.Errorf("create socket dir: %w", err)
	}
	// Lock the socket directory to the current user. On Unix the 0700 mode
	// above already does this; on Windows mode bits are ignored, so this
	// sets a restrictive ACL to uphold the local-only guarantee.
	if err := secureSocketDir(dir); err != nil {
		return nil, "", fmt.Errorf("secure socket dir: %w", err)
	}
	// Remove a stale socket from a previous unclean exit.
	if err := os.Remove(sock); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, "", fmt.Errorf("remove stale socket: %w", err)
	}

	srv, err := server.New(sock)
	if err != nil {
		return nil, "", fmt.Errorf("listen on %s: %w", sock, err)
	}
	if err := os.Chmod(sock, 0o600); err != nil {
		_ = srv.Close()
		return nil, "", fmt.Errorf("chmod socket: %w", err)
	}

	// Optional raw firehose: a second socket alongside the main one, mode 0600,
	// for the out-of-band high-throughput fast path (see internal/server/raw.go).
	// Additive - the main NDJSON socket above is unaffected.
	rawSock := server.RawSocketPath(sock)
	if err := os.Remove(rawSock); err != nil && !errors.Is(err, os.ErrNotExist) {
		_ = srv.Close()
		return nil, "", fmt.Errorf("remove stale raw socket: %w", err)
	}
	if err := srv.ListenRaw(rawSock); err != nil {
		_ = srv.Close()
		return nil, "", fmt.Errorf("listen raw on %s: %w", rawSock, err)
	}
	if err := os.Chmod(rawSock, 0o600); err != nil {
		_ = srv.Close()
		return nil, "", fmt.Errorf("chmod raw socket: %w", err)
	}
	return srv, sock, nil
}

// runDaemon dispatches "pupptyeer daemon [verb]". With no verb it runs the
// daemon: in the foreground when invoked from a terminal, or under the OS
// service manager when launched as the installed service (kardianos s.Run()
// handles both, including SIGINT/SIGTERM). With a verb it manages the
// service: install | uninstall | start | stop | restart | status.
func runDaemon(args []string) error {
	prg := &daemonProgram{}
	s, err := service.New(prg, svcConfig())
	if err != nil {
		return err
	}
	if len(args) == 0 {
		return s.Run()
	}

	switch verb := args[0]; verb {
	case "install":
		if err := s.Install(); err != nil {
			return fmt.Errorf("install: %w", err)
		}
		tuneUserService(s)
		if err := s.Start(); err != nil {
			return fmt.Errorf("service installed but failed to start: %w", err)
		}
		fmt.Printf("pupptyeer service installed and started (%s); it will start automatically at login.\n", s.Platform())
		return nil
	case "uninstall", "remove":
		// Best-effort stop first so we do not leave a live daemon behind.
		_ = s.Stop()
		cleanupUserService(s)
		if err := s.Uninstall(); err != nil {
			return fmt.Errorf("uninstall: %w", err)
		}
		fmt.Println("pupptyeer service stopped and uninstalled.")
		return nil
	case "start", "stop", "restart":
		if err := service.Control(s, verb); err != nil {
			return fmt.Errorf("%s: %w", verb, err)
		}
		fmt.Printf("pupptyeer service %s.\n", map[string]string{
			"start": "started", "stop": "stopped", "restart": "restarted",
		}[verb])
		return nil
	case "status":
		st, err := s.Status()
		if err != nil {
			if errors.Is(err, service.ErrNotInstalled) {
				fmt.Println("pupptyeer service: not installed")
				return nil
			}
			return fmt.Errorf("status: %w", err)
		}
		fmt.Printf("pupptyeer service: %s\n", statusString(st))
		return nil
	default:
		return fmt.Errorf("unknown daemon subcommand %q (use install|uninstall|start|stop|restart|status, or no argument to run in the foreground)", verb)
	}
}

// systemdDropInDir is the user drop-in directory for the pupptyeer unit.
func systemdDropInDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "systemd", "user", "pupptyeer.service.d"), nil
}

// tuneUserService patches up the systemd --user unit kardianos generates so
// it behaves well as a local interactive daemon. Best-effort and only
// meaningful on linux-systemd; a no-op elsewhere. Two fixes:
//
//  1. Start at login. kardianos enables the unit via WantedBy=multi-user.target,
//     but a user manager's default.target does not pull in multi-user.target,
//     so the unit would be "enabled" yet never started on login. Wiring it to
//     default.target closes that gap.
//  2. Restart with exponential backoff. kardianos hardcodes RestartSec=120
//     (a flat 2-minute outage after every crash). The drop-in starts at 2s
//     and, on systemd >= 254, grows the delay geometrically up to 5 min via
//     RestartSteps/RestartMaxDelaySec - quick recovery from a one-off crash,
//     but no tight respawn loop if the daemon is wedged. Older systemd
//     silently ignores the two extra keys and keeps the flat 2s.
func tuneUserService(s service.Service) {
	if s.Platform() != "linux-systemd" {
		return
	}
	if err := exec.Command("systemctl", "--user", "add-wants", "default.target", "pupptyeer.service").Run(); err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not wire pupptyeer.service to default.target for login start: %v\n", err)
	}
	override := "[Service]\nRestartSec=2\n"
	if systemdVersion() >= 254 {
		// 2s base, doubling-ish over 8 steps, capped at 300s.
		override += "RestartSteps=8\nRestartMaxDelaySec=300\n"
	}
	if dir, err := systemdDropInDir(); err == nil {
		if err := os.MkdirAll(dir, 0o755); err == nil {
			_ = os.WriteFile(filepath.Join(dir, "override.conf"), []byte(override), 0o644)
		}
	}
	_ = exec.Command("systemctl", "--user", "daemon-reload").Run()
}

// systemdVersion returns the running systemd major version, or 0 if it cannot
// be determined. The first line of `systemctl --version` is "systemd NNN ...".
func systemdVersion() int {
	out, err := exec.Command("systemctl", "--version").Output()
	if err != nil {
		return 0
	}
	fields := strings.Fields(string(out))
	if len(fields) >= 2 && fields[0] == "systemd" {
		if n, err := strconv.Atoi(fields[1]); err == nil {
			return n
		}
	}
	return 0
}

// cleanupUserService removes what tuneUserService added. systemctl disable
// (run by Uninstall) only removes links named in the unit's [Install]
// section, so the default.target link and the drop-in must be cleared here.
func cleanupUserService(s service.Service) {
	if s.Platform() != "linux-systemd" {
		return
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return
	}
	_ = os.Remove(filepath.Join(home, ".config", "systemd", "user", "default.target.wants", "pupptyeer.service"))
	if dir, err := systemdDropInDir(); err == nil {
		_ = os.RemoveAll(dir)
	}
}

func statusString(st service.Status) string {
	switch st {
	case service.StatusRunning:
		return "running"
	case service.StatusStopped:
		return "installed, stopped"
	default:
		return "unknown"
	}
}
