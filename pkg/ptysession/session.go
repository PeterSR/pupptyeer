// Package ptysession is the program-agnostic core of pupptyeer: one
// supervised pseudo-terminal with a ring buffer of scrollback, an optional
// live terminal emulator for rendered-screen capture, settle-aware capture,
// resize, and clean teardown with an exit code.
//
// It is deliberately free of any daemon, socket, or wire-protocol concern.
// The pupptyeer daemon (internal/server) embeds a Session and layers its
// multi-client attach/broadcast/size-arbitration on top via the OnOutput
// hook and Locked; in-process callers (e.g. a `claude -p` driver) use the
// same core directly with no external binary.
package ptysession

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"sync"
	"sync/atomic"
	"time"

	"github.com/charmbracelet/x/vt"

	"github.com/PeterSR/pupptyeer/internal/ptyx"
)

const (
	// ptyChunk caps a single PTY read so bulk output stays well under the
	// daemon decoder's line limit and interleaves fairly across sessions.
	ptyChunk = 32 * 1024
	// defaultRingSize bounds per-session scrollback retained for replay.
	defaultRingSize = 256 * 1024
	// defaultSettleTimeout caps a settle/capture wait when the caller passes
	// no timeout.
	defaultSettleTimeout = 5 * time.Second
)

// Config configures a Session. Zero values mean "use the default".
type Config struct {
	Command  string   // required
	Args     []string // argv after Command
	Cwd      string   // working dir; "" = inherit
	Env      []string // full child env (like exec.Cmd.Env); nil = inherit os env
	Cols     int      // initial columns; <=0 => 80
	Rows     int      // initial rows; <=0 => 24
	Raw      bool     // skip the terminal emulator (no rendered capture; lower cost)
	RingSize int      // scrollback bytes retained; <=0 => 256KiB

	// OnOutput, if set, is invoked for every chunk read from the PTY, UNDER
	// the session lock, AFTER the ring buffer and emulator are updated. The
	// daemon uses it to fan output out to attached connections. The chunk is
	// owned by the Session for the duration of the call only - copy it if you
	// retain it past the callback. Leave nil for in-process use.
	OnOutput func(chunk []byte)

	// OnExit, if set, is invoked exactly once after the child is reaped, with
	// its exit code, NOT under the lock. The daemon uses it to emit exit/closed
	// frames and deregister the session.
	OnExit func(exitCode int)
}

// Cursor is the cursor position in a rendered Screen. Row/Col are 0-based.
type Cursor struct {
	Row, Col int
	Visible  bool
}

// Screen is the rendered visible grid: one space-padded string per row, the
// cursor position, and whether the program is on the alternate screen buffer.
type Screen struct {
	Cols, Rows int
	Lines      []string
	Cursor     Cursor
	AltScreen  bool
}

// Session is one supervised PTY. Methods are safe for concurrent use.
type Session struct {
	cmd      *exec.Cmd
	pty      ptyx.Pty
	raw      bool
	onOutput func([]byte)
	onExit   func(int)

	alive  atomic.Bool
	killed atomic.Bool

	// lastActivity is the UnixNano of the most recent PTY input or output.
	lastActivity atomic.Int64

	// mu guards ring, the emulator, cursorVisible, and effCols/effRows.
	mu            sync.Mutex
	ring          *ringBuffer
	term          *vt.Emulator
	cursorVisible bool
	effCols       int
	effRows       int

	wg         sync.WaitGroup
	finishOnce sync.Once
	exited     chan struct{}
	exitCode   atomic.Int32
}

