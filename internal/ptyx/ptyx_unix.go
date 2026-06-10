//go:build !windows

package ptyx

import (
	"os"
	"os/exec"

	creack "github.com/creack/pty"
)

// unixPty wraps the master *os.File from creack/pty.
type unixPty struct{ f *os.File }

func (p *unixPty) Read(b []byte) (int, error)  { return p.f.Read(b) }
func (p *unixPty) Write(b []byte) (int, error) { return p.f.Write(b) }
func (p *unixPty) Close() error                { return p.f.Close() }
func (p *unixPty) Resize(cols, rows uint16) error {
	return creack.Setsize(p.f, &creack.Winsize{Cols: cols, Rows: rows})
}

func start(cmd *exec.Cmd, cols, rows uint16) (Pty, error) {
	f, err := creack.StartWithSize(cmd, &creack.Winsize{Cols: cols, Rows: rows})
	if err != nil {
		return nil, err
	}
	return &unixPty{f: f}, nil
}
