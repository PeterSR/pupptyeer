//go:build !windows

package main

// secureSocketDir is a no-op on Unix: runDaemon creates the directory with
// mode 0700 and chmods the socket to 0600, which already restrict access to
// the owning user.
func secureSocketDir(dir string) error { return nil }
