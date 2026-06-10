package server

import (
	"errors"
	"fmt"
	"os/exec"
	"sync"
	"sync/atomic"
	"time"

	"github.com/charmbracelet/x/vt"
	"github.com/google/uuid"

	"github.com/PeterSR/pupptyeer/internal/protocol"
	"github.com/PeterSR/pupptyeer/internal/ptyx"
)

const (
	// ptyChunk caps a single PTY read so bulk output interleaves fairly
	// across sessions and stays well under the decoder's line limit.
	ptyChunk = 32 * 1024
	// ringSize bounds per-session scrollback retained for attach replay.
	ringSize = 256 * 1024
	// defaultSettleTimeout caps a settle read when the caller passes no
	// timeout_ms (PROTOCOL.md: capture settle default timeout).
	defaultSettleTimeout = 5 * time.Second
)

type winsize struct{ cols, rows int }

// session is one supervised PTY. Its lifetime is owned by the Server's
// registry: connections attach/detach, but the PTY keeps running until
// the child exits or an explicit kill. Output flows from the PTY into a
// ring buffer (always) and to every attached connection (live).
type session struct {
	id  string
	srv *Server

	command string
	args    []string
	cwd     string
	created time.Time

	cmd *exec.Cmd
	pty ptyx.Pty

	alive  atomic.Bool
	killed atomic.Bool

	// lastActivity is the UnixNano of the most recent PTY input or
	// output. Bumped lock-free off the I/O hot path; read by gc to age
	// sessions. Attaching/detaching deliberately does NOT count as
	// activity - only bytes flowing through the PTY do.
	lastActivity atomic.Int64

	// mu guards ring, attachments, the effective size, and the emulator.
	mu          sync.Mutex
	ring        *ringBuffer
	attachments map[*conn]winsize
	effCols     int
	effRows     int

	// term is a live terminal emulator fed the same bytes as the ring, so
	// the daemon can answer "what is on the screen" (rendered capture)
	// authoritatively regardless of ring size. Guarded by mu. cursorVisible
	// tracks DECTCEM (the emulator exposes visibility only via callback).
	term          *vt.Emulator
	cursorVisible bool

	wg         sync.WaitGroup
	finishOnce sync.Once
}

func newSession(srv *Server, p protocol.Message) (*session, error) {
	if p.Command == "" {
		return nil, errors.New("new_session: command is required")
	}
	cols, rows := p.Cols, p.Rows
	if cols <= 0 {
		cols = 80
	}
	if rows <= 0 {
		rows = 24
	}

	cmd := exec.Command(p.Command, p.Args...)
	if p.Cwd != "" {
		cmd.Dir = p.Cwd
	}
	if len(p.Env) > 0 {
		env := make([]string, 0, len(p.Env))
		for k, v := range p.Env {
			env = append(env, k+"="+v)
		}
		cmd.Env = env
	}

	pt, err := ptyx.Start(cmd, uint16(cols), uint16(rows))
	if err != nil {
		return nil, fmt.Errorf("pty start: %w", err)
	}

	s := &session{
		id:            newID(),
		srv:           srv,
		command:       p.Command,
		args:          p.Args,
		cwd:           p.Cwd,
		created:       time.Now(),
		cmd:           cmd,
		pty:           pt,
		ring:          newRingBuffer(ringSize),
		attachments:   make(map[*conn]winsize),
		effCols:       cols,
		effRows:       rows,
		term:          vt.NewEmulator(cols, rows),
		cursorVisible: true,
	}
	// The emulator surfaces cursor visibility (DECTCEM) only through a
	// callback; mirror it onto the session. Fired during term.Write, which
	// the read loop always calls under s.mu, so the field is mu-guarded.
	s.term.SetCallbacks(vt.Callbacks{
		CursorVisibility: func(visible bool) { s.cursorVisible = visible },
	})
	s.alive.Store(true)
	s.lastActivity.Store(s.created.UnixNano())
	s.wg.Add(1)
	go s.readLoop()
	return s, nil
}

// touch records I/O activity now. Cheap and lock-free; called on every
// PTY read and write.
func (s *session) touch() { s.lastActivity.Store(time.Now().UnixNano()) }

// lastActive returns the time of the most recent PTY input or output.
func (s *session) lastActive() time.Time { return time.Unix(0, s.lastActivity.Load()) }

