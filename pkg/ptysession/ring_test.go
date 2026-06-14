package ptysession

import (
	"bytes"
	"testing"
)

// reference is a trivially-correct "keep last max bytes" oracle to check the
// circular ringBuffer against.
func reference(chunks [][]byte, max int) []byte {
	var all []byte
	for _, c := range chunks {
		all = append(all, c...)
	}
	if len(all) > max {
		all = all[len(all)-max:]
	}
	return all
}

func TestRingBufferSnapshotMatchesOracle(t *testing.T) {
	const max = 64
	cases := [][][]byte{
		{[]byte("abc")},                                                // below max
		{[]byte("0123456789"), []byte("abcdef")},                       // still below max
		{bytes.Repeat([]byte("x"), 64)},                                // exactly max in one write
		{bytes.Repeat([]byte("y"), 100)},                               // single write over max
		{bytes.Repeat([]byte("a"), 40), bytes.Repeat([]byte("b"), 40)}, // crosses max via two writes
		{ // many small writes that wrap several times
			bytes.Repeat([]byte("1"), 30), bytes.Repeat([]byte("2"), 30),
			bytes.Repeat([]byte("3"), 30), bytes.Repeat([]byte("4"), 30),
			[]byte("tail"),
		},
		{bytes.Repeat([]byte("z"), 200), []byte("end")}, // big then small
	}
	for i, chunks := range cases {
		r := newRingBuffer(max)
		for _, c := range chunks {
			r.append(c)
		}
		got := r.snapshot()
		want := reference(chunks, max)
		if !bytes.Equal(got, want) {
			t.Errorf("case %d: snapshot=%q want=%q", i, got, want)
		}
		if len(got) > max {
			t.Errorf("case %d: snapshot len %d exceeds max %d", i, len(got), max)
		}
	}
}

// TestRingBufferIncrementalConsistency drives a long stream of varied-size
// writes and checks the snapshot after each one against the oracle, exercising
// the grow→full transition and repeated wrapping.
func TestRingBufferIncrementalConsistency(t *testing.T) {
	const max = 256
	r := newRingBuffer(max)
	var chunks [][]byte
	sizes := []int{1, 7, 31, 100, 255, 256, 257, 13, 64, 200, 3}
	b := byte(0)
	for round := 0; round < 4; round++ {
		for _, n := range sizes {
			c := make([]byte, n)
			for j := range c {
				c[j] = b
				b++
			}
			r.append(c)
			chunks = append(chunks, c)
			if got, want := r.snapshot(), reference(chunks, max); !bytes.Equal(got, want) {
				t.Fatalf("round %d size %d: snapshot mismatch\n got=%v\nwant=%v", round, n, got, want)
			}
		}
	}
}
