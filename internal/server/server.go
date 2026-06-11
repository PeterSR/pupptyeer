// Package server runs the pupptyeer daemon: a unix-socket listener,
// a process-wide session registry, and per-connection NDJSON multiplexing.
// Sessions outlive any single connection - they end on child exit or an
// explicit kill, never on a client disconnect.
package server

import (
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"sync"
	"time"

	"github.com/PeterSR/pupptyeer/internal/protocol"
)

// outboundQueue bounds a connection's pending-write buffer. If a client
// can't keep up and the queue fills, the connection is dropped rather
// than blocking PTY readers or other clients (backpressure decision).
const outboundQueue = 256

// Server accepts connections and routes their multiplexed messages to a
// shared session registry.
type Server struct {
	ln net.Listener

	mu       sync.Mutex
	conns    map[*conn]struct{}
	sessions map[string]*session
}

// New starts a Server listening on socketPath. The caller owns the
// socket file's permissions and removal.
func New(socketPath string) (*Server, error) {
	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		return nil, err
	}
	return &Server{
		ln:       ln,
		conns:    make(map[*conn]struct{}),
		sessions: make(map[string]*session),
	}, nil
}

// Serve runs the accept loop until Close.
func (s *Server) Serve() error {
	for {
		nc, err := s.ln.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return nil
			}
			return fmt.Errorf("accept: %w", err)
		}
		c := newConn(s, nc)
		s.mu.Lock()
		s.conns[c] = struct{}{}
		s.mu.Unlock()
		go func() {
			c.run()
			s.mu.Lock()
			delete(s.conns, c)
			s.mu.Unlock()
		}()
	}
}

// Close stops accepting, drops all connections, and kills every session.
func (s *Server) Close() error {
	err := s.ln.Close()
	s.mu.Lock()
	conns := make([]*conn, 0, len(s.conns))
	for c := range s.conns {
		conns = append(conns, c)
	}
	sessions := make([]*session, 0, len(s.sessions))
	for _, sess := range s.sessions {
		sessions = append(sessions, sess)
	}
	s.mu.Unlock()
	for _, c := range conns {
		c.shutdown()
	}
	for _, sess := range sessions {
		sess.kill()
	}
	return err
}

func (s *Server) addSession(sess *session) {
	s.mu.Lock()
	s.sessions[sess.id] = sess
	s.mu.Unlock()
}

func (s *Server) removeSession(id string) {
	s.mu.Lock()
	delete(s.sessions, id)
	s.mu.Unlock()
}

func (s *Server) getSession(id string) *session {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sessions[id]
}

func (s *Server) listSessions() []protocol.SessionInfo {
	s.mu.Lock()
	sessions := make([]*session, 0, len(s.sessions))
	for _, sess := range s.sessions {
		sessions = append(sessions, sess)
	}
	s.mu.Unlock()
	out := make([]protocol.SessionInfo, 0, len(sessions))
	for _, sess := range sessions {
		out = append(out, sess.info())
	}
	return out
}

// gc kills every session idle (no PTY input or output) for at least
// maxIdleSeconds and returns metadata for the reaped ones, snapshotted
// just before each kill. maxIdleSeconds <= 0 reaps every session.
func (s *Server) gc(maxIdleSeconds int) []protocol.SessionInfo {
	if maxIdleSeconds < 0 {
		maxIdleSeconds = 0
	}
	cutoff := time.Now().Add(-time.Duration(maxIdleSeconds) * time.Second)

	s.mu.Lock()
	victims := make([]*session, 0)
	for _, sess := range s.sessions {
		// idle >= maxIdleSeconds ⇔ lastActive at or before the cutoff.
		if !sess.lastActive().After(cutoff) {
			victims = append(victims, sess)
		}
	}
	s.mu.Unlock()

	out := make([]protocol.SessionInfo, 0, len(victims))
	for _, sess := range victims {
		info := sess.info() // snapshot before kill removes it from the registry
		sess.kill()
		out = append(out, info)
	}
	return out
}

// conn owns one accepted connection: a reader goroutine, a writer
// goroutine draining a bounded outbound queue, and the set of sessions
// this connection is attached to (for cleanup on disconnect).
type conn struct {
	srv *Server
	nc  net.Conn

	out  chan []byte
	done chan struct{}
	once sync.Once

	mu       sync.Mutex
	attached map[string]bool
}

func newConn(srv *Server, nc net.Conn) *conn {
	return &conn{
		srv:      srv,
		nc:       nc,
		out:      make(chan []byte, outboundQueue),
		done:     make(chan struct{}),
		attached: make(map[string]bool),
	}
}

func (c *conn) addSession(id string) {
	c.mu.Lock()
	c.attached[id] = true
	c.mu.Unlock()
}

func (c *conn) dropSession(id string) {
	c.mu.Lock()
	delete(c.attached, id)
	c.mu.Unlock()
}

// send enqueues a message for the writer. Non-blocking: if the queue is
// full (slow client) the connection is shut down instead of blocking.
func (c *conn) send(m protocol.Message) {
	b, err := protocol.Marshal(m)
	if err != nil {
		return
	}
	select {
	case c.out <- b:
	case <-c.done:
	default:
		// Queue full: drop this slow client. Done asynchronously because
		// send() may be called while a session holds its lock, and
		// shutdown() detaches (which takes that same lock).
		go c.shutdown()
	}
}

func (c *conn) run() {
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); c.writeLoop() }()
	go func() { defer wg.Done(); c.readLoop() }()
	wg.Wait()
}

