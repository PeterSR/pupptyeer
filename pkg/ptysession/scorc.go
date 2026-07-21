package ptysession

import "bytes"

// scorcRewriter works around a gap in the upstream terminal emulator
// (github.com/charmbracelet/x/vt): it registers a CSI handler for 's' (SCOSC
// - save cursor, or DECSLRM when mode ?69 is set) but registers none for 'u'
// (SCORC - restore cursor). ESC[s is honoured, so it lands in the same
// SaveCursor() slot as DECSC (ESC7), but ESC[u is silently dropped instead of
// restoring what was saved - upstream main still has the gap as of this
// writing, so bumping the dependency does not help. There is no way to patch
// this from outside the package either: a caller can register its own 'u'
// handler, but restoring the cursor needs Screen.RestoreCursor(), and
// *vt.Emulator exposes no accessor for the screen and no cursor setter, only
// CursorPosition(). Writing ESC8 back into the emulator from inside a
// handler would also re-enter the parser mid-dispatch, so that route is out
// too.
//
// The workaround lives entirely on the way in: rewrite the exact 3 bytes
// ESC [ u into ESC 8 (DECRC) before the bytes reach the emulator. Since
// ESC[s and ESC7 already share one SaveCursor() slot upstream, ESC8 restores
// precisely what ESC[s saved - the rewrite is semantically exact, not an
// approximation.
//
// The rewrite touches ONLY the copy of the stream fed to the live emulator
// (Session.term). The ring buffer and the OnOutput callback both keep the
// child's bytes verbatim, so raw capture, replay, and any other consumer of
// the raw stream see the real output unmodified.
//
// TODO(scorc): delete this whole file once charmbracelet/x/vt grows its own
// CSI 'u' (SCORC) handler.
type scorcRewriter struct {
	// pending holds an escape sequence left incomplete by the previous
	// chunk: "", "\x1b", or "\x1b[". A sequence can straddle a PTY read, so
	// these bytes are carried over and retried against the next chunk. This
	// costs nothing beyond what the emulator's own parser would have done
	// anyway - it would have buffered the same incomplete bytes itself.
	pending []byte
	// in and out are scratch buffers reused across calls so a steady stream
	// of translate calls does not allocate once warmed up.
	in, out []byte
}

// translate returns the bytes to feed the emulator for chunk: any ESC[u
// (CSI u, SCORC) becomes ESC8 (DECRC); everything else, including Kitty
// keyboard-protocol sequences such as ESC[>1u, ESC[<1u, or ESC[=5;1u, passes
// through byte-for-byte (the byte right after ESC[ is not 'u' there, so the
// 3-byte match never fires). chunk is never modified, and the slice returned
// is only valid until the next call - copy it if you need to keep it.
func (r *scorcRewriter) translate(chunk []byte) []byte {
	if len(r.pending) == 0 && bytes.IndexByte(chunk, 0x1b) < 0 {
		return chunk // fast path: nothing pending and nothing to rewrite
	}
	in := append(append(r.in[:0], r.pending...), chunk...)
	r.in = in
	r.pending = r.pending[:0]
	out := r.out[:0]
	for i := 0; i < len(in); {
		if in[i] != 0x1b {
			out = append(out, in[i])
			i++
			continue
		}
		if i+1 >= len(in) { // ESC cut off at the end of the chunk: hold it
			r.pending = append(r.pending, in[i])
			break
		}
		if in[i+1] != '[' {
			out = append(out, in[i], in[i+1])
			i += 2
			continue
		}
		if i+2 >= len(in) { // ESC[ cut off at the end of the chunk: hold it
			r.pending = append(r.pending, in[i], in[i+1])
			break
		}
		if in[i+2] == 'u' {
			out = append(out, 0x1b, '8') // ESC[u -> ESC8 (DECRC)
		} else {
			out = append(out, in[i], in[i+1], in[i+2])
		}
		i += 3
	}
	r.out = out
	return out
}
