package server

import (
	"time"

	"github.com/google/uuid"

	"github.com/PeterSR/pupptyeer/internal/protocol"
	"github.com/PeterSR/pupptyeer/pkg/ptysession"
)

const (
	// ptyChunk caps a single output frame on the wire so bulk scrollback
	// replay interleaves fairly across sessions and stays well under the
	// decoder's line limit.
	ptyChunk = 32 * 1024
	// ringSize bounds per-session scrollback retained for attach replay.
	ringSize = 256 * 1024
)

type winsize struct{ cols, rows int }

// session is one supervised PTY plus the daemon's multi-client state. The PTY
// lifecycle, ring buffer, emulator, and rendered/raw capture live in the
// program-agnostic core (ptysession.Session); this type layers attach/detach,
// live broadcast, and tmux-style size arbitration on top.
//
// attachments and rawSubs are guarded by the core's lock: they are mutated
// only inside core.Locked and read only inside the onOutput hook (which the
// core invokes under the same lock). That reuses the core's serialisation so
// a client attaching to an actively-producing session never sees live bytes
// interleaved before its scrollback replay.
type session struct {
	id  string
	srv *Server

	command string
	args    []string
	cwd     string
	created time.Time

	core *ptysession.Session

	attachments map[*conn]winsize
	rawSubs     map[*rawConn]struct{} // raw firehose subscribers (see raw.go)
}

func newSession(srv *Server, p protocol.Message) (*session, error) {
	id := p.RequestedID
	if id == "" {
		id = newID()
	}

	var env []string
	if len(p.Env) > 0 {
		env = make([]string, 0, len(p.Env))
		for k, v := range p.Env {
			env = append(env, k+"="+v)
		}
	}

	s := &session{
		id:          id,
		srv:         srv,
		command:     p.Command,
		args:        p.Args,
		cwd:         p.Cwd,
		created:     time.Now(),
		attachments: make(map[*conn]winsize),
		rawSubs:     make(map[*rawConn]struct{}),
	}

	core, err := ptysession.Start(ptysession.Config{
		Command:  p.Command,
		Args:     p.Args,
		Cwd:      p.Cwd,
		Env:      env,
		Cols:     p.Cols,
		Rows:     p.Rows,
		Raw:      p.Raw,
		RingSize: ringSize,
		OnOutput: s.onOutput,
		OnExit:   s.onExit,
	})
	if err != nil {
		return nil, err
	}
	s.core = core
	return s, nil
}

// onOutput fans a freshly-read PTY chunk out to attached connections and raw
// subscribers. Invoked by the core under its lock, so reads of attachments and
// rawSubs are race-free against attach/detach (which mutate them under the same
// lock via core.Locked).
func (s *session) onOutput(chunk []byte) {
	if len(s.attachments) > 0 {
		data := protocol.EncodeData(chunk)
		for c := range s.attachments {
			c.send(protocol.Message{Type: protocol.TypeOutput, Session: s.id, Data: data})
		}
	}
	for rc := range s.rawSubs {
		rc.enqueue(chunk) // raw firehose: unframed bytes, no base64/JSON
	}
}

// onExit emits exit/closed to attached conns and removes the session from the
// registry. Invoked once by the core after the child is reaped.
func (s *session) onExit(exitCode int) {
	var conns []*conn
	var raws []*rawConn
	s.core.Locked(func(lc ptysession.LockedSession) {
		conns = make([]*conn, 0, len(s.attachments))
		for c := range s.attachments {
			conns = append(conns, c)
		}
		s.attachments = make(map[*conn]winsize)
		raws = make([]*rawConn, 0, len(s.rawSubs))
		for rc := range s.rawSubs {
			raws = append(raws, rc)
		}
		s.rawSubs = make(map[*rawConn]struct{})
	})

	for _, c := range conns {
		if !s.core.Killed() {
			code := exitCode
			c.send(protocol.Message{Type: protocol.TypeExit, Session: s.id, ExitCode: &code})
		}
		c.send(protocol.Message{Type: protocol.TypeSessionClosed, Session: s.id})
		c.dropSession(s.id)
	}
	// Raw firehose has no framing to carry an exit code: EOF is the signal.
	for _, rc := range raws {
		rc.shutdown()
	}
	s.srv.removeSession(s)
}

func (s *session) lastActive() time.Time { return s.core.LastActivity() }

func (s *session) info() protocol.SessionInfo {
	cols, rows := s.core.Size()
	attached := 0
	s.core.Locked(func(lc ptysession.LockedSession) { attached = len(s.attachments) })
	return protocol.SessionInfo{
		ID:           s.id,
		Command:      s.command,
		Args:         s.args,
		Cwd:          s.cwd,
		Cols:         cols,
		Rows:         rows,
		Created:      s.created.UTC().Format(time.RFC3339),
		LastActivity: s.core.LastActivity().UTC().Format(time.RFC3339),
		Attached:     attached,
		Alive:        s.core.Alive(),
		Raw:          s.core.Raw(),
	}
}

// attach adds c, applies the (possibly shrunk) effective size, then replays
// the ring as output frames terminated by scrollback_end. All under the core
// lock so the replay is serialised against live output (see the session-type
// doc comment).
func (s *session) attach(c *conn, cols, rows int) {
	c.addSession(s.id)
	s.core.Locked(func(lc ptysession.LockedSession) {
		s.attachments[c] = winsize{cols, rows}
		s.recomputeSizeLocked(lc)
		snapshot := lc.Snapshot()
		for i := 0; i < len(snapshot); i += ptyChunk {
			end := i + ptyChunk
			if end > len(snapshot) {
				end = len(snapshot)
			}
			c.send(protocol.Message{Type: protocol.TypeOutput, Session: s.id, Data: protocol.EncodeData(snapshot[i:end])})
		}
		c.send(protocol.Message{Type: protocol.TypeScrollbackEnd, Session: s.id})
	})
}

func (s *session) detach(c *conn) {
	s.core.Locked(func(lc ptysession.LockedSession) {
		delete(s.attachments, c)
		s.recomputeSizeLocked(lc)
	})
	c.dropSession(s.id)
}

// attachRaw registers a raw firehose subscriber and replays the current
// scrollback to it as a single unframed chunk. Held under the core lock - the
// same serialisation guarantee attach() relies on. Raw subscribers do not vote
// on size and do not run the emulator.
func (s *session) attachRaw(rc *rawConn) {
	s.core.Locked(func(lc ptysession.LockedSession) {
		s.rawSubs[rc] = struct{}{}
		if snapshot := lc.Snapshot(); len(snapshot) > 0 {
			rc.enqueue(snapshot)
		}
	})
}

func (s *session) detachRaw(rc *rawConn) {
	s.core.Locked(func(lc ptysession.LockedSession) {
		delete(s.rawSubs, rc)
	})
}

func (s *session) resizeFrom(c *conn, cols, rows int) {
	s.core.Locked(func(lc ptysession.LockedSession) {
		if _, ok := s.attachments[c]; ok {
			s.attachments[c] = winsize{cols, rows}
		}
		s.recomputeSizeLocked(lc)
	})
}

// recomputeSizeLocked sets the effective size to the smallest cols/rows across
// attached clients (tmux-style). If nobody is attached, the last size is
// retained. Caller runs inside core.Locked.
func (s *session) recomputeSizeLocked(lc ptysession.LockedSession) {
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
	lc.Resize(cols, rows)
}

func (s *session) write(b []byte) error { return s.core.Write(b) }

// kill tears the PTY down and waits for the read loop to drain.
func (s *session) kill() { s.core.Kill() }

func newID() string { return uuid.NewString() }
