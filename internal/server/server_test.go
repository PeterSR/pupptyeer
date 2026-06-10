package server_test

import (
	"bytes"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	client "github.com/PeterSR/pupptyeer/clients/go"
	"github.com/PeterSR/pupptyeer/internal/protocol"
	"github.com/PeterSR/pupptyeer/internal/server"
)

func startServer(t *testing.T) string {
	t.Helper()
	sock := filepath.Join(t.TempDir(), "d.sock")
	srv, err := server.New(sock)
	if err != nil {
		t.Fatalf("server.New: %v", err)
	}
	go func() { _ = srv.Serve() }()
	t.Cleanup(func() { _ = srv.Close() })
	return sock
}

// readUntil drains c.Events for the given session, accumulating output
// bytes, until pred(accumulated) is true or the deadline elapses.
func readUntil(t *testing.T, c *client.Client, session string, pred func([]byte) bool) []byte {
	t.Helper()
	var acc bytes.Buffer
	deadline := time.After(5 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatalf("timeout; accumulated %q", acc.String())
		case m, ok := <-c.Events():
			if !ok {
				t.Fatalf("events closed; accumulated %q", acc.String())
			}
			if m.Session != session {
				continue
			}
			if m.Type == protocol.TypeOutput {
				b, _ := client.OutputBytes(m)
				acc.Write(b)
				if pred(acc.Bytes()) {
					return acc.Bytes()
				}
			}
		}
	}
}

// waitFor polls until pred() is true or the deadline elapses.
func waitFor(t *testing.T, what string, pred func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if pred() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for %s", what)
}

// TestDriveEndToEnd is the load-bearing test: create → attach → send →
// read live → detach → reattach → assert scrollback replay → kill.
func TestDriveEndToEnd(t *testing.T) {
	sock := startServer(t)

	a, err := client.Dial(sock)
	if err != nil {
		t.Fatalf("dial A: %v", err)
	}
	defer a.Close()

	id, err := a.NewSession("cat", nil, "", nil, 80, 24)
	if err != nil {
		t.Fatalf("new_session: %v", err)
	}
	if id == "" {
		t.Fatal("empty session id")
	}

	if err := a.Attach(id, 80, 24); err != nil {
		t.Fatalf("attach A: %v", err)
	}

	marker := fmt.Sprintf("MARKER-%d-end", time.Now().UnixNano())
	if err := a.WritePane(id, []byte(marker+"\n")); err != nil {
		t.Fatalf("write_pane: %v", err)
	}
	readUntil(t, a, id, func(acc []byte) bool {
		return bytes.Contains(acc, []byte(marker))
	})

	// Detach A; attach a fresh client B and assert it replays the marker
	// from scrollback.
	if err := a.Detach(id); err != nil {
		t.Fatalf("detach: %v", err)
	}

	b, err := client.Dial(sock)
	if err != nil {
		t.Fatalf("dial B: %v", err)
	}
	defer b.Close()
	if err := b.Attach(id, 80, 24); err != nil {
		t.Fatalf("attach B: %v", err)
	}
	replay := readUntil(t, b, id, func(acc []byte) bool {
		return bytes.Contains(acc, []byte(marker))
	})
	if !bytes.Contains(replay, []byte(marker)) {
		t.Fatalf("scrollback replay missing marker; got %q", replay)
	}

	// capture_pane should also contain it.
	cap, err := b.CapturePane(id)
	if err != nil {
		t.Fatalf("capture_pane: %v", err)
	}
	if !bytes.Contains(cap, []byte(marker)) {
		t.Fatalf("capture missing marker; got %q", cap)
	}

	// Kill and confirm the session leaves the registry.
	if err := b.Kill(id); err != nil {
		t.Fatalf("kill: %v", err)
	}
	waitFor(t, "session removed", func() bool {
		sessions, err := a.ListSessions()
		if err != nil {
			return false
		}
		for _, s := range sessions {
			if s.ID == id {
				return false
			}
		}
		return true
	})
}

