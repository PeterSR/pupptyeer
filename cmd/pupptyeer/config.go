package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"
)

// config holds optional CLI-side preferences. It only affects `ctl`
// (interactive attach + new-session defaults); the daemon and wire
// protocol are unaffected, so it is not part of the parity matrix.
type config struct {
	detachSeq   []byte // key sequence that detaches; empty disables it
	defaultCols int    // size for `ctl new` when no explicit size is given
	defaultRows int
	quiet       bool // suppress the "[pupptyeer: attached ...]" banner
}

func defaultConfig() config {
	return config{detachSeq: []byte{0x1c}, defaultCols: 80, defaultRows: 24}
}

// fileConfig mirrors the on-disk TOML. Every field is a pointer so an
// unset key is distinguishable from an explicit zero value and keeps its
// default; string keys carry the human-friendly key specs resolved by
// parseKey (e.g. "ctrl-\", "ctrl-b", "d").
type fileConfig struct {
	DetachKey    *string `toml:"detach_key"`
	DetachPrefix *string `toml:"detach_prefix"`
	DefaultCols  *int    `toml:"default_cols"`
	DefaultRows  *int    `toml:"default_rows"`
	Quiet        *bool   `toml:"quiet"`
}

// configPath resolves the optional config file location and reports
// whether it was set explicitly via $PUPPTYEER_CONFIG. Resolution order:
//  1. $PUPPTYEER_CONFIG (explicit override)
//  2. $XDG_CONFIG_HOME/pupptyeer/config.toml, honoured on every platform
//     (os.UserConfigDir only consults XDG on Unix, so we check it here to
//     generalize the XDG convention to macOS and Windows too)
//  3. the OS-native user config dir (~/.config on Linux, Application
//     Support on macOS, %AppData% on Windows)
//
// Returns "" if no location can be determined.
func configPath() (path string, explicit bool) {
	if p := os.Getenv("PUPPTYEER_CONFIG"); p != "" {
		return p, true
	}
	if d := os.Getenv("XDG_CONFIG_HOME"); d != "" {
		return filepath.Join(d, "pupptyeer", "config.toml"), false
	}
	dir, err := os.UserConfigDir()
	if err != nil || dir == "" {
		return "", false
	}
	return filepath.Join(dir, "pupptyeer", "config.toml"), false
}

// loadConfig returns the defaults overlaid with any values from the
// config file. An absent file at the default location is not an error
// (config is optional); a malformed file is, as is a file explicitly
// named via $PUPPTYEER_CONFIG that cannot be read (so a typo'd path
// surfaces instead of being silently ignored).
func loadConfig() (config, error) {
	cfg := defaultConfig()
	path, explicit := configPath()
	if path == "" {
		return cfg, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) && !explicit {
			return cfg, nil
		}
		return cfg, fmt.Errorf("read config %s: %w", path, err)
	}
	if err := parseConfig(data, &cfg); err != nil {
		return cfg, fmt.Errorf("%s: %w", path, err)
	}
	return cfg, nil
}