func (c *conn) writeLoop() {
	for {
		select {
		case b, ok := <-c.out:
			if !ok {
				return
			}
			if _, err := c.nc.Write(b); err != nil {
				c.shutdown()
				return
			}
		case <-c.done:
			return
		}
	}
}

func (c *conn) readLoop() {
	dec := protocol.NewDecoder(c.nc)
	for {
		m, err := dec.Decode()
		if err != nil {
			if !errors.Is(err, io.EOF) && !errors.Is(err, net.ErrClosed) {
				fmt.Fprintf(os.Stderr, "pupptyeer: read error: %v\n", err)
			}
			c.shutdown()
			return
		}
		c.dispatch(m)
	}
}

func (c *conn) dispatch(m protocol.Message) {
	switch m.Type {
	case protocol.TypeNewSession:
		c.handleNewSession(m)
	case protocol.TypeListSessions:
		c.send(protocol.Message{Type: protocol.TypeSessions, ID: m.ID, Sessions: c.srv.listSessions()})
	case protocol.TypeAttach:
		c.handleAttach(m)
	case protocol.TypeDetach:
		if s := c.srv.getSession(m.Session); s != nil {
			s.detach(c)
		}
	case protocol.TypeWritePane:
		c.handleWrite(m)
	case protocol.TypeCapturePane:
		c.handleCapture(m)
	case protocol.TypeResize:
		if s := c.srv.getSession(m.Session); s != nil {
			s.resizeFrom(c, m.Cols, m.Rows)
		}
	case protocol.TypeKill:
		c.handleKill(m)
	case protocol.TypeGC:
		c.send(protocol.Message{Type: protocol.TypeReaped, ID: m.ID, Sessions: c.srv.gc(m.MaxIdleSeconds)})
	default:
		c.sendError(m.ID, m.Session, "unknown type: "+m.Type)
	}
}

func (c *conn) handleNewSession(m protocol.Message) {
	s, err := newSession(c.srv, m)
	if err != nil {
		c.sendError(m.ID, "", err.Error())
		return
	}
	c.srv.addSession(s)
	c.send(protocol.Message{Type: protocol.TypeOK, ID: m.ID, Session: s.id})
}

func (c *conn) handleAttach(m protocol.Message) {
	s := c.srv.getSession(m.Session)
	if s == nil {
		c.sendError(m.ID, m.Session, "session not found")
		return
	}
	c.send(protocol.Message{Type: protocol.TypeAttached, ID: m.ID, Session: s.id})
	s.attach(c, m.Cols, m.Rows)
}

func (c *conn) handleWrite(m protocol.Message) {
	s := c.srv.getSession(m.Session)
	if s == nil {
		c.sendError(m.ID, m.Session, "session not found")
		return
	}
	var b []byte
	if m.Data != "" {
		dec, err := protocol.DecodeData(m.Data)
		if err != nil {
			c.sendError(m.ID, m.Session, "bad base64 data: "+err.Error())
			return
		}
		b = dec
	} else {
		b = []byte(m.Text)
	}
	if err := s.write(b); err != nil {
		c.sendError(m.ID, m.Session, "write failed: "+err.Error())
	}
}

func (c *conn) handleCapture(m protocol.Message) {
	s := c.srv.getSession(m.Session)
	if s == nil {
		c.sendError(m.ID, m.Session, "session not found")
		return
	}
	// Optionally wait for the screen to go quiet before snapshotting.
	s.waitSettle(m.SettleMs, m.TimeoutMs)
	// Bound the snapshot itself by timeout_ms (default 5s) so a wedged read
	// loop can never hang the client forever; with the emulator drained this
	// always completes immediately.
	budget := defaultSettleTimeout
	if m.TimeoutMs > 0 {
		budget = time.Duration(m.TimeoutMs) * time.Millisecond
	}
	if m.Render {
		cols, rows, lines, cur, alt, ok := s.renderWithin(budget)
		if !ok {
			c.sendError(m.ID, m.Session, "capture timed out")
			return
		}
		c.send(protocol.Message{
			Type: protocol.TypeCapture, ID: m.ID, Session: s.id,
			Cols: cols, Rows: rows, Lines: lines, Cursor: &cur, AltScreen: alt,
		})
		return
	}
	data, ok := s.captureWithin(budget)
	if !ok {
		c.sendError(m.ID, m.Session, "capture timed out")
		return
	}
	c.send(protocol.Message{Type: protocol.TypeCapture, ID: m.ID, Session: s.id, Data: protocol.EncodeData(data)})
}

func (c *conn) handleKill(m protocol.Message) {
	s := c.srv.getSession(m.Session)
	if s == nil {
		c.sendError(m.ID, m.Session, "session not found")
		return
	}
	s.kill()
	c.send(protocol.Message{Type: protocol.TypeOK, ID: m.ID, Session: m.Session})
}

func (c *conn) sendError(id int, session, msg string) {
	c.send(protocol.Message{Type: protocol.TypeError, ID: id, Session: session, Message: msg})
}

// shutdown closes the connection and detaches it from every session it
// was attached to. Sessions are NOT killed - they outlive the client.
func (c *conn) shutdown() {
	c.once.Do(func() {
		close(c.done)
		_ = c.nc.Close()
		c.mu.Lock()
		ids := make([]string, 0, len(c.attached))
		for id := range c.attached {
			ids = append(ids, id)
		}
		c.mu.Unlock()
		for _, id := range ids {
			if s := c.srv.getSession(id); s != nil {
				s.detach(c)
			}
		}
		// NB: we deliberately do NOT close(c.out). send() may race with
		// shutdown from a session's read loop; closing would risk a
		// send-on-closed panic. writeLoop exits on c.done instead.
	})
}
