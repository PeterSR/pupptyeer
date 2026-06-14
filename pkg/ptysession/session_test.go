package ptysession

import (
	"context"
	"strings"
	"testing"
	"time"
)

// TestEchoAndRenderedCapture drives a `cat` session in-process: bytes written
// are echoed back, land in both the ring and the live emulator, and the
// rendered screen reflects them.
func TestEchoAndRenderedCapture(t *testing.T) {
	s, err := Start(Config{Command: "cat", Cols: 80, Rows: 24})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer s.Close()

	if err := s.Write([]byte("hello world\r\n")); err != nil {
		t.Fatalf("Write: %v", err)
	}

	scr, err := s.CaptureScreen(80*time.Millisecond, 2*time.Second)
	if err != nil {
		t.Fatalf("CaptureScreen: %v", err)
	}
	if scr.Cols != 80 || scr.Rows != 24 {
		t.Errorf("screen size = %dx%d, want 80x24", scr.Cols, scr.Rows)
	}
	if !strings.Contains(strings.Join(scr.Lines, "\n"), "hello world") {
		t.Errorf("rendered screen missing echoed text:\n%s", strings.Join(scr.Lines, "\n"))
	}

	raw, err := s.CaptureRaw(0, time.Second)
	if err != nil {
		t.Fatalf("CaptureRaw: %v", err)
	}
	if !strings.Contains(string(raw), "hello world") {
		t.Errorf("raw scrollback missing echoed text: %q", raw)
	}
}

// TestExitCode confirms Wait surfaces the child's real exit status.
func TestExitCode(t *testing.T) {
	s, err := Start(Config{Command: "sh", Args: []string{"-c", "exit 7"}})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer s.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	code, err := s.Wait(ctx)
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if code != 7 {
		t.Errorf("exit code = %d, want 7", code)
	}
	if exited, c := s.Exited(); !exited || c != 7 {
		t.Errorf("Exited() = (%v, %d), want (true, 7)", exited, c)
	}
}

// TestRawSessionHasNoRenderedCapture confirms a raw session refuses rendered
// capture but still serves raw scrollback.
func TestRawSessionHasNoRenderedCapture(t *testing.T) {
	s, err := Start(Config{Command: "cat", Raw: true})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer s.Close()
	if !s.Raw() {
		t.Error("Raw() = false, want true")
	}
	if _, err := s.CaptureScreen(0, time.Second); err == nil {
		t.Error("CaptureScreen on a raw session should error")
	}
	if _, err := s.CaptureRaw(0, time.Second); err != nil {
		t.Errorf("CaptureRaw on a raw session should work: %v", err)
	}
}
