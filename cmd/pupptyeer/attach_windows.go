//go:build windows

package main

import (
	"os"
	"time"

	"golang.org/x/term"
)

// notifyResize reports local terminal size changes. Windows has no
// SIGWINCH, so poll the console size instead.
func notifyResize() <-chan struct{} {
	ch := make(chan struct{}, 1)
	go func() {
		fd := int(os.Stdin.Fd())
		lastW, lastH, _ := term.GetSize(fd)
		for {
			time.Sleep(500 * time.Millisecond)
			w, h, err := term.GetSize(fd)
			if err != nil || (w == lastW && h == lastH) {
				continue
			}
			lastW, lastH = w, h
			select {
			case ch <- struct{}{}:
			default:
			}
		}
	}()
	return ch
}
