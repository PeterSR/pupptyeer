// Package client is a thin, dependency-free Go client for the
// pupptyeer daemon. It dials the unix socket, correlates id-tagged
// request/replies, and surfaces unsolicited messages (output, exit,
// session_closed) on an Events channel for the caller to consume.
//
// It lives in its own module (github.com/PeterSR/pupptyeer/clients/go)
// so importing it pulls in nothing but the standard library - the wire
// types and codec are inlined in wire.go, kept in parity with PROTOCOL.md.
package client

import (
	"bufio"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
)

// Version is the released version of this client, kept in step with the
// pupptyeer project release (see PROTOCOL.md / git tags).
const Version = "0.10.2"

// DefaultNamespace is the namespace a connection uses when none is given.
// Session identity is (namespace, id): ids are unique within a namespace but
// may repeat across namespaces. A client that never sets a namespace operates
// entirely here, matching pre-namespace behaviour.
const DefaultNamespace = "default"

// Client is a connection to the daemon. Safe for concurrent use.
type Client struct {
	nc         net.Conn
	socketPath string // retained for AttachRaw, which dials the sibling raw socket
	namespace  string // connection default namespace; per-call args override it

	writeMu sync.Mutex
	enc     *encoder

	mu      sync.Mutex
	nextID  int
	pending map[int]chan Message

	events chan Message
	closed chan struct{}
	once   sync.Once
}

// Dial connects to the daemon at socketPath in the default namespace. It is
// the low-level entry point: it returns the raw dial error verbatim and does
// no socket resolution. Prefer Connect for the connect-or-scream behaviour
// (default socket resolution + one actionable error + a chosen namespace).
func Dial(socketPath string) (*Client, error) {
	nc, err := net.Dial("unix", socketPath)
	if err != nil {
		return nil, err
	}
	c := &Client{
		nc:         nc,
		socketPath: socketPath,
		namespace:  DefaultNamespace,
		enc:        newEncoder(nc),
		pending:    make(map[int]chan Message),
		events:     make(chan Message, 1024),
		closed:     make(chan struct{}),
	}
	go c.readLoop()
	return c, nil
}

// ConnectOption configures Connect.
type ConnectOption func(*connectConfig)

type connectConfig struct {
	socket    string
	namespace string
}

// WithSocket sets an explicit daemon socket path. Without it, Connect resolves
// the default location via DefaultSocketPath.
func WithSocket(path string) ConnectOption {
	return func(cc *connectConfig) { cc.socket = path }
}

// WithNamespace sets the connection's default namespace (kubectl-context
// style): every session call inherits it unless overridden per call. Empty
// means DefaultNamespace.
func WithNamespace(ns string) ConnectOption {
	return func(cc *connectConfig) { cc.namespace = ns }
}

// Connect is the single entry point apps should use: it resolves the default
// socket (unless WithSocket is given), dials it, and on an unreachable daemon
// returns ONE canonical, actionable error instead of a bare syscall string. It
// never spawns a daemon - lifecycle is a supervisor/system-package concern.
func Connect(opts ...ConnectOption) (*Client, error) {
	cc := connectConfig{namespace: DefaultNamespace}
	for _, o := range opts {
		o(&cc)
	}
	sock := cc.socket
	if sock == "" {
		sock = DefaultSocketPath()
	}
	c, err := Dial(sock)
	if err != nil {
		return nil, fmt.Errorf("no pupptyeer daemon at %s; install/start it (`pupptyeer daemon install`) or set PUPPTYEER_SOCK: %w", sock, err)
	}
	if cc.namespace != "" {
		c.namespace = cc.namespace
	}
	return c, nil
}

// Namespace returns the connection's default namespace.
func (c *Client) Namespace() string { return c.namespace }

// ns resolves a per-call namespace override against the connection default:
// the first non-empty override wins, else the connection default.
func (c *Client) ns(override ...string) string {
	if len(override) > 0 && override[0] != "" {
		return override[0]
	}
	return c.namespace
}

// Events returns the channel of unsolicited server messages (output,
// scrollback_end, exit, session_closed). Closed when the connection ends.
//
// You MUST drain this channel for any connection that has attached to a
// session. It is a live byte stream: the reader goroutine applies
// backpressure (blocks) rather than dropping output when the channel
// fills, so an attached connection that ignores Events will eventually
// stall its own request/reply calls too. Connections that only issue
// request/reply calls (never Attach) produce no output events and need
// not drain it.
func (c *Client) Events() <-chan Message { return c.events }

