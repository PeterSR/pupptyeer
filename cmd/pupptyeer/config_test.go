package main

import (
	"bytes"
	"testing"
)

func TestParseKey(t *testing.T) {
	cases := []struct {
		in        string
		allowBare bool
		want      int
		err       bool
	}{
		{`ctrl-\`, false, 0x1c, false},
		{`Ctrl-\`, false, 0x1c, false},
		{`ctrl+\`, false, 0x1c, false},
		{`c-\`, false, 0x1c, false},
		{`^\`, false, 0x1c, false},
		{`ctrl-]`, false, 0x1d, false},
		{`ctrl-a`, false, 0x01, false},
		{`ctrl-A`, false, 0x01, false},
		{`ctrl-@`, false, 0x00, false},
		{`ctrl-?`, false, 0x7f, false},
		{`ctrl-b`, false, 0x02, false},
		{`0x1c`, false, 0x1c, false},
		{`0x03`, false, 0x03, false},
		{`none`, false, -1, false},
		{`off`, false, -1, false},
		{`disabled`, false, -1, false},
		{``, false, -1, false},
		{`d`, true, int('d'), false}, // bare allowed
		{`d`, false, 0, true},        // bare not allowed standalone
		{`1`, true, int('1'), false}, // bare digit allowed
		{`ctrl-ab`, false, 0, true},  // more than one key
		{`ctrl-1`, false, 0, true},   // not a control-able char
		{`0xZZ`, false, 0, true},     // bad hex
		{`0x1ff`, false, 0, true},    // out of byte range
	}
	for _, tc := range cases {
		got, err := parseKey(tc.in, tc.allowBare)
		if tc.err {
			if err == nil {
				t.Errorf("parseKey(%q, %v): expected error, got %#x", tc.in, tc.allowBare, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("parseKey(%q, %v): unexpected error: %v", tc.in, tc.allowBare, err)
			continue
		}
		if got != tc.want {
			t.Errorf("parseKey(%q, %v) = %#x, want %#x", tc.in, tc.allowBare, got, tc.want)
		}
	}
}

func TestResolveDetach(t *testing.T) {
	cases := []struct {
		prefix, key string
		want        []byte
		err         bool
	}{
		{"", `ctrl-\`, []byte{0x1c}, false},       // default single key
		{"", `ctrl-]`, []byte{0x1d}, false},       // custom single key
		{"ctrl-b", "d", []byte{0x02, 'd'}, false}, // tmux-style prefix + key
		{"ctrl-a", "x", []byte{0x01, 'x'}, false}, // GNU screen-style
		{"", "none", nil, false},                  // disabled
		{"ctrl-b", "none", nil, false},            // disabled even with a prefix
		{"", "d", nil, true},                      // bare key without prefix is rejected
		{"d", "x", nil, true},                     // bare prefix is rejected
	}
	for _, tc := range cases {
		got, err := resolveDetach(tc.prefix, tc.key)
		if tc.err {
			if err == nil {
				t.Errorf("resolveDetach(%q,%q): expected error, got %v", tc.prefix, tc.key, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("resolveDetach(%q,%q): unexpected error: %v", tc.prefix, tc.key, err)
			continue
		}
		if !bytes.Equal(got, tc.want) {
			t.Errorf("resolveDetach(%q,%q) = %v, want %v", tc.prefix, tc.key, got, tc.want)
		}
	}
}

func TestDetachLabel(t *testing.T) {
	cases := []struct {
		in   []byte
		want string
	}{
		{[]byte{0x1c}, `Ctrl-\`},
		{[]byte{0x1d}, `Ctrl-]`},
		{[]byte{0x01}, `Ctrl-A`},
		{[]byte{0x7f}, `Ctrl-?`},
		{[]byte{0x02, 'd'}, `Ctrl-B d`},
		{nil, ``},
	}
	for _, tc := range cases {
		if got := detachLabel(tc.in); got != tc.want {
			t.Errorf("detachLabel(%v) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestParseConfig(t *testing.T) {
	in := []byte(`# pupptyeer config
detach_prefix = 'ctrl-b'
detach_key = 'd'        # inline comments work now (real TOML)
default_cols = 120
default_rows = 40
quiet = true
`)
	cfg := defaultConfig()
	if err := parseConfig(in, &cfg); err != nil {
		t.Fatalf("parseConfig: %v", err)
	}
	if !bytes.Equal(cfg.detachSeq, []byte{0x02, 'd'}) {
		t.Errorf("detachSeq = %v, want [0x02 d]", cfg.detachSeq)
	}
	if cfg.defaultCols != 120 || cfg.defaultRows != 40 {
		t.Errorf("size = %dx%d, want 120x40", cfg.defaultCols, cfg.defaultRows)
	}
	if !cfg.quiet {
		t.Errorf("quiet = false, want true")
	}
}

func TestParseConfigLiteralBackslash(t *testing.T) {
	// A single-quoted TOML literal string carries the backslash verbatim.
	cfg := defaultConfig()
	if err := parseConfig([]byte(`detach_key = 'ctrl-\'`), &cfg); err != nil {
		t.Fatalf("parseConfig: %v", err)
	}
	if !bytes.Equal(cfg.detachSeq, []byte{0x1c}) {
		t.Errorf("detachSeq = %v, want [0x1c]", cfg.detachSeq)
	}
}