// Start spawns cfg.Command in a fresh PTY and begins reading its output.
// The caller must Close (or Kill) the Session when done.
func Start(cfg Config) (*Session, error) {
	if cfg.Command == "" {
		return nil, errors.New("ptysession: Command is required")
	}
	cols, rows := cfg.Cols, cfg.Rows
	if cols <= 0 {
		cols = 80
	}
	if rows <= 0 {
		rows = 24
	}
	ringSize := cfg.RingSize
	if ringSize <= 0 {
		ringSize = defaultRingSize
	}

	cmd := exec.Command(cfg.Command, cfg.Args...)
	if cfg.Cwd != "" {
		cmd.Dir = cfg.Cwd
	}
	if cfg.Env != nil {
		cmd.Env = cfg.Env
	}

	pt, err := ptyx.Start(cmd, uint16(cols), uint16(rows))
	if err != nil {
		return nil, fmt.Errorf("pty start: %w", err)
	}

	s := &Session{
		cmd:           cmd,
		pty:           pt,
		raw:           cfg.Raw,
		onOutput:      cfg.OnOutput,
		onExit:        cfg.OnExit,
		ring:          newRingBuffer(ringSize),
		effCols:       cols,
		effRows:       rows,
		cursorVisible: true,
		exited:        make(chan struct{}),
	}
	s.wg.Add(1) // readLoop
	if !s.raw {
		// A live emulator answers "what is on the screen" (rendered capture).
		// It surfaces cursor visibility (DECTCEM) only through a callback;
		// mirror it onto the session. The callback fires during term.Write,
		// which readLoop always calls under s.mu, so cursorVisible is mu-guarded.
		s.term = vt.NewEmulator(cols, rows)
		s.term.SetCallbacks(vt.Callbacks{
			CursorVisibility: func(visible bool) { s.cursorVisible = visible },
		})
		s.wg.Add(1)
		go s.drainTerm()
	}
	s.alive.Store(true)
	s.lastActivity.Store(time.Now().UnixNano())
	go s.readLoop()
	return s, nil
}

// drainTerm consumes the emulator's response stream. charmbracelet/x/vt
// answers terminal queries the child emits (DSR, DA, cursor-position reports,
// in-band resize) by writing the reply into an UNBUFFERED internal pipe; if
// nothing reads it, term.Write blocks the moment a reply is generated - and
// readLoop calls term.Write while holding s.mu, so that wedges the whole
// session and every capture. We discard the replies (this core is a passive
// observer of the child's output, not the terminal answering it back). The
// copy returns when the emulator is closed in finish/Kill.
func (s *Session) drainTerm() {
	defer s.wg.Done()
	_, _ = io.Copy(io.Discard, s.term)
}

func (s *Session) touch() { s.lastActivity.Store(time.Now().UnixNano()) }

// LastActivity returns the time of the most recent PTY input or output.
func (s *Session) LastActivity() time.Time { return time.Unix(0, s.lastActivity.Load()) }

// Alive reports whether the child is still running.
func (s *Session) Alive() bool { return s.alive.Load() }

// Raw reports whether the session was created without a terminal emulator.
func (s *Session) Raw() bool { return s.raw }

// Size returns the current effective terminal size.
func (s *Session) Size() (cols, rows int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.effCols, s.effRows
}

func (s *Session) readLoop() {
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
			if s.term != nil {
				_, _ = s.term.Write(chunk) // update the live grid in lockstep with the ring
			}
			if s.onOutput != nil {
				s.onOutput(chunk)
			}
			s.mu.Unlock()
		}
		if err != nil {
			break
		}
	}
	s.finish()
}

// finish reaps the child, records the exit code, closes the emulator, and
// signals waiters. Runs exactly once.
func (s *Session) finish() {
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
		s.exitCode.Store(int32(exitCode))
		s.alive.Store(false)
		// readLoop has stopped, so no more term.Write; closing the emulator
		// unblocks drainTerm's io.Copy so it can exit. nil for raw sessions.
		if s.term != nil {
			_ = s.term.Close()
		}
		close(s.exited)
		if s.onExit != nil {
			s.onExit(exitCode)
		}
	})
}

// Write sends raw bytes to the child's PTY input.
func (s *Session) Write(b []byte) error {
	if !s.alive.Load() {
		return errors.New("session not alive")
	}
	s.touch()
	_, err := s.pty.Write(b)
	return err
}

// Resize sets the terminal size (emulator + PTY). Concurrency-safe.
func (s *Session) Resize(cols, rows int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.resizeLocked(cols, rows)
}

func (s *Session) resizeLocked(cols, rows int) error {
	if cols <= 0 || rows <= 0 {
		return nil
	}
	if cols == s.effCols && rows == s.effRows {
		return nil
	}
	s.effCols, s.effRows = cols, rows
	if s.term != nil {
		s.term.Resize(cols, rows)
	}
	return s.pty.Resize(uint16(cols), uint16(rows))
}

// LockedSession is the handle passed to Locked: it lets a multi-client layer
// read scrollback and resize while holding the session lock, so those actions
// are atomic with respect to live output delivered through OnOutput.
type LockedSession struct{ s *Session }

// Snapshot returns the current scrollback. Valid only inside the Locked call.
func (lc LockedSession) Snapshot() []byte { return lc.s.ring.snapshot() }

// Resize applies a new size; the lock is already held.
func (lc LockedSession) Resize(cols, rows int) { _ = lc.s.resizeLocked(cols, rows) }

