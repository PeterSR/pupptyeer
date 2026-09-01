package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	client "github.com/PeterSR/pupptyeer/clients/go"
	"golang.org/x/term"
)

type stealOptions struct {
	pid int
	tty bool
	id  string
}

func runCtl(args []string) error {
	if len(args) == 0 {
		return errors.New(ctlUsage())
	}
	// Namespace selection: -n/--namespace <ns> scopes the connection (default
	// "default"); --all-namespaces/-A is the cross-cutting view, valid only for
	// list and gc. Consumed only from the LEADING run (before the subcommand) so
	// a subcommand's free-form payload is never touched - e.g. `ctl new grep -n
	// foo file` must pass `-n foo` to grep, and `ctl send <id> -n` must type the
	// literal text. list and gc have no free-form positional payload, so for
	// those we also accept the flags after the subcommand (k8s muscle memory).
	ns, allNS, rest, err := extractNSFlags(args, true)
	if err != nil {
		return err
	}
	if len(rest) == 0 {
		return errors.New(ctlUsage())
	}
	cmd := rest[0]
	sub := rest[1:]
	if cmd == "list" || cmd == "gc" {
		ns2, all2, sub2, err := extractNSFlags(sub, false)
		if err != nil {
			return err
		}
		if ns2 != "" {
			ns = ns2
		}
		allNS = allNS || all2
		sub = sub2
	}
	if allNS && cmd != "list" && cmd != "gc" {
		return errors.New("--all-namespaces is only valid for list and gc")
	}
	c, err := client.Connect(client.WithSocket(socketPath()), client.WithNamespace(ns))
	if err != nil {
		return err
	}
	defer c.Close()
	args = append([]string{cmd}, sub...)

	switch args[0] {
	case "list":
		var sessions []client.SessionInfo
		if allNS {
			sessions, err = c.ListAllSessions()
		} else {
			sessions, err = c.ListSessions()
		}
		if err != nil {
			return err
		}
		if len(sessions) == 0 {
			fmt.Println("(no sessions)")
			return nil
		}
		for _, s := range sessions {
			// cwd is printed as a keyed field, and only when the session has one,
			// so the leading positional fields stay where scripts expect them.
			cwd := ""
			if s.Cwd != "" {
				cwd = "  cwd=" + s.Cwd
			}
			fmt.Printf("%s  %s  %dx%d  attached=%d  alive=%v%s  %s\n",
				s.Namespace, s.ID, s.Cols, s.Rows, s.Attached, s.Alive, cwd, s.Command)
		}
		return nil

	case "new":
		o, err := parseNewArgs(args[1:], os.Environ)
		if err != nil {
			return err
		}
		var opts []client.SessionOption
		if o.raw {
			opts = append(opts, client.WithRaw())
		}
		if o.requestedID != "" {
			opts = append(opts, client.WithSessionID(o.requestedID))
		}
		if o.getOrCreate {
			opts = append(opts, client.WithGetOrCreate())
		}
		cfg, err := loadConfig()
		if err != nil {
			return err
		}
		id, err := c.NewSession(o.command, o.args, o.cwd, o.env, cfg.defaultCols, cfg.defaultRows, opts...)
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

	case "expect":
		return ctlExpect(c, args[1:])

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
		var reaped []client.SessionInfo
		if allNS {
			reaped, err = c.GCAll(int(maxIdle.Seconds()))
		} else {
			reaped, err = c.GC(int(maxIdle.Seconds()))
		}
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
			fmt.Printf("reaped %s/%s  idle=%s  %s\n", s.Namespace, s.ID, idle, s.Command)
		}
		fmt.Printf("reaped %d session(s)\n", len(reaped))
		return nil

	case "steal":
		opts, err := parseStealArgs(args[1:])
		if err != nil {
			return err
		}
		cfg, err := loadConfig()
		if err != nil {
			return err
		}
		id, err := ctlSteal(c, opts, cfg)
		if err != nil {
			return err
		}
		fmt.Println(id)
		return nil

	default:
		return fmt.Errorf("unknown ctl command %q", args[0])
	}
}

