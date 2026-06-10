package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	client "github.com/PeterSR/pupptyeer/clients/go"
	"golang.org/x/term"
)

func runCtl(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: pupptyeer ctl <list|new|send|capture|attach|resize|kill|gc> [args...]")
	}
	c, err := client.Dial(socketPath())
	if err != nil {
		return fmt.Errorf("dial daemon (is it running?): %w", err)
	}
	defer c.Close()

	switch args[0] {
	case "list":
		sessions, err := c.ListSessions()
		if err != nil {
			return err
		}
		if len(sessions) == 0 {
			fmt.Println("(no sessions)")
			return nil
		}
		for _, s := range sessions {
			fmt.Printf("%s  %dx%d  attached=%d  alive=%v  %s\n",
				s.ID, s.Cols, s.Rows, s.Attached, s.Alive, s.Command)
		}
		return nil

	case "new":
		if len(args) < 2 {
			return errors.New("usage: pupptyeer ctl new <command> [args...]")
		}
		cfg, err := loadConfig()
		if err != nil {
			return err
		}
		id, err := c.NewSession(args[1], args[2:], "", nil, cfg.defaultCols, cfg.defaultRows)
		if err != nil {
			return err
		}
		fmt.Println(id)
		return nil

	case "send":
		if len(args) < 3 {
			return errors.New("usage: pupptyeer ctl send <session> <text>  (text is sent verbatim; append \\n yourself or use a trailing newline)")
		}
		return c.WritePane(args[1], []byte(args[2]))

	case "capture":
		render := false
		settleMs := 0
		rest := args[1:]
		for len(rest) > 0 && strings.HasPrefix(rest[0], "-") {
			switch {
			case rest[0] == "--render":
				render = true
				rest = rest[1:]
			case rest[0] == "--settle":
				if len(rest) < 2 {
					return errors.New("usage: pupptyeer ctl capture [--render] [--settle <ms>] <session>")
				}
				ms, err := strconv.Atoi(rest[1])
				if err != nil || ms < 0 {
					return fmt.Errorf("invalid --settle value %q", rest[1])
				}
				settleMs = ms
				rest = rest[2:]
			default:
				return fmt.Errorf("unknown flag %q for capture", rest[0])
			}
		}
		if len(rest) < 1 {
			return errors.New("usage: pupptyeer ctl capture [--render] [--settle <ms>] <session>")
		}
		var opts []client.CaptureOption
		if settleMs > 0 {
			opts = append(opts, client.WithSettle(settleMs))
		}
		if render {
			scr, err := c.CaptureScreen(rest[0], opts...)
			if err != nil {
				return err
			}
			for _, line := range scr.Lines {
				fmt.Println(strings.TrimRight(line, " "))
			}
			return nil
		}
		data, err := c.CapturePane(rest[0], opts...)
		if err != nil {
			return err
		}
		_, _ = os.Stdout.Write(data)
		return nil

	case "attach":
		readOnly := false
		rest := args[1:]
		if len(rest) > 0 && (rest[0] == "-r" || rest[0] == "--read-only") {
			readOnly = true
			rest = rest[1:]
		}
		if len(rest) < 1 {
			return errors.New("usage: pupptyeer ctl attach [-r] <session>")
		}
		if readOnly || !term.IsTerminal(int(os.Stdin.Fd())) {
			return ctlAttach(c, rest[0]) // read-only stream needs no config
		}
		cfg, err := loadConfig()
		if err != nil {
			return err
		}
		return ctlAttachInteractive(c, rest[0], cfg)

	case "resize":
		if len(args) < 4 {
			return errors.New("usage: pupptyeer ctl resize <session> <cols> <rows>")
		}
		// Bound to the PTY winsize range: the daemon narrows cols/rows to
		// uint16, so an unchecked value like 70000 would silently wrap.
		cols, err := parseDim(args[2], "cols")
		if err != nil {
			return err
		}
		rows, err := parseDim(args[3], "rows")
		if err != nil {
			return err
		}
		return c.Resize(args[1], cols, rows)

	case "kill":
		if len(args) < 2 {
			return errors.New("usage: pupptyeer ctl kill <session>")
		}
		return c.Kill(args[1])

	case "gc":
		// Flag-based (not positional) so the selection criteria are
		// self-describing and new filters/rules can be added later
		// (e.g. --detached, --command, --dry-run) without reordering args.
		fs := flag.NewFlagSet("gc", flag.ContinueOnError)
		fs.SetOutput(io.Discard) // we format our own usage on error
		const gcUsage = "usage: pupptyeer ctl gc --max-idle <duration>  (e.g. --max-idle 1h; --max-idle 0 reaps all)"
		maxIdle := fs.Duration("max-idle", -1, "reap sessions idle at least this long (e.g. 30m, 1h; 0 reaps all)")
		if err := fs.Parse(args[1:]); err != nil {
			return fmt.Errorf("%s: %w", gcUsage, err)
		}
		if *maxIdle < 0 {
			return errors.New(gcUsage)
		}
		reaped, err := c.GC(int(maxIdle.Seconds()))
		if err != nil {
			return err
		}
		if len(reaped) == 0 {
			fmt.Println("(nothing to reap)")
			return nil
		}
		now := time.Now()
		for _, s := range reaped {
			idle := "?"
			if t, err := time.Parse(time.RFC3339, s.LastActivity); err == nil {
				idle = now.Sub(t).Round(time.Second).String()
			}
			fmt.Printf("reaped %s  idle=%s  %s\n", s.ID, idle, s.Command)
		}
		fmt.Printf("reaped %d session(s)\n", len(reaped))
		return nil

	default:
		return fmt.Errorf("unknown ctl command %q", args[0])
	}
}