func (s *session) info() protocol.SessionInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	return protocol.SessionInfo{
		ID:           s.id,
		Command:      s.command,
		Args:         s.args,
		Cwd:          s.cwd,
		Cols:         s.effCols,
		Rows:         s.effRows,
		Created:      s.created.UTC().Format(time.RFC3339),
		LastActivity: s.lastActive().UTC().Format(time.RFC3339),
		Attached:     len(s.attachments),
		Alive:        s.alive.Load(),
	}
}

func (s *session) readLoop() {
	defer s.wg.Done()
	buf := make([]byte, ptyChunk)
	for {
		n, err := s.pty.Read(buf)
		if n > 0 {
			s.touch()
			chunk := make([]byte, n)
			copy(chunk, buf[:n])
			s.mu.Lock()
			s.ring.append(chunk)
			_, _ = s.term.Write(chunk) // update the live grid in lockstep with the ring
			data := protocol.EncodeData(chunk)
			for c := range s.attachments {
				c.send(protocol.Message{Type: protocol.TypeOutput, Session: s.id, Data: data})
			}
			s.mu.Unlock()
		}
		if err != nil {
			break
		}
	}
	s.finish()
}

// finish reaps the child, emits exit/closed to attached conns, and
// removes the session from the registry. Runs exactly once.
func (s *session) finish() {
	s.finishOnce.Do(func() {
		exitCode := 0
		if err := s.cmd.Wait(); err != nil {
			var ee *exec.ExitError
			if errors.As(err, &ee) {
				exitCode = ee.ExitCode()
			} else {
				exitCode = 1
			}
		}
		s.alive.Store(false)

		s.mu.Lock()
		conns := make([]*conn, 0, len(s.attachments))
		for c := range s.attachments {
			conns = append(conns, c)
		}
		s.attachments = make(map[*conn]winsize)
		s.mu.Unlock()

		for _, c := range conns {
			if !s.killed.Load() {
				code := exitCode
				c.send(protocol.Message{Type: protocol.TypeExit, Session: s.id, ExitCode: &code})
			}
			c.send(protocol.Message{Type: protocol.TypeSessionClosed, Session: s.id})
			c.dropSession(s.id)
		}
		s.srv.removeSession(s.id)
	})
}

// attach adds c, applies the (possibly shrunk) effective size, then
// replays the ring as output frames terminated by scrollback_end.
//
// The replay is sent while holding s.mu - the same lock readLoop holds
// to broadcast live output. That serialisation is load-bearing: it
// guarantees every live chunk is either already in the snapshot or
// queued strictly after scrollback_end, so a client attaching to an
// actively-producing session never sees live bytes interleaved before
// its scrollback. c.send is non-blocking, so holding the lock across
// the replay does not stall readLoop.
func (s *session) attach(c *conn, cols, rows int) {
	c.addSession(s.id)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.attachments[c] = winsize{cols, rows}
	s.recomputeSizeLocked()
	snapshot := s.ring.snapshot()
	for i := 0; i < len(snapshot); i += ptyChunk {
		end := i + ptyChunk
		if end > len(snapshot) {
			end = len(snapshot)
		}
		c.send(protocol.Message{Type: protocol.TypeOutput, Session: s.id, Data: protocol.EncodeData(snapshot[i:end])})
	}
	c.send(protocol.Message{Type: protocol.TypeScrollbackEnd, Session: s.id})
}

func (s *session) detach(c *conn) {
	s.mu.Lock()
	delete(s.attachments, c)
	s.recomputeSizeLocked()
	s.mu.Unlock()
	c.dropSession(s.id)
}

func (s *session) resizeFrom(c *conn, cols, rows int) {
	s.mu.Lock()
	if _, ok := s.attachments[c]; ok {
		s.attachments[c] = winsize{cols, rows}
	}
	s.recomputeSizeLocked()
	s.mu.Unlock()
}