// extractNSFlags pulls the namespace flags out of an argument list, returning
// the chosen namespace (empty => the client's default), whether --all-namespaces
// was given, and the remaining args with the flags removed. Recognised:
// "-n"/"--namespace" <ns> and "-A"/"--all-namespaces". With leadingOnly it
// consumes them only from the front and stops at the first other token, so a
// subcommand and its free-form payload (a command + its argv, or verbatim send
// text) are returned untouched. Without it, the flags may appear anywhere -
// safe only for args with no free-form positional payload (list, gc).
func extractNSFlags(args []string, leadingOnly bool) (ns string, all bool, rest []string, err error) {
	i := 0
	for i < len(args) {
		switch args[i] {
		case "-n", "--namespace":
			if i+1 >= len(args) {
				return "", false, nil, errors.New("-n/--namespace requires a namespace argument")
			}
			ns = args[i+1]
			i += 2
		case "-A", "--all-namespaces":
			all = true
			i++
		default:
			if leadingOnly {
				rest = append(rest, args[i:]...)
				return ns, all, rest, nil
			}
			rest = append(rest, args[i])
			i++
		}
	}
	return ns, all, rest, nil
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

func ctlUsage() string {
	return fmt.Sprintf("usage: pupptyeer ctl [-n <namespace>] <%s> [args...]", ctlCommandList())
}

// newOptions is the parsed form of `ctl new`'s leading flags. env stays nil
// unless one of the environment flags is given, and nil is what the daemon
// reads as "inherit my environment" - the historical behaviour of this command.
type newOptions struct {
	raw         bool
	requestedID string
	getOrCreate bool
	cwd         string
	env         map[string]string
	command     string
	args        []string
}

// newUsage is a consumer-visible contract, not just help text: a launcher that
// has to work against both an older installed pupptyeer and this one feature-
// detects the environment flags by running `ctl new` with no command and looking
// for "--copy-env" in the usage it prints (on stderr, with a non-zero exit). Keep
// the flags spelled out here rather than collapsing them into "[options]".
const newUsage = "usage: pupptyeer ctl new [--raw] [--id <id> [--get-or-create]] [--cwd <dir>] " +
	"[--copy-env|--clean-env] [--env NAME=VALUE]... [--env-file <file>]... <command> [args...]"

// parseNewArgs consumes the flags that precede the command to spawn. Parsing
// stops at the first word that isn't one of ours, so the child's own flags are
// never touched (`ctl new --cwd /tmp grep -i foo` passes -i to grep).
//
// The environment flags build one explicit environment, applied in the order
// they appear: --copy-env merges in this process's environment, --clean-env
// resets to empty, --env and --env-file set individual variables. The wire
// carries either a complete environment or none, so "the daemon's environment
// plus FOO" is not expressible; --env therefore insists on an explicit base
// rather than quietly handing the child a one-variable environment.
//
// environ is injected (os.Environ in production) so the tests don't depend on
// the environment they happen to run in.
func parseNewArgs(args []string, environ func() []string) (newOptions, error) {
	var o newOptions
	env := map[string]string{}
	envTouched := false // any environment flag seen
	baseChosen := false // --copy-env or --clean-env seen

	rest := args
loop:
	for len(rest) > 0 {
		name := rest[0]
		switch name {
		case "--id", "--cwd", "--env", "--env-file":
			if len(rest) < 2 {
				return newOptions{}, errors.New(newUsage)
			}
		}
		switch name {
		case "--raw":
			o.raw = true
			rest = rest[1:]
		case "--get-or-create":
			o.getOrCreate = true
			rest = rest[1:]
		case "--id":
			o.requestedID = rest[1]
			rest = rest[2:]
		case "--cwd":
			// Resolved here, against the caller's cwd, because the daemon runs
			// from somewhere else entirely: a relative path sent over the wire
			// would land in the wrong directory.
			abs, err := filepath.Abs(rest[1])
			if err != nil {
				return newOptions{}, fmt.Errorf("--cwd %q: %w", rest[1], err)
			}
			o.cwd = abs
			rest = rest[2:]
		case "--copy-env":
			for _, kv := range environ() {
				if k, v, ok := strings.Cut(kv, "="); ok && k != "" {
					env[k] = v
				}
			}
			envTouched, baseChosen = true, true
			rest = rest[1:]
		case "--clean-env", "-i":
			env = map[string]string{}
			envTouched, baseChosen = true, true
			rest = rest[1:]
		case "--env":
			k, v, ok := strings.Cut(rest[1], "=")
			if !ok || k == "" {
				return newOptions{}, fmt.Errorf("--env expects NAME=VALUE, got %q", rest[1])
			}
			env[k] = v
			envTouched = true
			rest = rest[2:]
		case "--env-file":
			if err := readEnvFile(rest[1], env); err != nil {
				return newOptions{}, err
			}
			envTouched = true
			rest = rest[2:]
		default:
			break loop
		}
	}
	if len(rest) < 1 {
		return newOptions{}, errors.New(newUsage)
	}
	if envTouched && !baseChosen {
		return newOptions{}, errors.New("--env/--env-file replace the child's whole environment instead of adding to the daemon's, " +
			"so they need an explicit starting point: --copy-env to start from this shell's environment, or --clean-env to start from nothing")
	}
	if envTouched && len(env) == 0 {
		return newOptions{}, errors.New("--clean-env with no --env/--env-file would give the child an empty environment, " +
			"which the wire protocol cannot express (no env means inherit); set at least one variable or drop --clean-env")
	}
	if envTouched {
		o.env = env
	}
	o.command, o.args = rest[0], rest[1:]
	return o, nil
}

// readEnvFile merges NAME=VALUE lines from path into env. Blank lines and lines
// whose first non-blank character is # are skipped, the name is trimmed, and the
// value is taken verbatim to the end of the line: no quote stripping, no variable
// expansion, so a value may contain spaces, quotes and '=' without ceremony.
func readEnvFile(path string, env map[string]string) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("--env-file: %w", err)
	}
	for i, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSuffix(line, "\r")
		if t := strings.TrimSpace(line); t == "" || strings.HasPrefix(t, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		k = strings.TrimSpace(k)
		if !ok || k == "" {
			return fmt.Errorf("%s:%d: expected NAME=VALUE, got %q", path, i+1, strings.TrimSpace(line))
		}
		env[k] = v
	}
	return nil
}