// Close closes the connection.
func (c *Client) Close() error {
	c.once.Do(func() { close(c.closed); _ = c.nc.Close() })
	return nil
}

func (c *Client) readLoop() {
	dec := newDecoder(c.nc)
	for {
		m, err := dec.Decode()
		if err != nil {
			c.failPending(err)
			close(c.events)
			c.Close()
			return
		}
		// id-tagged replies route to their waiter; everything else is
		// an event.
		if m.ID != 0 {
			c.mu.Lock()
			ch, ok := c.pending[m.ID]
			if ok {
				delete(c.pending, m.ID)
			}
			c.mu.Unlock()
			if ok {
				ch <- m
				continue
			}
		}
		select {
		case c.events <- m:
		case <-c.closed:
			return
		}
	}
}

func (c *Client) failPending(err error) {
	c.mu.Lock()
	for id, ch := range c.pending {
		ch <- Message{Type: TypeError, ID: id, Message: err.Error()}
		delete(c.pending, id)
	}
	c.mu.Unlock()
}

// send writes a fire-and-forget message (no reply expected).
func (c *Client) send(m Message) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return c.enc.Encode(m)
}

// call sends a request with a fresh id and waits for the matching reply.
func (c *Client) call(m Message) (Message, error) {
	c.mu.Lock()
	c.nextID++
	id := c.nextID
	ch := make(chan Message, 1)
	c.pending[id] = ch
	c.mu.Unlock()
	m.ID = id

	if err := c.send(m); err != nil {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return Message{}, err
	}
	select {
	case reply := <-ch:
		if reply.Type == TypeError {
			return reply, errors.New(reply.Message)
		}
		return reply, nil
	case <-c.closed:
		return Message{}, errors.New("connection closed")
	}
}

// SessionOption tunes a NewSession call.
type SessionOption func(*Message)

// WithRaw creates a raw session: the daemon runs no terminal emulator for it,
// lowering CPU and latency. Rendered capture (CaptureScreen / capture with
// render) is unavailable on a raw session; raw scrollback capture still works.
// Pairs naturally with AttachRaw for a maximally fast path.
func WithRaw() SessionOption { return func(m *Message) { m.Raw = true } }

// WithSessionID requests that the new session use id instead of a
// daemon-generated UUID. Without WithGetOrCreate, creating a session whose id
// is already live is an error.
func WithSessionID(id string) SessionOption { return func(m *Message) { m.RequestedID = id } }

// WithGetOrCreate makes NewSession return an existing alive session that holds
// the requested id (set via WithSessionID) instead of spawning a new process.
// It is the building block for continuation: same id, same live program.
func WithGetOrCreate() SessionOption { return func(m *Message) { m.GetOrCreate = true } }

// InNamespace overrides the connection's default namespace for this one
// NewSession/EnsureSession call (a per-call override of the kubectl-context
// connection default). An empty ns is a no-op (keeps the connection default),
// matching the per-call override semantics of the other methods.
func InNamespace(ns string) SessionOption {
	return func(m *Message) {
		if ns != "" {
			m.Namespace = ns
		}
	}
}

// NewSession spawns command in a fresh PTY and returns its session id. The
// session is created in the connection's default namespace unless InNamespace
// overrides it.
func (c *Client) NewSession(command string, args []string, cwd string, env map[string]string, cols, rows int, opts ...SessionOption) (string, error) {
	m := Message{
		Type: TypeNewSession, Namespace: c.namespace, Command: command, Args: args,
		Cwd: cwd, Env: env, Cols: cols, Rows: rows,
	}
	for _, o := range opts {
		o(&m)
	}
	reply, err := c.call(m)
	if err != nil {
		return "", err
	}
	return reply.Session, nil
}

// EnsureSession attaches to the semantics of "continue if alive, else create":
// if an alive session already holds id it is returned (created=false); otherwise
// a new session is spawned with that id (created=true). It is a thin wrapper over
// NewSession with WithSessionID + WithGetOrCreate. command/args/cwd/env/cols/rows
// are used only when a session is actually created.
func (c *Client) EnsureSession(id, command string, args []string, cwd string, env map[string]string, cols, rows int, opts ...SessionOption) (created bool, err error) {
	// Resolve the target namespace the same way NewSession will (connection
	// default unless an InNamespace option overrides it) so the existence check
	// looks in the right namespace.
	probe := Message{Namespace: c.namespace}
	for _, o := range opts {
		o(&probe)
	}
	infos, err := c.ListSessions(probe.Namespace)
	if err != nil {
		return false, err
	}
	for _, info := range infos {
		if info.ID == id && info.Alive {
			return false, nil
		}
	}
	opts = append(opts, WithSessionID(id), WithGetOrCreate())
	_, err = c.NewSession(command, args, cwd, env, cols, rows, opts...)
	if err != nil {
		return false, err
	}
	return true, nil
}