func TestParseConfigErrors(t *testing.T) {
	cases := []string{
		"detach_key",                          // not valid TOML (no value)
		"unknown_key = 1",                     // unknown key
		"default_cols = 0",                    // below range
		"default_cols = 70000",                // above uint16 range
		"default_cols = 'abc'",                // not an int
		"quiet = maybe",                       // not valid TOML (bad bool)
		`detach_key = 'ctrl-1'`,               // bad key spec
		`detach_key = 'd'`,                    // bare key without a prefix
		"detach_prefix = 'b'\ndetach_key='d'", // bare prefix
		"detach_key = 5",                      // wrong type (int, not string)
	}
	for _, in := range cases {
		cfg := defaultConfig()
		if err := parseConfig([]byte(in), &cfg); err == nil {
			t.Errorf("parseConfig(%q): expected error", in)
		}
	}
}

func TestParseConfigDefaultsUntouched(t *testing.T) {
	cfg := defaultConfig()
	// Only override one key; the rest must keep their defaults.
	if err := parseConfig([]byte("default_cols = 100\n"), &cfg); err != nil {
		t.Fatalf("parseConfig: %v", err)
	}
	if !bytes.Equal(cfg.detachSeq, []byte{0x1c}) {
		t.Errorf("detachSeq changed unexpectedly: %v", cfg.detachSeq)
	}
	if cfg.defaultRows != 24 {
		t.Errorf("defaultRows changed unexpectedly: %d", cfg.defaultRows)
	}
}

// feedAll runs a script of input chunks through a matcher and returns the
// forwarded bytes plus whether (and on which chunk) it detached.
func feedAll(seq []byte, chunks ...string) (forwarded []byte, detached bool) {
	m := detachMatcher{seq: seq}
	var out bytes.Buffer
	for _, ch := range chunks {
		if m.feed([]byte(ch), &out) {
			detached = true
			break
		}
	}
	return out.Bytes(), detached
}

func TestDetachMatcherSingleKey(t *testing.T) {
	seq := []byte{0x1c}
	// Plain typing passes through untouched, no detach.
	if got, det := feedAll(seq, "hello"); det || string(got) != "hello" {
		t.Errorf("typing: got %q detached=%v", got, det)
	}
	// Bytes before the detach key are forwarded; the key is swallowed.
	if got, det := feedAll(seq, "ab\x1ccd"); !det || string(got) != "ab" {
		t.Errorf("detach: got %q detached=%v, want \"ab\" true", got, det)
	}
}

func TestDetachMatcherPrefixSequence(t *testing.T) {
	seq := []byte{0x02, 'd'} // Ctrl-b d

	// Full sequence detaches; neither byte is forwarded.
	if got, det := feedAll(seq, "\x02d"); !det || len(got) != 0 {
		t.Errorf("prefix+d: got %q detached=%v, want empty true", got, det)
	}

	// Prefix followed by a non-command key replays the prefix, then the key.
	if got, det := feedAll(seq, "\x02x"); det || string(got) != "\x02x" {
		t.Errorf("prefix+x: got %q detached=%v, want \"\\x02x\" false", got, det)
	}

	// Prefix split across reads still detaches on the next chunk.
	if got, det := feedAll(seq, "\x02", "d"); !det || len(got) != 0 {
		t.Errorf("split prefix: got %q detached=%v, want empty true", got, det)
	}

	// A lone prefix at end of input is held (not yet forwarded).
	if got, det := feedAll(seq, "ab\x02"); det || string(got) != "ab" {
		t.Errorf("held prefix: got %q detached=%v, want \"ab\" false", got, det)
	}

	// Surrounding text is preserved around a real detach.
	if got, det := feedAll(seq, "ls\x02d"); !det || string(got) != "ls" {
		t.Errorf("text+detach: got %q detached=%v, want \"ls\" true", got, det)
	}

	// Double prefix forwards one prefix byte and holds the second.
	if got, det := feedAll(seq, "\x02\x02"); det || string(got) != "\x02" {
		t.Errorf("double prefix: got %q detached=%v, want \"\\x02\" false", got, det)
	}
}

func TestParseDim(t *testing.T) {
	cases := []struct {
		in   string
		want int
		err  bool
	}{
		{"80", 80, false},
		{"1", 1, false},
		{"65535", 65535, false},
		{"0", 0, true},     // below range
		{"-5", 0, true},    // negative
		{"65536", 0, true}, // above uint16
		{"70000", 0, true}, // would wrap to 4464 at the daemon
		{"abc", 0, true},   // not an integer
		{"", 0, true},      // empty
	}
	for _, tc := range cases {
		got, err := parseDim(tc.in, "cols")
		if tc.err {
			if err == nil {
				t.Errorf("parseDim(%q): expected error, got %d", tc.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("parseDim(%q): unexpected error: %v", tc.in, err)
		} else if got != tc.want {
			t.Errorf("parseDim(%q) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

func TestDetachMatcherDisabled(t *testing.T) {
	// Empty sequence: everything passes through, never detaches.
	if got, det := feedAll(nil, "anything\x1c\x02d"); det || string(got) != "anything\x1c\x02d" {
		t.Errorf("disabled: got %q detached=%v", got, det)
	}
}