func parseStealArgs(args []string) (stealOptions, error) {
	const usage = "usage: pupptyeer ctl steal [-T] [--id <id>] <pid>"
	var opts stealOptions
	rest := args
	for len(rest) > 0 {
		if rest[0] == "-T" || rest[0] == "--tty-steal" {
			opts.tty = true
			rest = rest[1:]
			continue
		}
		if rest[0] == "--id" {
			if len(rest) < 2 {
				return stealOptions{}, errors.New(usage)
			}
			opts.id = rest[1]
			rest = rest[2:]
			continue
		}
		break
	}
	if len(rest) != 1 {
		return stealOptions{}, errors.New(usage)
	}
	pid, err := strconv.Atoi(rest[0])
	if err != nil || pid < 1 {
		return stealOptions{}, fmt.Errorf("pid must be a positive integer, got %q", rest[0])
	}
	opts.pid = pid
	return opts, nil
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
			if m.Session != session || m.Namespace != c.Namespace() {
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
			if m.Session != session || m.Namespace != c.Namespace() {
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

// expectWindow bounds the match buffer so a long-running stream can't grow it
// without limit. It is large enough that a sentinel split across output chunks
// (or a regex spanning a chunk boundary) still matches; it mirrors the
// scrollback ring size.
const expectWindow = 256 * 1024

// ansiRe strips ANSI escape sequences (CSI/SGR plus the common two-byte
// escapes) so --strip-ansi can match plain text against an animated TUI. It is
// applied per output chunk, so an escape split across two chunks may slip
// through; a sentinel printed in a single write (the intended use) is
// unaffected.
var ansiRe = regexp.MustCompile(`\x1b(?:[@-Z\\-_]|\[[0-?]*[ -/]*[@-~])`)

// ctlExpect attaches to a session and blocks until a trigger fires: a regex or
// substring appears in the output, the output goes quiet for --idle, the child
// exits (--exit), or --timeout elapses. It does not poll - it rides the daemon's
// push-based attach stream. Each fire prints one line to stdout; the exit code
// reports the outcome (0 fired, 2 timed out, 3 session closed first). With
// --follow it keeps reporting matches until the session closes or --timeout.
//
// By default matching is armed only for live output (after scrollback replay),
// so a stale marker in the ring does not cause a false fire; --include-scrollback
// also scans the replay. It is built entirely on the existing Attach/Events
// client surface, so it adds no wire verb and no parity surface.
func ctlExpect(c *client.Client, args []string) error {
	fs := flag.NewFlagSet("expect", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	const usage = "usage: pupptyeer ctl expect [--regex <re>] [--substr <s>] [--idle <dur>] [--exit] [--timeout <dur>] [--follow] [--strip-ansi] [--include-scrollback] <session>"
	reFlag := fs.String("regex", "", "fire when this regexp matches the output")
	substrFlag := fs.String("substr", "", "fire when this literal substring appears")
	idleFlag := fs.Duration("idle", 0, "fire when output is quiet this long (e.g. 3s)")
	exitFlag := fs.Bool("exit", false, "fire when the session's process exits")
	timeoutFlag := fs.Duration("timeout", 0, "overall deadline (0 = no limit)")
	followFlag := fs.Bool("follow", false, "keep matching; one line per hit until close/timeout")
	stripFlag := fs.Bool("strip-ansi", false, "strip ANSI escapes before matching")
	inclScroll := fs.Bool("include-scrollback", false, "also scan replayed scrollback (default: live only)")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("%s: %w", usage, err)
	}
	rest := fs.Args()
	if len(rest) != 1 {
		return errors.New(usage)
	}
	session := rest[0]

	var re *regexp.Regexp
	if *reFlag != "" {
		var err error
		if re, err = regexp.Compile(*reFlag); err != nil {
			return fmt.Errorf("invalid --regex: %w", err)
		}
	}
	if re == nil && *substrFlag == "" && *idleFlag <= 0 && !*exitFlag {
		return fmt.Errorf("arm at least one trigger (--regex/--substr/--idle/--exit)\n%s", usage)
	}

	if err := c.Attach(session, 0, 0); err != nil {
		return err
	}
	defer func() { _ = c.Detach(session) }()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)

	armed := *inclScroll // until scrollback_end, only match if asked to
	var buf []byte       // rolling match window (stripped text when --strip-ansi)
	var base int         // bytes dropped off the front of buf, for absolute offsets

	// Idle timer, reset on every output chunk; armed only once matching is live.
	var idleTimer *time.Timer
	var idleC <-chan time.Time
	armIdle := func() {
		if *idleFlag <= 0 || !armed {
			return
		}
		if idleTimer == nil {
			idleTimer = time.NewTimer(*idleFlag)
			idleC = idleTimer.C
			return
		}
		if !idleTimer.Stop() {
			select {
			case <-idleTimer.C:
			default:
			}
		}
		idleTimer.Reset(*idleFlag)
	}
	armIdle()

	var deadlineC <-chan time.Time
	if *timeoutFlag > 0 {
		deadlineC = time.NewTimer(*timeoutFlag).C
	}

	// scan reports matches in buf, advancing past each; in one-shot mode it
	// returns true after the first. It then trims buf back to expectWindow.
	scan := func() bool {
		if !armed {
			return false
		}
		for {
			idx, end, kind, pat := firstMatch(buf, re, *substrFlag)
			if idx < 0 {
				break
			}
			fmt.Printf("match %s=%q off=%d\n", kind, pat, base+idx)
			if end <= idx { // zero-width match: force progress
				end = idx + 1
			}
			base += end
			buf = buf[end:]
			if !*followFlag {
				return true
			}
		}
		if len(buf) > expectWindow {
			drop := len(buf) - expectWindow
			buf = buf[drop:]
			base += drop
		}
		return false
	}

	for {
		select {
		case <-sig:
			return &exitError{code: 130}
		case <-deadlineC:
			fmt.Println("timeout")
			return &exitError{code: 2}
		case <-idleC:
			fmt.Printf("idle %s\n", *idleFlag)
			if !*followFlag {
				return nil
			}
			armIdle() // re-arm for the next quiet window
		case m, ok := <-c.Events():
			if !ok {
				fmt.Println("closed")
				return &exitError{code: 3}
			}
			if m.Session != session || m.Namespace != c.Namespace() {
				continue
			}
			switch m.Type {
			case client.TypeScrollbackEnd:
				armed = true
				armIdle()
				if scan() {
					return nil
				}
			case client.TypeOutput:
				b, _ := client.OutputBytes(m)
				if *stripFlag {
					b = ansiRe.ReplaceAll(b, nil)
				}
				if armed {
					buf = append(buf, b...)
				}
				armIdle()
				if scan() {
					return nil
				}
			case client.TypeExit:
				if *exitFlag {
					if m.ExitCode != nil {
						fmt.Printf("exit code=%d\n", *m.ExitCode)
					} else {
						fmt.Println("exit code=?")
					}
					return nil
				}
			case client.TypeSessionClosed:
				fmt.Println("closed")
				return &exitError{code: 3}
			}
		}
	}
}

// firstMatch returns the earliest match of re or substr in buf as [start,end)
// byte offsets, a kind label, and the matched pattern; start is -1 if neither
// matches. When both match, the one starting earlier wins.
func firstMatch(buf []byte, re *regexp.Regexp, substr string) (start, end int, kind, pat string) {
	start = -1
	if re != nil {
		if loc := re.FindIndex(buf); loc != nil {
			start, end, kind, pat = loc[0], loc[1], "regex", re.String()
		}
	}
	if substr != "" {
		if i := bytes.Index(buf, []byte(substr)); i >= 0 && (start < 0 || i < start) {
			start, end, kind, pat = i, i+len(substr), "substr", substr
		}
	}
	return
}