// ListSessions returns metadata for live sessions in the given namespace
// (the first argument, if any) or the connection's default namespace. Use
// ListAllSessions for a cross-namespace view.
func (c *Client) ListSessions(ns ...string) ([]SessionInfo, error) {
	return c.listSessions(Message{Type: TypeListSessions, Namespace: c.ns(ns...)})
}

// ListAllSessions returns metadata for live sessions across every namespace.
func (c *Client) ListAllSessions() ([]SessionInfo, error) {
	return c.listSessions(Message{Type: TypeListSessions, All: true})
}

func (c *Client) listSessions(m Message) ([]SessionInfo, error) {
	reply, err := c.call(m)
	if err != nil {
		return nil, err
	}
	if reply.Sessions == nil {
		// omitempty drops an empty list on the wire; normalise to a
		// non-nil empty slice so callers (and JSON) see [] not null.
		return []SessionInfo{}, nil
	}
	return reply.Sessions, nil
}

// Attach subscribes this connection to session's live output (delivered
// on Events). cols/rows is this client's desired size (0 = don't vote). An
// optional trailing namespace overrides the connection default.
func (c *Client) Attach(session string, cols, rows int, ns ...string) error {
	_, err := c.call(Message{Type: TypeAttach, Namespace: c.ns(ns...), Session: session, Cols: cols, Rows: rows})
	return err
}

// Detach stops this connection's subscription to session.
func (c *Client) Detach(session string, ns ...string) error {
	return c.send(Message{Type: TypeDetach, Namespace: c.ns(ns...), Session: session})
}

// WritePane sends raw bytes to the session's PTY input.
func (c *Client) WritePane(session string, data []byte, ns ...string) error {
	return c.send(Message{Type: TypeWritePane, Namespace: c.ns(ns...), Session: session, Data: EncodeData(data)})
}

// AttachRaw opens a raw firehose connection to session over the daemon's
// sibling raw socket (<sock>.raw): an unframed, base64/JSON-free byte pipe to
// the PTY for throughput/latency-sensitive consumers. Read raw PTY output from
// the returned conn; write raw input bytes to it. Closing it detaches (it does
// NOT kill the session); EOF means the session ended.
//
// This is an optional fast path, deliberately outside the core NDJSON wire
// protocol and the client parity matrix (see PROTOCOL.md). It carries no
// framing, so it streams a single session with no exit code or scrollback
// marker - use the regular NDJSON connection for control and metadata. Pair it
// with a session created via WithRaw to also skip terminal emulation.
func (c *Client) AttachRaw(session string, ns ...string) (net.Conn, error) {
	nc, err := net.Dial("unix", rawSocketPath(c.socketPath))
	if err != nil {
		return nil, err
	}
	// Handshake addresses a session by (namespace, id): "<namespace>\t<id>".
	// (A bare "<id>" would mean the default namespace, but we always send the
	// resolved namespace explicitly.)
	if _, err := nc.Write([]byte(c.ns(ns...) + "\t" + session + "\n")); err != nil {
		_ = nc.Close()
		return nil, err
	}
	r := bufio.NewReader(nc)
	status, err := r.ReadString('\n')
	if err != nil {
		_ = nc.Close()
		return nil, err
	}
	if s := strings.TrimSpace(status); s != "OK" {
		_ = nc.Close()
		return nil, fmt.Errorf("raw attach: %s", s)
	}
	// Reads go through r so any output bytes buffered past the status line are
	// preserved; writes go straight to the socket.
	return &rawConn{Conn: nc, r: r}, nil
}

// rawConn is the net.Conn returned by AttachRaw. It reads through a buffered
// reader (to keep bytes read past the handshake) and writes to the raw socket.
type rawConn struct {
	net.Conn
	r *bufio.Reader
}

func (rc *rawConn) Read(p []byte) (int, error) { return rc.r.Read(p) }

