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
	"errors"
	"net"
	"sync"
)

// Version is the released version of this client, kept in step with the
// pupptyeer project release (see PROTOCOL.md / git tags).
const Version = "0.3.0"

// Client is a connection to the daemon. Safe for concurrent use.
type Client struct {
	nc net.Conn

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
		nc:      nc,
		enc:     newEncoder(nc),
		pending: make(map[int]chan Message),
		events:  make(chan Message, 1024),
		closed:  make(chan struct{}),
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

// NewSession spawns command in a fresh PTY and returns its session id.
func (c *Client) NewSession(command string, args []string, cwd string, env map[string]string, cols, rows int) (string, error) {
	reply, err := c.call(Message{
		Type: TypeNewSession, Command: command, Args: args,
		Cwd: cwd, Env: env, Cols: cols, Rows: rows,
	})
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