// parseConfig decodes the TOML config and overlays it onto cfg. Unknown
// keys are rejected so typos surface rather than being silently ignored.
// String values follow TOML quoting; a backslash key like the default
// detach binding is written as a literal string: detach_key = 'ctrl-\'.
func parseConfig(data []byte, cfg *config) error {
	var fc fileConfig
	md, err := toml.Decode(string(data), &fc)
	if err != nil {
		return err
	}
	if undecoded := md.Undecoded(); len(undecoded) > 0 {
		keys := make([]string, len(undecoded))
		for i, k := range undecoded {
			keys[i] = k.String()
		}
		return fmt.Errorf("unknown key(s): %s", strings.Join(keys, ", "))
	}

	if fc.DefaultCols != nil {
		if *fc.DefaultCols < 1 || *fc.DefaultCols > 65535 {
			return fmt.Errorf("default_cols must be between 1 and 65535")
		}
		cfg.defaultCols = *fc.DefaultCols
	}
	if fc.DefaultRows != nil {
		if *fc.DefaultRows < 1 || *fc.DefaultRows > 65535 {
			return fmt.Errorf("default_rows must be between 1 and 65535")
		}
		cfg.defaultRows = *fc.DefaultRows
	}
	if fc.Quiet != nil {
		cfg.quiet = *fc.Quiet
	}

	// Resolve the detach binding from prefix + key together: without a
	// prefix detach_key must be a control key; with one it may be any
	// single key.
	prefixRaw := ""
	if fc.DetachPrefix != nil {
		prefixRaw = *fc.DetachPrefix
	}
	keyRaw := `ctrl-\`
	if fc.DetachKey != nil {
		keyRaw = *fc.DetachKey
	}
	seq, err := resolveDetach(prefixRaw, keyRaw)
	if err != nil {
		return err
	}
	cfg.detachSeq = seq
	return nil
}

// resolveDetach turns the prefix + key specs into the byte sequence that
// detaches. A nil sequence means detach is disabled.
func resolveDetach(prefixRaw, keyRaw string) ([]byte, error) {
	prefix, err := parseKey(prefixRaw, false) // prefix must be a control key
	if err != nil {
		return nil, fmt.Errorf("detach_prefix: %w", err)
	}
	// A prefix guards the command key, so after one a bare letter like "d"
	// is safe; standalone it must be a control key (allowBare = false).
	key, err := parseKey(keyRaw, prefix >= 0)
	if err != nil {
		return nil, fmt.Errorf("detach_key: %w", err)
	}
	if key < 0 {
		return nil, nil // detach disabled
	}
	if prefix >= 0 {
		return []byte{byte(prefix), byte(key)}, nil
	}
	return []byte{byte(key)}, nil
}

// parseKey turns a human-friendly key spec into the byte the terminal
// delivers in raw mode, or -1 for an empty/"none" spec. Accepted forms
// (case-insensitive modifier): "ctrl-\", "ctrl+]", "c-x", "^\", a raw
// byte like "0x1c", and (only when allowBare) a single literal character
// like "d". "none"/"off"/"disabled" return -1.
func parseKey(s string, allowBare bool) (int, error) {
	t := strings.TrimSpace(s)
	switch strings.ToLower(t) {
	case "", "none", "off", "disabled":
		return -1, nil
	}
	if len(t) > 2 && t[0] == '0' && (t[1] == 'x' || t[1] == 'X') {
		v, err := strconv.ParseUint(t[2:], 16, 8)
		if err != nil {
			return 0, fmt.Errorf("invalid hex byte %q", s)
		}
		return int(v), nil
	}
	lower := strings.ToLower(t)
	var rest string
	switch {
	case strings.HasPrefix(lower, "ctrl-"):
		rest = t[len("ctrl-"):]
	case strings.HasPrefix(lower, "ctrl+"):
		rest = t[len("ctrl+"):]
	case strings.HasPrefix(lower, "c-"):
		rest = t[len("c-"):]
	case strings.HasPrefix(t, "^"):
		rest = t[1:]
	default:
		if allowBare && len(t) == 1 {
			return int(t[0]), nil
		}
		return 0, fmt.Errorf(`must be a control key like "ctrl-\" or "^]", a hex byte like 0x1c, or "none"; got %q`, s)
	}
	if len(rest) != 1 {
		return 0, fmt.Errorf("expected a single key after the modifier; got %q", s)
	}
	return controlByte(rest[0])
}

func controlByte(ch byte) (int, error) {
	c := ch
	if c >= 'a' && c <= 'z' {
		c -= 'a' - 'A'
	}
	if c == '?' {
		return 0x7f, nil // Ctrl-? is DEL by convention
	}
	if c >= '@' && c <= '_' {
		return int(c & 0x1f), nil
	}
	return 0, fmt.Errorf("%q is not a valid control key", string(ch))
}

// keyLabel renders one byte for the attach banner: control bytes as
// "Ctrl-X", printable bytes as themselves, else a hex escape.
func keyLabel(b byte) string {
	switch {
	case b == 0x7f:
		return "Ctrl-?"
	case b < 0x20:
		return "Ctrl-" + string(rune(b|0x40))
	case b < 0x7f:
		return string(rune(b))
	default:
		return fmt.Sprintf("0x%02x", b)
	}
}

// detachLabel renders a detach sequence, e.g. {0x1c} -> "Ctrl-\" and
// {0x02,'d'} -> "Ctrl-B d". Empty sequence (disabled) renders as "".
func detachLabel(seq []byte) string {
	if len(seq) == 0 {
		return ""
	}
	parts := make([]string, len(seq))
	for i, b := range seq {
		parts[i] = keyLabel(b)
	}
	return strings.Join(parts, " ")
}