// rawSocketPath mirrors the daemon's ".raw" suffix convention (the wire/codec
// copy pattern: kept in parity with internal/server.RawSocketPath).
func rawSocketPath(ndjsonSocket string) string { return ndjsonSocket + ".raw" }

// CaptureOption tunes a capture call. Use WithSettle/WithTimeout to wait
// for the screen to go quiet before snapshotting.
type CaptureOption func(*Message)

// WithSettle holds the capture reply until the PTY has produced no output
// for a continuous ms window. ms <= 0 disables waiting (the default).
func WithSettle(ms int) CaptureOption { return func(m *Message) { m.SettleMs = ms } }

// WithTimeout caps the total settle wait. ms <= 0 uses the daemon default.
func WithTimeout(ms int) CaptureOption { return func(m *Message) { m.TimeoutMs = ms } }

// WithCaptureNamespace overrides the connection's default namespace for this
// one capture call (the per-call override; the SessionOption equivalent for
// new_session is InNamespace). An empty ns is a no-op (keeps the connection
// default), matching the per-call override semantics of the other methods.
func WithCaptureNamespace(ns string) CaptureOption {
	return func(m *Message) {
		if ns != "" {
			m.Namespace = ns
		}
	}
}

// Screen is the rendered visible terminal grid returned by CaptureScreen.
// Lines holds exactly Rows strings, each space-padded to Cols.
type Screen struct {
	Cols, Rows int
	Lines      []string
	Cursor     Cursor
	AltScreen  bool
}

// CapturePane returns a snapshot of the session's raw scrollback bytes.
// With WithSettle, it first waits for the screen to go quiet.
func (c *Client) CapturePane(session string, opts ...CaptureOption) ([]byte, error) {
	m := Message{Type: TypeCapturePane, Namespace: c.namespace, Session: session}
	for _, o := range opts {
		o(&m)
	}
	reply, err := c.call(m)
	if err != nil {
		return nil, err
	}
	return DecodeData(reply.Data)
}

// CaptureScreen returns the daemon's authoritative rendered screen (the
// visible grid, not scrollback). With WithSettle, it first waits for the
// screen to go quiet - the usual way to read a TUI after sending input.
func (c *Client) CaptureScreen(session string, opts ...CaptureOption) (*Screen, error) {
	m := Message{Type: TypeCapturePane, Namespace: c.namespace, Session: session, Render: true}
	for _, o := range opts {
		o(&m)
	}
	reply, err := c.call(m)
	if err != nil {
		return nil, err
	}
	scr := &Screen{Cols: reply.Cols, Rows: reply.Rows, Lines: reply.Lines, AltScreen: reply.AltScreen}
	if reply.Lines == nil {
		scr.Lines = []string{}
	}
	if reply.Cursor != nil {
		scr.Cursor = *reply.Cursor
	}
	return scr, nil
}

// Resize updates this client's desired size for the session (effective
// size is the smallest across attached clients).
func (c *Client) Resize(session string, cols, rows int, ns ...string) error {
	return c.send(Message{Type: TypeResize, Namespace: c.ns(ns...), Session: session, Cols: cols, Rows: rows})
}

// Kill terminates the session's PTY.
func (c *Client) Kill(session string, ns ...string) error {
	_, err := c.call(Message{Type: TypeKill, Namespace: c.ns(ns...), Session: session})
	return err
}

// GC reaps every session idle (no PTY input or output) for at least
// maxIdleSeconds in the given namespace (the first argument, if any) or the
// connection's default namespace, and returns metadata for the sessions it
// killed. maxIdleSeconds <= 0 reaps every (matching) session. Use GCAll to
// reap across every namespace.
func (c *Client) GC(maxIdleSeconds int, ns ...string) ([]SessionInfo, error) {
	return c.gc(Message{Type: TypeGC, MaxIdleSeconds: maxIdleSeconds, Namespace: c.ns(ns...)})
}

// GCAll reaps idle sessions across every namespace.
func (c *Client) GCAll(maxIdleSeconds int) ([]SessionInfo, error) {
	return c.gc(Message{Type: TypeGC, MaxIdleSeconds: maxIdleSeconds, All: true})
}

func (c *Client) gc(m Message) ([]SessionInfo, error) {
	reply, err := c.call(m)
	if err != nil {
		return nil, err
	}
	if reply.Sessions == nil {
		// omitempty drops an empty list on the wire; normalise to a
		// non-nil empty slice so callers (and JSON) see [] not null.
		return []SessionInfo{}, nil
	}
	return reply.Sessions, nil
}
