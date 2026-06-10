// Conformance runner (Go) - implements conformance/scenario.md.
package main

import (
	"bytes"
	"fmt"
	"os"
	"time"

	client "github.com/PeterSR/pupptyeer/clients/go"
)

func fail(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "FAIL[go] "+format+"\n", a...)
	os.Exit(1)
}

func containsID(ss []client.SessionInfo, id string) bool {
	for _, s := range ss {
		if s.ID == id {
			return true
		}
	}
	return false
}

// waitOutput drains c.Events for session until the accumulated output
// contains marker, or the deadline elapses.
func waitOutput(c *client.Client, session, marker string, d time.Duration) bool {
	var acc bytes.Buffer
	deadline := time.After(d)
	for {
		select {
		case <-deadline:
			return false
		case m, ok := <-c.Events():
			if !ok {
				return false
			}
			if m.Session == session && m.Type == "output" {
				b, _ := client.OutputBytes(m)
				acc.Write(b)
				if bytes.Contains(acc.Bytes(), []byte(marker)) {
					return true
				}
			}
		}
	}
}

func main() {
	sock := os.Getenv("PUPPTYEER_SOCK")
	if sock == "" {
		fail("PUPPTYEER_SOCK not set")
	}
	marker := fmt.Sprintf("GO-%d", time.Now().UnixNano())

	c, err := client.Dial(sock)
	if err != nil {
		fail("dial: %v", err)
	}
	defer c.Close()

	id, err := c.NewSession("cat", nil, "", nil, 80, 24)
	if err != nil {
		fail("new_session: %v", err)
	}
	if id == "" {
		fail("empty session id")
	}
	if err := c.Attach(id, 80, 24); err != nil {
		fail("attach: %v", err)
	}
	if err := c.WritePane(id, []byte(marker+"\n")); err != nil {
		fail("write_pane: %v", err)
	}
	if !waitOutput(c, id, marker, 3*time.Second) {
		fail("marker not in live output")
	}
	cap, err := c.CapturePane(id)
	if err != nil {
		fail("capture_pane: %v", err)
	}
	if !bytes.Contains(cap, []byte(marker)) {
		fail("capture missing marker")
	}
	ss, err := c.ListSessions()
	if err != nil {
		fail("list_sessions: %v", err)
	}
	if !containsID(ss, id) {
		fail("session not listed")
	}
	if err := c.Detach(id); err != nil {
		fail("detach: %v", err)
	}

	b, err := client.Dial(sock)
	if err != nil {
		fail("dial(reattach): %v", err)
	}
	defer b.Close()
	if err := b.Attach(id, 80, 24); err != nil {
		fail("reattach: %v", err)
	}
	if !waitOutput(b, id, marker, 3*time.Second) {
		fail("scrollback replay missing marker")
	}
	if err := b.Kill(id); err != nil {
		fail("kill: %v", err)
	}
	gone := false
	for i := 0; i < 100; i++ {
		ss, _ := c.ListSessions()
		if !containsID(ss, id) {
			gone = true
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !gone {
		fail("session still listed after kill")
	}

	// rendered capture + settle: a cursor-positioned layout that naive ANSI
	// stripping would collapse to "AB".
	id3, err := c.NewSession("sh", []string{"-c", "printf 'A\\033[1;10HB'; sleep 2"}, "", nil, 80, 24)
	if err != nil {
		fail("new_session(render): %v", err)
	}
	scr, err := c.CaptureScreen(id3, client.WithSettle(200), client.WithTimeout(2000))
	if err != nil {
		fail("capture_screen: %v", err)
	}
	if scr.Cols != 80 || scr.Rows != 24 {
		fail("render dims %dx%d", scr.Cols, scr.Rows)
	}
	if len(scr.Lines) != 24 {
		fail("render line count %d", len(scr.Lines))
	}
	if len(scr.Lines) == 0 || len(scr.Lines[0]) < 10 || scr.Lines[0][:10] != "A        B" {
		got := ""
		if len(scr.Lines) > 0 {
			got = scr.Lines[0]
		}
		fail("render line0 not cursor-positioned: %q", got)
	}
	if err := c.Kill(id3); err != nil {
		fail("kill(render): %v", err)
	}

	// gc: a fresh session reaped by gc(0) (reap all idle sessions).
	id2, err := c.NewSession("cat", nil, "", nil, 80, 24)
	if err != nil {
		fail("new_session(gc): %v", err)
	}
	reaped, err := c.GC(0)
	if err != nil {
		fail("gc: %v", err)
	}
	if !containsID(reaped, id2) {
		fail("gc did not report reaping the session")
	}
	gone = false
	for i := 0; i < 100; i++ {
		ss, _ := c.ListSessions()
		if !containsID(ss, id2) {
			gone = true
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !gone {
		fail("session still listed after gc")
	}

	fmt.Println("OK go")
}
