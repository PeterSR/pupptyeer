package server

import (
	"bufio"
	"net"
	"strings"
	"sync"
)

// RawSocketPath returns the raw firehose socket path for a given NDJSON socket
// path. The ".raw" suffix convention lives here so the daemon and any client
// helper derive the same location.
func RawSocketPath(ndjsonSocket string) string { return ndjsonSocket + ".raw" }

// The raw firehose is an OPTIONAL, out-of-band fast path that lives on a
// second unix socket (<sock>.raw). It is deliberately NOT part of the NDJSON
// wire protocol or the client parity matrix - the default socket and its code
// path are untouched. A raw connection is a transparent pipe to one session's
// PTY: zero framing, no base64, no JSON, no terminal emulation. It exists for
// throughput/latency-sensitive consumers who don't need rendered capture or
// multiplexing.
//
// Handshake (newline-delimited, then pure bytes):
//
//	client → "<session-id>\n"
//	server → "OK\n"            then: raw scrollback replay, then live PTY bytes
//	      or  "ERR <message>\n" then: close
//
// After OK the stream is bidirectional and unframed: bytes the client writes go
// straight to the PTY as input; bytes the PTY produces stream straight back.
// EOF in either direction tears the pipe down. The session keeps running (a raw
// client disconnect is a detach, not a kill), exactly like an NDJSON attach.

// rawQueue bounds a raw subscriber's pending-write buffer in chunks. A raw
// client that can't keep up is dropped rather than stalling the PTY read loop
// or other subscribers - the same backpressure decision as the NDJSON path.
const rawQueue = 64

// rawConn is one connected raw firehose client subscribed to a session's
// output. Output chunks are enqueued (non-blocking) by the session read loop
// and drained to the socket by a writer goroutine.
type rawConn struct {
	nc   net.Conn
	out  chan []byte
	done chan struct{}
	once sync.Once
}

func newRawConn(nc net.Conn) *rawConn {
	return &rawConn{nc: nc, out: make(chan []byte, rawQueue), done: make(chan struct{})}
}

// enqueue queues a chunk for the writer. Non-blocking: a full queue means the
// client is too slow, so it is dropped. The chunk is the read loop's immutable
// per-iteration copy, shared read-only across all subscribers (zero-copy).
func (rc *rawConn) enqueue(b []byte) {
	select {
	case rc.out <- b:
	case <-rc.done:
	default:
		go rc.shutdown()
	}
}

func (rc *rawConn) writeLoop() {
	for {
		select {
		case b, ok := <-rc.out:
			if !ok {
				return
			}
			if _, err := rc.nc.Write(b); err != nil {
				rc.shutdown()
				return
			}
		case <-rc.done:
			return
		}
	}
}

// shutdown closes the socket (unblocking both the writer and the input reader)
// exactly once. Idempotent: called on client EOF, on a slow-client drop, and by
// the session's finish() when the child exits.
func (rc *rawConn) shutdown() {
	rc.once.Do(func() {
		close(rc.done)
		_ = rc.nc.Close()
	})
}

// ListenRaw opens the raw firehose listener at path and serves it in the
// background until Close. Additive: the main NDJSON listener is unaffected.
func (s *Server) ListenRaw(path string) error {
	ln, err := net.Listen("unix", path)
	if err != nil {
		return err
	}
	s.rawLn = ln
	go s.serveRaw()
	return nil
}

func (s *Server) serveRaw() {
	for {
		nc, err := s.rawLn.Accept()
		if err != nil {
			return // listener closed on shutdown
		}
		go s.handleRawConn(nc)
	}
}

// handleRawConn runs one raw firehose connection: read the handshake line,
// validate the session, then become a transparent bidirectional pipe.
func (s *Server) handleRawConn(nc net.Conn) {
	// bufio.Reader so any bytes the client pipelined after the handshake
	// newline are preserved and fed to the PTY (no over-read loss).
	r := bufio.NewReaderSize(nc, ptyChunk)
	line, err := r.ReadString('\n')
	if err != nil {
		_ = nc.Close()
		return
	}
	sid := strings.TrimSpace(line)
	sess := s.getSession(sid)
	if sess == nil {
		_, _ = nc.Write([]byte("ERR session not found\n"))
		_ = nc.Close()
		return
	}
	if _, err := nc.Write([]byte("OK\n")); err != nil {
		_ = nc.Close()
		return
	}

	rc := newRawConn(nc)
	go rc.writeLoop()
	sess.attachRaw(rc) // registers + replays scrollback under the session lock

	// Input plane: raw socket bytes → PTY. Reading from r (not nc) preserves
	// anything buffered past the handshake.
	buf := make([]byte, ptyChunk)
	for {
		n, rerr := r.Read(buf)
		if n > 0 {
			_ = sess.write(buf[:n])
		}
		if rerr != nil {
			break
		}
	}
	sess.detachRaw(rc)
	rc.shutdown()
}
