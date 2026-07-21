package ptysession

import (
	"bytes"
	"testing"
)

// runChunks feeds in through a fresh scorcRewriter split into pieces of
// chunkSize bytes (chunkSize<=0 means "the whole input in one call") and
// returns the concatenated output. Each returned slice is copied before
// accumulating, since translate reuses its output buffer across calls.
func runChunks(t *testing.T, in string, chunkSize int) []byte {
	t.Helper()
	r := &scorcRewriter{}
	b := []byte(in)
	if chunkSize <= 0 {
		chunkSize = len(b)
		if chunkSize == 0 {
			chunkSize = 1
		}
	}
	var got []byte
	for i := 0; i < len(b); i += chunkSize {
		end := i + chunkSize
		if end > len(b) {
			end = len(b)
		}
		out := r.translate(b[i:end])
		got = append(got, append([]byte(nil), out...)...)
	}
	return got
}

// TestScorcRewriterTranslate feeds each case through the rewriter at chunk
// sizes 1, 2, 3, and "whole buffer", confirming ESC[u alone becomes ESC8 and
// everything else - including Kitty keyboard-protocol sequences that also
// end in 'u' - survives byte-for-byte, no matter how the bytes are chunked.
func TestScorcRewriterTranslate(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "SCO save then restore: ESC[u becomes ESC8",
			in:   "\x1b[sAAAA\x1b[uBB",
			want: "\x1b[sAAAA\x1b8BB",
		},
		{
			name: "DEC save/restore already correct, left alone",
			in:   "\x1b7AAAA\x1b8BB",
			want: "\x1b7AAAA\x1b8BB",
		},
		{
			name: "Kitty keyboard protocol push, not touched",
			in:   "AAAA\x1b[>1uBB",
			want: "AAAA\x1b[>1uBB",
		},
		{
			name: "Kitty keyboard protocol pop, not touched",
			in:   "AAAA\x1b[<1uBB",
			want: "AAAA\x1b[<1uBB",
		},
		{
			name: "Kitty keyboard protocol set flags, not touched",
			in:   "AAAA\x1b[=5;1uBB",
			want: "AAAA\x1b[=5;1uBB",
		},
		{
			name: "only the SCORC among mixed sequences is rewritten",
			in:   "\x1b[sAA\x1b[31mAA\x1b[uBB",
			want: "\x1b[sAA\x1b[31mAA\x1b8BB",
		},
		{
			name: "plain text with no escapes at all",
			in:   "the quick brown fox jumps over the lazy dog",
			want: "the quick brown fox jumps over the lazy dog",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, size := range []int{1, 2, 3, 0} { // 0 = whole buffer in one call
				got := runChunks(t, tc.in, size)
				if !bytes.Equal(got, []byte(tc.want)) {
					t.Errorf("chunkSize=%d: got %q, want %q", size, got, tc.want)
				}
			}
		})
	}
}

// TestScorcRewriterHoldsSplitEscape confirms an ESC or ESC[ left dangling
// right at a chunk boundary is held back rather than emitted early or lost,
// and completes into the right rewrite once the rest of the sequence
// arrives in the next chunk.
func TestScorcRewriterHoldsSplitEscape(t *testing.T) {
	cases := []struct {
		name   string
		chunks []string
		want   string
	}{
		{
			name:   "chunk ends exactly after ESC",
			chunks: []string{"\x1b[sAAAA\x1b", "[uBB"},
			want:   "\x1b[sAAAA\x1b8BB",
		},
		{
			name:   "chunk ends exactly after ESC[",
			chunks: []string{"\x1b[sAAAA\x1b[", "uBB"},
			want:   "\x1b[sAAAA\x1b8BB",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := &scorcRewriter{}
			var got []byte
			for _, c := range tc.chunks {
				out := r.translate([]byte(c))
				got = append(got, append([]byte(nil), out...)...)
			}
			if string(got) != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}
