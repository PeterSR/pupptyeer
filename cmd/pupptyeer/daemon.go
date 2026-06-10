package main

import (
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/PeterSR/pupptyeer/internal/server"
)

func runDaemon() error {
	sock := socketPath()
	dir := filepath.Dir(sock)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create socket dir: %w", err)
	}
	// Lock the socket directory to the current user. On Unix the 0700 mode
	// above already does this; on Windows mode bits are ignored, so this
	// sets a restrictive ACL to uphold the local-only guarantee.
	if err := secureSocketDir(dir); err != nil {
		return fmt.Errorf("secure socket dir: %w", err)
	}
	// Remove a stale socket from a previous unclean exit.
	if err := os.Remove(sock); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove stale socket: %w", err)
	}

	srv, err := server.New(sock)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", sock, err)
	}
	if err := os.Chmod(sock, 0o600); err != nil {
		_ = srv.Close()
		return fmt.Errorf("chmod socket: %w", err)
	}
	fmt.Fprintf(os.Stderr, "pupptyeer daemon listening on %s (version %s)\n", sock, version)

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		<-stop
		fmt.Fprintln(os.Stderr, "pupptyeer daemon shutting down")
		_ = srv.Close()
	}()

	err = srv.Serve()
	_ = os.Remove(sock)
	return err
}