// parseDim parses a terminal dimension argument (cols/rows), rejecting
// non-integers and values outside the PTY winsize range so they don't
// silently wrap when the daemon narrows them to uint16.
func parseDim(s, name string) (int, error) {
	n, err := strconv.Atoi(s)
	if err != nil || n < 1 || n > 65535 {
		return 0, fmt.Errorf("%s must be an integer between 1 and 65535, got %q", name, s)
	}
	return n, nil
}

// detachMatcher scans forwarded stdin for the detach sequence. A single
// key (the default, Ctrl-\) detaches as soon as it appears. A multi-key
// sequence (tmux-style prefix + command) holds the matched prefix instead
// of forwarding it; if the next key completes the sequence it detaches,
// otherwise the held prefix is replayed ahead of the diverging input so
// the PTY still receives it. matched persists across reads, so a prefix
// at the end of one chunk waits for the next. (Naive restart on mismatch
// can miss overlapping matches in general, but is exact for sequences of
// at most two keys, which is all the config can produce.)
type detachMatcher struct {
	seq     []byte
	matched int
}

// feed forwards in to out, swallowing any in-progress detach prefix, and
// returns true once the full sequence completes (its bytes are swallowed;
// bytes before it in this call are already written to out, bytes after it
// are dropped since the attach is ending).
func (m *detachMatcher) feed(in []byte, out *bytes.Buffer) bool {
	if len(m.seq) == 0 {
		out.Write(in)
		return false
	}
	for _, b := range in {
		if b == m.seq[m.matched] {
			m.matched++
			if m.matched == len(m.seq) {
				m.matched = 0
				return true
			}
			continue // hold: part of a pending match
		}
		if m.matched > 0 {
			out.Write(m.seq[:m.matched]) // replay the held prefix
			m.matched = 0
		}
		if b == m.seq[0] {
			m.matched = 1
			continue
		}
		out.WriteByte(b)
	}
	return false
}

