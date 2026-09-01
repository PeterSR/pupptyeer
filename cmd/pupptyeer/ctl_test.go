package main

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// TestExtractNSFlags pins the namespace-flag parsing, especially that a
// subcommand's free-form payload (a spawned command's own argv, or verbatim
// send text) is never mistaken for a namespace flag.
func TestExtractNSFlags(t *testing.T) {
	cases := []struct {
		name        string
		args        []string
		leadingOnly bool
		wantNS      string
		wantAll     bool
		wantRest    []string
		wantErr     bool
	}{
		{
			name:        "leading -n before subcommand",
			args:        []string{"-n", "appA", "new", "cat"},
			leadingOnly: true,
			wantNS:      "appA",
			wantRest:    []string{"new", "cat"},
		},
		{
			name:        "payload -n is left untouched (leadingOnly stops at subcommand)",
			args:        []string{"new", "grep", "-n", "foo", "file"},
			leadingOnly: true,
			wantNS:      "",
			wantRest:    []string{"new", "grep", "-n", "foo", "file"},
		},
		{
			name:        "send literal -n text passes through",
			args:        []string{"send", "sess", "-n"},
			leadingOnly: true,
			wantRest:    []string{"send", "sess", "-n"},
		},
		{
			name:        "leading -A then non-flag",
			args:        []string{"-A", "list"},
			leadingOnly: true,
			wantAll:     true,
			wantRest:    []string{"list"},
		},
		{
			name:        "anywhere mode picks up trailing flags (list/gc)",
			args:        []string{"--max-idle", "0", "-n", "appB"},
			leadingOnly: false,
			wantNS:      "appB",
			wantRest:    []string{"--max-idle", "0"},
		},
		{
			name:        "anywhere mode trailing --all-namespaces",
			args:        []string{"--all-namespaces"},
			leadingOnly: false,
			wantAll:     true,
			wantRest:    nil,
		},
		{
			name:        "-n without value errors",
			args:        []string{"-n"},
			leadingOnly: true,
			wantErr:     true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ns, all, rest, err := extractNSFlags(tc.args, tc.leadingOnly)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got ns=%q all=%v rest=%v", ns, all, rest)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if ns != tc.wantNS || all != tc.wantAll {
				t.Fatalf("ns=%q all=%v, want ns=%q all=%v", ns, all, tc.wantNS, tc.wantAll)
			}
			if !reflect.DeepEqual(rest, tc.wantRest) {
				t.Fatalf("rest = %#v, want %#v", rest, tc.wantRest)
			}
		})
	}
}

func TestParseStealArgs(t *testing.T) {
	cases := []struct {
		name    string
		args    []string
		want    stealOptions
		wantErr bool
	}{
		{
			name: "pid only",
			args: []string{"1234"},
			want: stealOptions{pid: 1234},
		},
		{
			name: "tty steal with custom id",
			args: []string{"-T", "--id", "lifted", "5678"},
			want: stealOptions{pid: 5678, tty: true, id: "lifted"},
		},
		{
			name:    "missing pid",
			args:    []string{"-T"},
			wantErr: true,
		},
		{
			name:    "bad pid",
			args:    []string{"abc"},
			wantErr: true,
		},
		{
			name:    "missing id value",
			args:    []string{"--id"},
			wantErr: true,
		},
		{
			name:    "extra arg",
			args:    []string{"1234", "tail"},
			wantErr: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseStealArgs(tc.args)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got %#v", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %#v, want %#v", got, tc.want)
			}
		})
	}
}

