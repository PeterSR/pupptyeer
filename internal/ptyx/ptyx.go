// Package ptyx is a thin cross-platform pseudo-terminal abstraction.
//
// On Unix it wraps creack/pty; on Windows it wraps charmbracelet/x/conpty
// (the Windows 10+ pseudoconsole). Both present the same surface - a
// Pty is an io.ReadWriteCloser plus Resize - so the daemon's session
// code is platform-agnostic.
//
// Start spawns cmd attached to a fresh PTY sized cols×rows and returns
// the master end. On both platforms cmd.Process is populated so the
// caller's cmd.Wait() / cmd.Process.Kill() keep working (on Windows the
// child is created by ConPty.Spawn, not cmd.Start, and the pid is wired
// back into cmd.Process).
package ptyx

import (
	"io"
	"os/exec"
)

// Pty is the master end of a pseudo-terminal.
type Pty interface {
	io.ReadWriteCloser
	// Resize sets the terminal window size. The program inside the PTY
	// reads this (SIGWINCH on Unix) and re-renders to the new size.
	Resize(cols, rows uint16) error
}

// Start spawns cmd in a new PTY of the given size and returns the master.
// The caller owns cmd.Process (Wait/Kill) and must Close the Pty.
func Start(cmd *exec.Cmd, cols, rows uint16) (Pty, error) {
	return start(cmd, cols, rows)
}
