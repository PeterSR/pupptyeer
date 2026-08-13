package main

import (
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