// Locked runs fn while holding the session lock - the same lock OnOutput is
// invoked under. Use it to mutate a multi-client subscriber set and replay the
// ring snapshot atomically with respect to live output (no interleaving, no
// gap). Do not block inside fn.
func (s *Session) Locked(fn func(lc LockedSession)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	fn(LockedSession{s})
}

// lockTimeout acquires s.mu within d, returning false if it cannot. With the
// emulator drained (drainTerm) the lock is never held long, so this succeeds
// immediately; it exists so a future readLoop stall can never hang a capture
// caller forever.
func (s *Session) lockTimeout(d time.Duration) bool {
	if s.mu.TryLock() {
		return true
	}
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		time.Sleep(2 * time.Millisecond)
		if s.mu.TryLock() {
			return true
		}
	}
	return false
}

// CaptureRaw returns a snapshot of the raw scrollback bytes. With settle>0 it
// first waits for the PTY to be quiet for that long (capped by timeout).
func (s *Session) CaptureRaw(settle, timeout time.Duration) ([]byte, error) {
	s.waitSettle(settle, timeout)
	budget := defaultSettleTimeout
	if timeout > 0 {
		budget = timeout
	}
	if !s.lockTimeout(budget) {
		return nil, errors.New("capture timed out")
	}
	defer s.mu.Unlock()
	return s.ring.snapshot(), nil
}

// CaptureScreen returns the rendered visible grid. With settle>0 it first waits
// for the PTY to be quiet for that long (capped by timeout). Errors on a raw
// session (no emulator).
func (s *Session) CaptureScreen(settle, timeout time.Duration) (*Screen, error) {
	if s.raw {
		return nil, errors.New("rendered capture unavailable on a raw session")
	}
	s.waitSettle(settle, timeout)
	budget := defaultSettleTimeout
	if timeout > 0 {
		budget = timeout
	}
	if !s.lockTimeout(budget) {
		return nil, errors.New("capture timed out")
	}
	defer s.mu.Unlock()
	return s.renderLocked(), nil
}

// renderLocked renders the visible grid: one string per row (space-padded to
// the width, no escape sequences), the cursor, and the alt-screen flag. Caller
// holds mu.
func (s *Session) renderLocked() *Screen {
	w, h := s.term.Width(), s.term.Height()
	lines := make([]string, h)
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
			// A wide cell occupies cw columns; skip the trailing ones so the
			// column count stays aligned with the grid.
			x += cw - 1
		}
		for ; width < w; width++ {
			buf = append(buf, ' ')
		}
		lines[y] = string(buf)
	}
	p := s.term.CursorPosition()
	return &Screen{
		Cols:      w,
		Rows:      h,
		Lines:     lines,
		Cursor:    Cursor{Row: p.Y, Col: p.X, Visible: s.cursorVisible},
		AltScreen: s.term.IsAltScreen(),
	}
}

// waitSettle blocks until the PTY has produced no output for a continuous
// settle window, or until timeout total has elapsed (<=0 uses the default).
// settle <= 0 returns immediately. It polls lastActivity (atomic) and holds no
// lock, so it never stalls the PTY or other callers.
func (s *Session) waitSettle(settle, timeout time.Duration) {
	if settle <= 0 {
		return
	}
	if timeout <= 0 {
		timeout = defaultSettleTimeout
	}
	deadline := time.Now().Add(timeout)
	for {
		quiet := time.Since(s.LastActivity())
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

// Wait blocks until the child exits (or ctx is done) and returns the exit code.
func (s *Session) Wait(ctx context.Context) (int, error) {
	select {
	case <-s.exited:
		return int(s.exitCode.Load()), nil
	case <-ctx.Done():
		return 0, ctx.Err()
	}
}

// Exited reports without blocking whether the child has exited, plus its code.
func (s *Session) Exited() (bool, int) {
	select {
	case <-s.exited:
		return true, int(s.exitCode.Load())
	default:
		return false, 0
	}
}

// Killed reports whether Kill was called on this session.
func (s *Session) Killed() bool { return s.killed.Load() }

// Kill tears the PTY down and waits for the read loop to drain. Idempotent.
func (s *Session) Kill() {
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
	// Closing the emulator unblocks readLoop should it ever be parked in
	// term.Write, so wg.Wait below cannot deadlock. Idempotent with finish().
	if s.term != nil {
		_ = s.term.Close()
	}
	s.wg.Wait()
}

// Close kills the child (if still running) and releases resources. After Close
// the Session is unusable.
func (s *Session) Close() error {
	s.Kill()
	return nil
}