// ctlAttachInteractive attaches with the local terminal in raw mode:
// stdin is forwarded byte-for-byte to the session's PTY, output streams
// to stdout, and local terminal resizes propagate as resize votes. The
// detach binding (Ctrl-\ by default, like dtach) ends the attach; raw
// mode disables ISIG so it arrives as a plain byte rather than a signal.
// It is configurable via the config file: detach_key, optionally with a
// tmux-style detach_prefix (e.g. Ctrl-b then d), or "none" to disable it
// and leave SIGINT/SIGTERM as the only way out.
func ctlAttachInteractive(c *client.Client, session string, cfg config) error {
	fd := int(os.Stdin.Fd())
	cols, rows, err := term.GetSize(fd)
	if err != nil {
		cols, rows = 0, 0
	}
	if err := c.Attach(session, cols, rows); err != nil {
		return err
	}
	oldState, err := term.MakeRaw(fd)
	if err != nil {
		return fmt.Errorf("raw mode: %w", err)
	}
	defer term.Restore(fd, oldState)
	if !cfg.quiet {
		// Plain ASCII (no em dash): this prints to the terminal, including
		// Windows consoles that may not be in a UTF-8 code page.
		if label := detachLabel(cfg.detachSeq); label != "" {
			fmt.Fprintf(os.Stderr, "[pupptyeer: attached to %s - %s detaches]\r\n", session, label)
		} else {
			// No detach key configured: raw mode disables ISIG, so Ctrl-C
			// is forwarded as a byte rather than raising a signal. The only
			// way out is an external signal or the session ending.
			fmt.Fprintf(os.Stderr, "[pupptyeer: attached to %s - no detach key set; kill this process (e.g. SIGTERM) to detach]\r\n", session)
		}
	}

	// Forward stdin to the PTY until the detach sequence or stdin closes.
	// The matcher swallows a pending prefix (tmux-style) and replays it if
	// the next key isn't the command, so its bytes only reach the PTY when
	// they aren't part of a detach. WritePane copies its argument before
	// returning, so reusing buf/out across reads is safe.
	matcher := detachMatcher{seq: cfg.detachSeq}
	detached := make(chan struct{})
	go func() {
		defer close(detached)
		buf := make([]byte, 4096)
		var out bytes.Buffer
		for {
			n, err := os.Stdin.Read(buf)
			if n > 0 {
				out.Reset()
				done := matcher.feed(buf[:n], &out)
				if out.Len() > 0 {
					if werr := c.WritePane(session, out.Bytes()); werr != nil {
						return
					}
				}
				if done {
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()

	winch := notifyResize()
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	for {
		select {
		case <-detached:
			fmt.Fprint(os.Stderr, "\r\n[pupptyeer: detached]\r\n")
			return c.Detach(session)
		case <-sig:
			return c.Detach(session)
		case <-winch:
			if w, h, err := term.GetSize(fd); err == nil {
				_ = c.Resize(session, w, h)
			}
		case m, ok := <-c.Events():
			if !ok {
				return nil
			}
			if m.Session != session {
				continue
			}
			switch m.Type {
			case client.TypeOutput:
				b, _ := client.OutputBytes(m)
				_, _ = os.Stdout.Write(b)
			case client.TypeExit:
				if m.ExitCode != nil {
					fmt.Fprintf(os.Stderr, "\r\n[session exited: code %d]\r\n", *m.ExitCode)
				}
			case client.TypeSessionClosed:
				fmt.Fprint(os.Stderr, "\r\n[session closed]\r\n")
				return nil
			}
		}
	}
}

// ctlAttach streams a session's output to stdout until the session
// closes or the user interrupts. Read-only (does not forward stdin);
// used for `attach -r` and whenever stdin is not a terminal.
func ctlAttach(c *client.Client, session string) error {
	if err := c.Attach(session, 0, 0); err != nil {
		return err
	}
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	for {
		select {
		case <-sig:
			return c.Detach(session)
		case m, ok := <-c.Events():
			if !ok {
				return nil
			}
			if m.Session != session {
				continue
			}
			switch m.Type {
			case client.TypeOutput:
				b, _ := client.OutputBytes(m)
				_, _ = os.Stdout.Write(b)
			case client.TypeExit:
				if m.ExitCode != nil {
					fmt.Fprintf(os.Stderr, "\n[session exited: code %d]\n", *m.ExitCode)
				}
			case client.TypeSessionClosed:
				fmt.Fprintln(os.Stderr, "\n[session closed]")
				return nil
			}
		}
	}
}
