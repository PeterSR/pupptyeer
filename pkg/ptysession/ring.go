package ptysession

// ringBuffer keeps the most recent <=max bytes of PTY output. It grows
// lazily by append (cheap memory for low-output sessions) until it reaches
// max, then switches to a fixed circular buffer so appends are O(len(p))
// instead of recopying the whole window on every chunk - the difference
// between ~15 and hundreds of MiB/s of sustained PTY output.
type ringBuffer struct {
	buf  []byte // grows until len==max, then fixed-size and used circularly
	max  int
	pos  int  // circular write index; meaningful only once full
	full bool // true once max bytes have been written (buf is the full window)
}

func newRingBuffer(max int) *ringBuffer { return &ringBuffer{max: max} }

func (r *ringBuffer) append(p []byte) {
	if r.full {
		r.appendCircular(p)
		return
	}
	// Growing phase: amortised-O(1) append. On reaching max, compact to the
	// most recent max bytes once and switch to circular mode (oldest at index
	// 0, so the next write wraps from 0).
	r.buf = append(r.buf, p...)
	if len(r.buf) >= r.max {
		if len(r.buf) > r.max {
			copy(r.buf, r.buf[len(r.buf)-r.max:])
			r.buf = r.buf[:r.max]
		}
		r.full = true
		r.pos = 0
	}
}

// appendCircular writes p into the full fixed-size buffer, wrapping as needed.
func (r *ringBuffer) appendCircular(p []byte) {
	if len(p) >= r.max {
		copy(r.buf, p[len(p)-r.max:]) // only the last max bytes survive
		r.pos = 0
		return
	}
	n := copy(r.buf[r.pos:], p)
	if n < len(p) {
		copy(r.buf, p[n:]) // wrap the remainder to the front
	}
	r.pos = (r.pos + len(p)) % r.max
}

func (r *ringBuffer) snapshot() []byte {
	if !r.full {
		out := make([]byte, len(r.buf))
		copy(out, r.buf)
		return out
	}
	// Oldest byte is at pos; reassemble in chronological order.
	out := make([]byte, r.max)
	n := copy(out, r.buf[r.pos:])
	copy(out[n:], r.buf[:r.pos])
	return out
}
