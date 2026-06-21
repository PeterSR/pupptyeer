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

// defaultNamespace is the namespace a session lands in when a message
// carries none. Session identity is (namespace, id): ids are unique within
// a namespace but may repeat across namespaces. A client that never sends a
// namespace operates entirely here, so the change is backward compatible.
const defaultNamespace = "default"

// nsOf normalises a (possibly empty) namespace from the wire to the value
// the registry keys on.
func nsOf(ns string) string {
	if ns == "" {
		return defaultNamespace
	}
	return ns
}

// sessKey is the registry key: a session is addressed by (namespace, id).
type sessKey struct {
	ns string
	id string
}

// Server accepts connections and routes their multiplexed messages to a
// shared session registry.
type Server struct {
	ln    net.Listener
	rawLn net.Listener // optional raw firehose listener (see raw.go); nil if not enabled

	mu       sync.Mutex
	conns    map[*conn]struct{}
	sessions map[sessKey]*session
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
		sessions: make(map[sessKey]*session),
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
	if s.rawLn != nil {
		_ = s.rawLn.Close() // stops serveRaw's accept loop
	}
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

// addSession registers sess and returns the session now mapped to its id. If a
// live session already holds the id (a concurrent create with the same
// requested_id raced us), it is NOT overwritten and that existing session is
// returned instead; the caller must discard the loser it just spawned. A dead
// session under the same id is replaced (caller-supplied ids are reusable).
func (s *Server) addSession(sess *session) *session {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := sessKey{sess.namespace, sess.id}
	if cur, ok := s.sessions[key]; ok && cur.core.Alive() {
		return cur
	}
	s.sessions[key] = sess
	return sess
}

// removeSession deregisters sess, but only if the registry still maps its id to
// THIS session. Caller-supplied ids are reusable, so a session can die while a
// newer session already holds the same id; a blind delete-by-id would evict the
// live newcomer and orphan its PTY. The identity check makes the dying session's
// onExit a no-op in that case.
func (s *Server) removeSession(sess *session) {
	s.mu.Lock()
	key := sessKey{sess.namespace, sess.id}
	if s.sessions[key] == sess {
		delete(s.sessions, key)
	}
	s.mu.Unlock()
}

func (s *Server) getSession(ns, id string) *session {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sessions[sessKey{nsOf(ns), id}]
}

// listSessions returns metadata for sessions in namespace ns, or across every
// namespace when all is true (the explicit cross-cutting view).
func (s *Server) listSessions(ns string, all bool) []protocol.SessionInfo {
	ns = nsOf(ns)
	s.mu.Lock()
	sessions := make([]*session, 0, len(s.sessions))
	for key, sess := range s.sessions {
		if all || key.ns == ns {
			sessions = append(sessions, sess)
		}
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
// just before each kill. maxIdleSeconds <= 0 reaps every session. Scoping
// matches list: namespace ns only, or every namespace when all is true, so
// an app cannot reap another namespace's idle sessions.
func (s *Server) gc(maxIdleSeconds int, ns string, all bool) []protocol.SessionInfo {
	if maxIdleSeconds < 0 {
		maxIdleSeconds = 0
	}
	ns = nsOf(ns)
	cutoff := time.Now().Add(-time.Duration(maxIdleSeconds) * time.Second)

	s.mu.Lock()
	victims := make([]*session, 0)
	for key, sess := range s.sessions {
		if !all && key.ns != ns {
			continue
		}
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
	attached map[sessKey]bool
}

func newConn(srv *Server, nc net.Conn) *conn {
	return &conn{
		srv:      srv,
		nc:       nc,
		out:      make(chan []byte, outboundQueue),
		done:     make(chan struct{}),
		attached: make(map[sessKey]bool),
	}
}

func (c *conn) addSession(ns, id string) {
	c.mu.Lock()
	c.attached[sessKey{ns, id}] = true
	c.mu.Unlock()
}

func (c *conn) dropSession(ns, id string) {
	c.mu.Lock()
	delete(c.attached, sessKey{ns, id})
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
		c.send(protocol.Message{Type: protocol.TypeSessions, ID: m.ID, Sessions: c.srv.listSessions(m.Namespace, m.All)})
	case protocol.TypeAttach:
		c.handleAttach(m)
	case protocol.TypeDetach:
		if s := c.srv.getSession(m.Namespace, m.Session); s != nil {
			s.detach(c)
		}
	case protocol.TypeWritePane:
		c.handleWrite(m)
	case protocol.TypeCapturePane:
		c.handleCapture(m)
	case protocol.TypeResize:
		if s := c.srv.getSession(m.Namespace, m.Session); s != nil {
			s.resizeFrom(c, m.Cols, m.Rows)
		}
	case protocol.TypeKill:
		c.handleKill(m)
	case protocol.TypeGC:
		c.send(protocol.Message{Type: protocol.TypeReaped, ID: m.ID, Sessions: c.srv.gc(m.MaxIdleSeconds, m.Namespace, m.All)})
	default:
		c.sendError(m.ID, m.Namespace, m.Session, "unknown type: "+m.Type)
	}
}

func (c *conn) handleNewSession(m protocol.Message) {
	ns := nsOf(m.Namespace)
	// Caller-supplied id: with GetOrCreate, an alive session already holding
	// the id is returned as-is (continuation); without it, a live clash errors.
	// The clash check is per-namespace: the same id may live in two namespaces.
	if m.RequestedID != "" {
		if existing := c.srv.getSession(ns, m.RequestedID); existing != nil && existing.core.Alive() {
			if !m.GetOrCreate {
				c.sendError(m.ID, ns, m.RequestedID, "session id already exists")
				return
			}
			c.send(protocol.Message{Type: protocol.TypeOK, ID: m.ID, Namespace: ns, Session: existing.id})
			return
		}
	}
	s, err := newSession(c.srv, m)
	if err != nil {
		c.sendError(m.ID, ns, "", err.Error())
		return
	}
	if winner := c.srv.addSession(s); winner != s {
		// A concurrent create with the same requested_id beat us to the
		// registry. Discard our just-spawned duplicate so we don't orphan a
		// live PTY, then honour the same get_or_create semantics as the top.
		s.kill()
		if !m.GetOrCreate {
			c.sendError(m.ID, ns, m.RequestedID, "session id already exists")
			return
		}
		s = winner
	}
	c.send(protocol.Message{Type: protocol.TypeOK, ID: m.ID, Namespace: s.namespace, Session: s.id})
}

func (c *conn) handleAttach(m protocol.Message) {
	s := c.srv.getSession(m.Namespace, m.Session)
	if s == nil {
		c.sendError(m.ID, m.Namespace, m.Session, "session not found")
		return
	}
	c.send(protocol.Message{Type: protocol.TypeAttached, ID: m.ID, Namespace: s.namespace, Session: s.id})
	s.attach(c, m.Cols, m.Rows)
}

func (c *conn) handleWrite(m protocol.Message) {
	s := c.srv.getSession(m.Namespace, m.Session)
	if s == nil {
		c.sendError(m.ID, m.Namespace, m.Session, "session not found")
		return
	}
	var b []byte
	if m.Data != "" {
		dec, err := protocol.DecodeData(m.Data)
		if err != nil {
			c.sendError(m.ID, s.namespace, m.Session, "bad base64 data: "+err.Error())
			return
		}
		b = dec
	} else {
		b = []byte(m.Text)
	}
	if err := s.write(b); err != nil {
		c.sendError(m.ID, s.namespace, m.Session, "write failed: "+err.Error())
	}
}

func (c *conn) handleCapture(m protocol.Message) {
	s := c.srv.getSession(m.Namespace, m.Session)
	if s == nil {
		c.sendError(m.ID, m.Namespace, m.Session, "session not found")
		return
	}
	// Optionally wait for the screen to go quiet before snapshotting, then
	// snapshot under a bound so a wedged read loop can never hang the client
	// forever (with the emulator drained this always completes immediately).
	settle := time.Duration(m.SettleMs) * time.Millisecond
	timeout := time.Duration(m.TimeoutMs) * time.Millisecond
	if m.Render {
		if s.core.Raw() {
			c.sendError(m.ID, s.namespace, m.Session, "rendered capture is unavailable on a raw session (created with raw:true); use capture without render for raw scrollback")
			return
		}
		scr, err := s.core.CaptureScreen(settle, timeout)
		if err != nil {
			c.sendError(m.ID, s.namespace, m.Session, err.Error())
			return
		}
		cur := protocol.Cursor{Row: scr.Cursor.Row, Col: scr.Cursor.Col, Visible: scr.Cursor.Visible}
		c.send(protocol.Message{
			Type: protocol.TypeCapture, ID: m.ID, Namespace: s.namespace, Session: s.id,
			Cols: scr.Cols, Rows: scr.Rows, Lines: scr.Lines, Cursor: &cur, AltScreen: scr.AltScreen,
		})
		return
	}
	data, err := s.core.CaptureRaw(settle, timeout)
	if err != nil {
		c.sendError(m.ID, s.namespace, m.Session, err.Error())
		return
	}
	c.send(protocol.Message{Type: protocol.TypeCapture, ID: m.ID, Namespace: s.namespace, Session: s.id, Data: protocol.EncodeData(data)})
}

func (c *conn) handleKill(m protocol.Message) {
	s := c.srv.getSession(m.Namespace, m.Session)
	if s == nil {
		c.sendError(m.ID, m.Namespace, m.Session, "session not found")
		return
	}
	s.kill()
	c.send(protocol.Message{Type: protocol.TypeOK, ID: m.ID, Namespace: s.namespace, Session: m.Session})
}

func (c *conn) sendError(id int, namespace, session, msg string) {
	c.send(protocol.Message{Type: protocol.TypeError, ID: id, Namespace: namespace, Session: session, Message: msg})
}

// shutdown closes the connection and detaches it from every session it
// was attached to. Sessions are NOT killed - they outlive the client.
func (c *conn) shutdown() {
	c.once.Do(func() {
		close(c.done)
		_ = c.nc.Close()
		c.mu.Lock()
		keys := make([]sessKey, 0, len(c.attached))
		for key := range c.attached {
			keys = append(keys, key)
		}
		c.mu.Unlock()
		for _, key := range keys {
			if s := c.srv.getSession(key.ns, key.id); s != nil {
				s.detach(c)
			}
		}
		// NB: we deliberately do NOT close(c.out). send() may race with
		// shutdown from a session's read loop; closing would risk a
		// send-on-closed panic. writeLoop exits on c.done instead.
	})
}
