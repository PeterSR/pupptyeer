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
const Version = "0.5.1"

// Client is a connection to the daemon. Safe for concurrent use.
type Client struct {
	nc         net.Conn
	socketPath string // retained for AttachRaw, which dials the sibling raw socket

	writeMu sync.Mutex
	enc     *encoder

	mu      sync.Mutex
	nextID  int
	pending map[int]chan Message

	events chan Message
	closed chan struct{}
	once   sync.Once
}

// Dial connects to the daemon at socketPath.
func Dial(socketPath string) (*Client, error) {
	nc, err := net.Dial("unix", socketPath)
	if err != nil {
		return nil, err
	}
	c := &Client{
		nc:         nc,
		socketPath: socketPath,
		enc:        newEncoder(nc),
		pending:    make(map[int]chan Message),
		events:     make(chan Message, 1024),
		closed:     make(chan struct{}),
	}
	go c.readLoop()
	return c, nil
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

// NewSession spawns command in a fresh PTY and returns its session id.
func (c *Client) NewSession(command string, args []string, cwd string, env map[string]string, cols, rows int, opts ...SessionOption) (string, error) {
	m := Message{
		Type: TypeNewSession, Command: command, Args: args,
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

// ListSessions returns metadata for all live sessions.
func (c *Client) ListSessions() ([]SessionInfo, error) {
	reply, err := c.call(Message{Type: TypeListSessions})
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
// on Events). cols/rows is this client's desired size (0 = don't vote).
func (c *Client) Attach(session string, cols, rows int) error {
	_, err := c.call(Message{Type: TypeAttach, Session: session, Cols: cols, Rows: rows})
	return err
}

// Detach stops this connection's subscription to session.
func (c *Client) Detach(session string) error {
	return c.send(Message{Type: TypeDetach, Session: session})
}

// WritePane sends raw bytes to the session's PTY input.
func (c *Client) WritePane(session string, data []byte) error {
	return c.send(Message{Type: TypeWritePane, Session: session, Data: EncodeData(data)})
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
func (c *Client) AttachRaw(session string) (net.Conn, error) {
	nc, err := net.Dial("unix", rawSocketPath(c.socketPath))
	if err != nil {
		return nil, err
	}
	if _, err := nc.Write([]byte(session + "\n")); err != nil {
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
	m := Message{Type: TypeCapturePane, Session: session}
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
	m := Message{Type: TypeCapturePane, Session: session, Render: true}
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
func (c *Client) Resize(session string, cols, rows int) error {
	return c.send(Message{Type: TypeResize, Session: session, Cols: cols, Rows: rows})
}

// Kill terminates the session's PTY.
func (c *Client) Kill(session string) error {
	_, err := c.call(Message{Type: TypeKill, Session: session})
	return err
}

// GC reaps every session idle (no PTY input or output) for at least
// maxIdleSeconds and returns metadata for the sessions it killed.
// maxIdleSeconds <= 0 reaps every session.
func (c *Client) GC(maxIdleSeconds int) ([]SessionInfo, error) {
	reply, err := c.call(Message{Type: TypeGC, MaxIdleSeconds: maxIdleSeconds})
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