// TestParseNewArgs pins `ctl new`'s flag parsing: that flags stop at the
// command so the child's own argv is untouched, that cwd is made absolute
// against the caller (not the daemon), and the environment rules.
func TestParseNewArgs(t *testing.T) {
	fakeEnv := func() []string { return []string{"PATH=/bin", "TERM=xterm", "EMPTY=", "=bogus", "NOEQUALS"} }
	abs := func(p string) string {
		a, err := filepath.Abs(p)
		if err != nil {
			t.Fatalf("abs %q: %v", p, err)
		}
		return a
	}
	cases := []struct {
		name    string
		args    []string
		want    newOptions
		wantErr bool
	}{
		{
			name: "bare command inherits daemon env",
			args: []string{"bash"},
			want: newOptions{command: "bash", args: []string{}},
		},
		{
			name: "child flags are not consumed",
			args: []string{"--raw", "grep", "-i", "--cwd", "foo"},
			want: newOptions{raw: true, command: "grep", args: []string{"-i", "--cwd", "foo"}},
		},
		{
			name: "existing flags still parse",
			args: []string{"--raw", "--id", "x", "--get-or-create", "bash"},
			want: newOptions{raw: true, requestedID: "x", getOrCreate: true, command: "bash", args: []string{}},
		},
		{
			name: "cwd is absolute",
			args: []string{"--cwd", "/tmp", "bash"},
			want: newOptions{cwd: "/tmp", command: "bash", args: []string{}},
		},
		{
			name: "relative cwd resolves against the caller",
			args: []string{"--cwd", "sub/dir", "bash"},
			want: newOptions{cwd: abs("sub/dir"), command: "bash", args: []string{}},
		},
		{
			name: "copy-env then override",
			args: []string{"--copy-env", "--env", "TERM=xterm-256color", "bash"},
			want: newOptions{
				env:     map[string]string{"PATH": "/bin", "TERM": "xterm-256color", "EMPTY": ""},
				command: "bash", args: []string{},
			},
		},
		{
			name: "clean-env starts from nothing",
			args: []string{"--clean-env", "--env", "PATH=/usr/bin", "bash"},
			want: newOptions{env: map[string]string{"PATH": "/usr/bin"}, command: "bash", args: []string{}},
		},
		{
			name: "-i is clean-env, and resets an earlier copy",
			args: []string{"--copy-env", "-i", "--env", "A=1", "bash"},
			want: newOptions{env: map[string]string{"A": "1"}, command: "bash", args: []string{}},
		},
		{
			name: "value keeps later '=' signs",
			args: []string{"--clean-env", "--env", "K=a=b=c", "bash"},
			want: newOptions{env: map[string]string{"K": "a=b=c"}, command: "bash", args: []string{}},
		},
		{name: "no command", args: []string{"--raw"}, wantErr: true},
		{name: "no command after env flags", args: []string{"--copy-env"}, wantErr: true},
		{name: "dangling --cwd", args: []string{"--cwd"}, wantErr: true},
		{name: "dangling --env", args: []string{"--env"}, wantErr: true},
		{name: "env without a base", args: []string{"--env", "A=1", "bash"}, wantErr: true},
		{name: "env-file without a base", args: []string{"--env-file", "f", "bash"}, wantErr: true},
		{name: "clean-env with no variables", args: []string{"--clean-env", "bash"}, wantErr: true},
		{name: "env without '='", args: []string{"--copy-env", "--env", "A", "bash"}, wantErr: true},
		{name: "env with empty name", args: []string{"--copy-env", "--env", "=1", "bash"}, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseNewArgs(tc.args, fakeEnv)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got %#v", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %#v, want %#v", got, tc.want)
			}
		})
	}
}

// TestParseNewArgsEnvFile covers --env-file: comments, blanks, verbatim values,
// and that a later --env wins over the file.
func TestParseNewArgsEnvFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "env")
	body := "# a comment\n\n  # indented comment\nPATH=/usr/bin\nQUOTED=\"keep quotes\"\nSPACED = has  spaces \nEMPTY=\nCRLF=ok\r\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := parseNewArgs([]string{"--clean-env", "--env-file", path, "--env", "PATH=/override", "bash"}, os.Environ)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := map[string]string{
		"PATH":   "/override",
		"QUOTED": "\"keep quotes\"",
		"SPACED": " has  spaces ",
		"EMPTY":  "",
		"CRLF":   "ok",
	}
	if !reflect.DeepEqual(got.env, want) {
		t.Fatalf("got %#v, want %#v", got.env, want)
	}

	bad := filepath.Join(dir, "bad")
	if err := os.WriteFile(bad, []byte("PATH=/usr/bin\nnot a pair\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := parseNewArgs([]string{"--clean-env", "--env-file", bad, "bash"}, os.Environ); err == nil {
		t.Fatal("expected an error for a line without '='")
	}
	if _, err := parseNewArgs([]string{"--clean-env", "--env-file", filepath.Join(dir, "missing"), "bash"}, os.Environ); err == nil {
		t.Fatal("expected an error for a missing file")
	}
}