// TestListAndResizeArbitration checks list metadata and that the
// effective size is the smallest across attached clients.
func TestListAndResizeArbitration(t *testing.T) {
	sock := startServer(t)
	a, err := client.Dial(sock)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer a.Close()

	id, err := a.NewSession("cat", nil, "", nil, 120, 40)
	if err != nil {
		t.Fatalf("new_session: %v", err)
	}
	if err := a.Attach(id, 120, 40); err != nil {
		t.Fatalf("attach A: %v", err)
	}

	b, err := client.Dial(sock)
	if err != nil {
		t.Fatalf("dial B: %v", err)
	}
	defer b.Close()
	if err := b.Attach(id, 80, 24); err != nil { // smaller
		t.Fatalf("attach B: %v", err)
	}

	waitFor(t, "size shrinks to smallest", func() bool {
		sessions, _ := a.ListSessions()
		for _, s := range sessions {
			if s.ID == id {
				return s.Cols == 80 && s.Rows == 24 && s.Attached == 2
			}
		}
		return false
	})

	_ = b.Kill(id)
}

// waitEvent drains c.Events for session until a message of the given
// type arrives, or the deadline elapses.
func waitEvent(t *testing.T, c *client.Client, session, typ string, d time.Duration) bool {
	t.Helper()
	deadline := time.After(d)
	for {
		select {
		case <-deadline:
			return false
		case m, ok := <-c.Events():
			if !ok {
				return false
			}
			if m.Session == session && m.Type == typ {
				return true
			}
		}
	}
}

// TestAttachDuringActiveOutput attaches a second client while the session
// is actively producing output. It guards the attach race fix: holding
// s.mu across the scrollback replay must not deadlock with readLoop's
// broadcast, and live output must still flow to the new client after
// scrollback_end.
func TestAttachDuringActiveOutput(t *testing.T) {
	sock := startServer(t)
	a, err := client.Dial(sock)
	if err != nil {
		t.Fatalf("dial A: %v", err)
	}
	defer a.Close()
	id, err := a.NewSession("cat", nil, "", nil, 80, 24)
	if err != nil {
		t.Fatalf("new_session: %v", err)
	}
	if err := a.Attach(id, 80, 24); err != nil {
		t.Fatalf("attach A: %v", err)
	}

	// Drive a steady stream of output via cat's echo.
	stop := make(chan struct{})
	go func() {
		i := 0
		for {
			select {
			case <-stop:
				return
			default:
			}
			_ = a.WritePane(id, []byte(fmt.Sprintf("L%d\n", i)))
			i++
			time.Sleep(3 * time.Millisecond)
		}
	}()
	// Drain A so its outbound queue can't fill while we work.
	go func() {
		for {
			select {
			case <-stop:
				return
			case <-a.Events():
			}
		}
	}()
	time.Sleep(60 * time.Millisecond) // let some output accumulate

	b, err := client.Dial(sock)
	if err != nil {
		close(stop)
		t.Fatalf("dial B: %v", err)
	}
	defer b.Close()
	if err := b.Attach(id, 80, 24); err != nil { // must not deadlock under active output
		close(stop)
		t.Fatalf("attach B during active output: %v", err)
	}
	if !waitEvent(t, b, id, "scrollback_end", 5*time.Second) {
		close(stop)
		t.Fatal("B never received scrollback_end")
	}
	// A marker written after the replay must reach B live.
	marker := fmt.Sprintf("POSTATTACH-%d", time.Now().UnixNano())
	if err := a.WritePane(id, []byte(marker+"\n")); err != nil {
		close(stop)
		t.Fatalf("write marker: %v", err)
	}
	got := readUntil(t, b, id, func(acc []byte) bool { return bytes.Contains(acc, []byte(marker)) })
	if !bytes.Contains(got, []byte(marker)) {
		close(stop)
		t.Fatalf("post-attach marker not delivered live to B")
	}
	close(stop)
	_ = b.Kill(id)
}

func TestProtocolRoundTrip(t *testing.T) {
	code := 7
	in := protocol.Message{Type: protocol.TypeExit, Session: "abc", ExitCode: &code}
	line, err := protocol.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(string(line), "\n") {
		t.Fatal("missing newline terminator")
	}
	dec := protocol.NewDecoder(bytes.NewReader(line))
	out, err := dec.Decode()
	if err != nil {
		t.Fatal(err)
	}
	if out.Type != protocol.TypeExit || out.Session != "abc" || out.ExitCode == nil || *out.ExitCode != 7 {
		t.Fatalf("round trip mismatch: %+v", out)
	}
}
