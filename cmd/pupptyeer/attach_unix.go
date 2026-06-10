//go:build !windows

package main

import (
	"os"
	"os/signal"
	"syscall"
)

// notifyResize reports local terminal size changes via SIGWINCH.
func notifyResize() <-chan struct{} {
	ch := make(chan struct{}, 1)
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGWINCH)
	go func() {
		for range sig {
			select {
			case ch <- struct{}{}:
			default:
			}
		}
	}()
	return ch
}
