//go:build windows

package ptyx

import (
	"fmt"
	"os"
	"os/exec"
	"sync"
	"syscall"

	"github.com/charmbracelet/x/conpty"
)

// windowsPty wraps a ConPty pseudoconsole.
type windowsPty struct {
	cp        *conpty.ConPty
	closeOnce sync.Once
	closeErr  error
}

func (p *windowsPty) Read(b []byte) (int, error)  { return p.cp.Read(b) }
func (p *windowsPty) Write(b []byte) (int, error) { return p.cp.Write(b) }
func (p *windowsPty) Resize(cols, rows uint16) error {
	return p.cp.Resize(int(cols), int(rows))
}
func (p *windowsPty) Close() error {
	p.closeOnce.Do(func() { p.closeErr = p.cp.Close() })
	return p.closeErr
}

func start(cmd *exec.Cmd, cols, rows uint16) (Pty, error) {
	if cmd.Path == "" {
		return nil, fmt.Errorf("ptyx: cmd.Path is empty")
	}
	cp, err := conpty.New(int(cols), int(rows), 0)
	if err != nil {
		return nil, fmt.Errorf("ptyx: conpty.New: %w", err)
	}

	attr := &syscall.ProcAttr{Dir: cmd.Dir, Env: cmd.Env}

	// ConPty.Spawn calls CreateProcess via the pseudoconsole attribute
	// list (not cmd.Start). Wire the returned pid into cmd.Process so the
	// caller's cmd.Wait() / cmd.Process.Kill() keep working unchanged.
	pid, _, err := cp.Spawn(cmd.Path, cmd.Args, attr)
	if err != nil {
		_ = cp.Close()
		return nil, fmt.Errorf("ptyx: conpty.Spawn %q: %w", cmd.Path, err)
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		_ = cp.Close()
		return nil, fmt.Errorf("ptyx: os.FindProcess(%d): %w", pid, err)
	}
	cmd.Process = proc

	return &windowsPty{cp: cp}, nil
}
