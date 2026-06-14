// Package protocol defines the NDJSON wire format spoken between the
// pupptyeer daemon and its clients over a unix socket.
//
// One JSON object per message; clients emit them newline-delimited. Raw
// PTY bytes ride in the base64 `data` field. A single Message shape is
// used for every message; the `type` field discriminates. The design
// goal is cross-language ergonomics (Go, TS, Python clients) over raw
// throughput. See PROTOCOL.md for the canonical wire contract.
package protocol

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"io"
)

// Message types (client → server).
const (
	TypeNewSession   = "new_session"
	TypeListSessions = "list_sessions"
	TypeAttach       = "attach"
	TypeDetach       = "detach"
	TypeWritePane    = "write_pane"
	TypeCapturePane  = "capture_pane"
	TypeResize       = "resize"
	TypeKill         = "kill"
	TypeGC           = "gc"
)

// Message types (server → client).
const (
	TypeOK            = "ok"
	TypeError         = "error"
	TypeSessions      = "sessions"
	TypeAttached      = "attached"
	TypeOutput        = "output"
	TypeScrollbackEnd = "scrollback_end"
	TypeCapture       = "capture"
	TypeExit          = "exit"
	TypeSessionClosed = "session_closed"
	TypeReaped        = "reaped"
)

// Message is the single wire shape. Fields are omitempty so a given
// message only carries what its type needs. Clients read the fields
// relevant to the type they received.
type Message struct {
	Type    string `json:"type"`
	ID      int    `json:"id,omitempty"`
	Session string `json:"session,omitempty"`

	// new_session
	Command string            `json:"command,omitempty"`
	Args    []string          `json:"args,omitempty"`
	Cwd     string            `json:"cwd,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
	Cols    int               `json:"cols,omitempty"`
	Rows    int               `json:"rows,omitempty"`
	Raw     bool              `json:"raw,omitempty"` // new_session: don't run a terminal emulator for this session (no rendered capture; lower CPU/latency)

	// new_session, caller-supplied id. RequestedID, if set, becomes the
	// session's id instead of a daemon-generated UUID. With GetOrCreate, an
	// alive session already holding RequestedID is returned as-is (no new
	// process); without it, a clash is an error.
	RequestedID string `json:"requested_id,omitempty"`
	GetOrCreate bool   `json:"get_or_create,omitempty"`

	// gc
	MaxIdleSeconds int `json:"max_idle_seconds,omitempty"`

	// data-bearing (output / write_pane / capture)
	Data string `json:"data,omitempty"` // base64 of raw PTY bytes
	Text string `json:"text,omitempty"` // convenience: raw UTF-8 input for write_pane

	// capture_pane options
	Render    bool `json:"render,omitempty"`     // return the rendered grid instead of raw bytes
	SettleMs  int  `json:"settle_ms,omitempty"`  // hold reply until PTY quiet for this long
	TimeoutMs int  `json:"timeout_ms,omitempty"` // cap on settle wait; <=0 uses the default

	// rendered capture response (Render == true). Cols/Rows above carry the
	// grid dimensions; Lines/Cursor/AltScreen describe the visible screen.
	Lines     []string `json:"lines,omitempty"`
	Cursor    *Cursor  `json:"cursor,omitempty"`
	AltScreen bool     `json:"alt_screen,omitempty"`

	// responses / events
	Message  string        `json:"message,omitempty"`   // error text
	ExitCode *int          `json:"exit_code,omitempty"` // pointer so 0 is preserved
	Sessions []SessionInfo `json:"sessions,omitempty"`
}

// Cursor is the cursor position in a rendered capture. Row/Col are 0-based;
// Col may equal the grid width (a pending-wrap cursor). Visible reflects the
// terminal's cursor-visibility (DECTCEM) state.
type Cursor struct {
	Row     int  `json:"row"`
	Col     int  `json:"col"`
	Visible bool `json:"visible"`
}

// SessionInfo is the metadata returned by list_sessions (and the reaped
// list from gc). LastActivity is the RFC3339 time of the most recent
// PTY input or output; gc ages sessions by it.
type SessionInfo struct {
	ID           string   `json:"id"`
	Command      string   `json:"command"`
	Args         []string `json:"args,omitempty"`
	Cwd          string   `json:"cwd,omitempty"`
	Cols         int      `json:"cols"`
	Rows         int      `json:"rows"`
	Created      string   `json:"created"`
	LastActivity string   `json:"last_activity"`
	Attached     int      `json:"attached"`
	Alive        bool     `json:"alive"`
	Raw          bool     `json:"raw,omitempty"` // session was created with raw:true (no terminal emulator)
}

// EncodeData base64-encodes raw bytes for the `data` field.
func EncodeData(b []byte) string { return base64.StdEncoding.EncodeToString(b) }

// DecodeData decodes the `data` field back to raw bytes.
func DecodeData(s string) ([]byte, error) { return base64.StdEncoding.DecodeString(s) }

// Marshal serialises a message to a single NDJSON line (with trailing \n).
func Marshal(m Message) ([]byte, error) {
	b, err := json.Marshal(m)
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

// Encoder writes newline-delimited messages to w.
type Encoder struct {
	w io.Writer
}

// NewEncoder returns an Encoder writing to w.
func NewEncoder(w io.Writer) *Encoder { return &Encoder{w: w} }

// Encode writes one message as an NDJSON line.
func (e *Encoder) Encode(m Message) error {
	b, err := Marshal(m)
	if err != nil {
		return err
	}
	_, err = e.w.Write(b)
	return err
}

// Decoder reads newline-delimited messages from r. It uses a large
// buffer because base64 output lines can be tens of KiB.
type Decoder struct {
	sc *bufio.Scanner
}

// NewDecoder returns a Decoder reading from r.
func NewDecoder(r io.Reader) *Decoder {
	sc := bufio.NewScanner(r)
	// PTY output chunks are capped at 32 KiB → ~44 KiB base64 plus
	// envelope; 1 MiB max token leaves generous headroom.
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	return &Decoder{sc: sc}
}

// Decode reads the next message. Returns io.EOF at clean end of stream.
func (d *Decoder) Decode() (Message, error) {
	for d.sc.Scan() {
		line := d.sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var m Message
		if err := json.Unmarshal(line, &m); err != nil {
			return Message{}, err
		}
		return m, nil
	}
	if err := d.sc.Err(); err != nil {
		return Message{}, err
	}
	return Message{}, io.EOF
}