// recomputeSizeLocked sets the effective size to the smallest cols/rows
// across attached clients (tmux-style). If nobody is attached, the last
// size is retained. Applies the change to the PTY. Caller holds mu.
func (s *session) recomputeSizeLocked() {
	cols, rows := 0, 0
	for _, ws := range s.attachments {
		if ws.cols > 0 && (cols == 0 || ws.cols < cols) {
			cols = ws.cols
		}
		if ws.rows > 0 && (rows == 0 || ws.rows < rows) {
			rows = ws.rows
		}
	}
	if cols == 0 || rows == 0 {
		return // no attached client with a size; keep current
	}
	if cols == s.effCols && rows == s.effRows {
		return
	}
	s.effCols, s.effRows = cols, rows
	s.term.Resize(cols, rows)
	_ = s.pty.Resize(uint16(cols), uint16(rows))
}

func (s *session) write(b []byte) error {
	if !s.alive.Load() {
		return errors.New("session not alive")
	}
	s.touch()
	_, err := s.pty.Write(b)
	return err
}

func (s *session) capture() []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ring.snapshot()
}

// renderScreen returns the visible terminal grid: one string per row
// (space-padded to the width, no escape sequences), the cursor position,
// and whether the program is on the alternate screen buffer. The grid is
// the daemon's authoritative screen state, not scrollback.
func (s *session) renderScreen() (cols, rows int, lines []string, cur protocol.Cursor, alt bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	w, h := s.term.Width(), s.term.Height()
	lines = make([]string, h)
	buf := make([]rune, 0, w)
	for y := 0; y < h; y++ {
		buf = buf[:0]
		width := 0
		for x := 0; x < w; x++ {
			c := s.term.CellAt(x, y)
			if c == nil || c.Content == "" {
				buf = append(buf, ' ')
				width++
				continue
			}
			buf = append(buf, []rune(c.Content)...)
			cw := c.Width
			if cw < 1 {
				cw = 1
			}
			width += cw
			// A wide cell occupies cw columns; skip the trailing ones so
			// the column count stays aligned with the grid.
			x += cw - 1
		}
		// Pad to the full width so every line is exactly cols wide.
		for ; width < w; width++ {
			buf = append(buf, ' ')
		}
		lines[y] = string(buf)
	}
	p := s.term.CursorPosition()
	cur = protocol.Cursor{Row: p.Y, Col: p.X, Visible: s.cursorVisible}
	return w, h, lines, cur, s.term.IsAltScreen()
}

// waitSettle blocks until the PTY has produced no output for a continuous
// settleMs window, or until timeoutMs total has elapsed (<=0 uses the
// default). settleMs <= 0 returns immediately. It polls lastActivity
// (atomic, bumped by readLoop) and holds no lock, so it never stalls the
// PTY or other connections.
func (s *session) waitSettle(settleMs, timeoutMs int) {
	if settleMs <= 0 {
		return
	}
	settle := time.Duration(settleMs) * time.Millisecond
	timeout := defaultSettleTimeout
	if timeoutMs > 0 {
		timeout = time.Duration(timeoutMs) * time.Millisecond
	}
	deadline := time.Now().Add(timeout)
	for {
		quiet := time.Since(s.lastActive())
		if quiet >= settle {
			return
		}
		now := time.Now()
		if !now.Before(deadline) {
			return
		}
		wait := settle - quiet
		if d := deadline.Sub(now); d < wait {
			wait = d
		}
		if wait > 25*time.Millisecond {
			wait = 25 * time.Millisecond
		}
		time.Sleep(wait)
	}
}

// kill tears the PTY down and waits for the read loop to drain.
func (s *session) kill() {
	if !s.alive.Load() && s.killed.Load() {
		return
	}
	s.killed.Store(true)
	if s.pty != nil {
		_ = s.pty.Close() // SIGHUP to the child via the controlling tty
	}
	if s.cmd.Process != nil {
		_ = s.cmd.Process.Kill()
	}
	s.wg.Wait()
}

func newID() string { return uuid.NewString() }

// ringBuffer keeps the most recent <=max bytes of PTY output.
type ringBuffer struct {
	buf []byte
	max int
}

func newRingBuffer(max int) *ringBuffer { return &ringBuffer{max: max} }

func (r *ringBuffer) append(p []byte) {
	r.buf = append(r.buf, p...)
	if len(r.buf) > r.max {
		r.buf = append([]byte(nil), r.buf[len(r.buf)-r.max:]...)
	}
}

func (r *ringBuffer) snapshot() []byte {
	out := make([]byte, len(r.buf))
	copy(out, r.buf)
	return out
}
