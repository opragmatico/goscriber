package audio

import "sync/atomic"

// ring is a single-producer, single-consumer buffer of samples. The device
// callback writes and one goroutine reads, without locks, so the callback,
// which runs on the audio thread, never waits. When the reader falls behind
// and the buffer fills, write drops what does not fit and counts it.
type ring struct {
	buf  []float32 // its length is a power of two
	mask uint64
	// w and r count every sample ever written and read; w-r are buffered.
	w, r    atomic.Uint64
	dropped atomic.Uint64
}

// newRing returns a ring holding at least n samples.
func newRing(n int) *ring {
	size := 1
	for size < n {
		size <<= 1
	}
	return &ring{buf: make([]float32, size), mask: uint64(size - 1)}
}

// write appends p, dropping the samples that do not fit. Only the producer
// calls it.
func (q *ring) write(p []float32) {
	w, r := q.w.Load(), q.r.Load()
	free := uint64(len(q.buf)) - (w - r)
	n := uint64(len(p))
	if n > free {
		q.dropped.Add(n - free)
		n = free
	}
	start := w & q.mask
	first := min(n, uint64(len(q.buf))-start)
	copy(q.buf[start:], p[:first])
	copy(q.buf, p[first:n])
	q.w.Store(w + n)
}

// read moves up to len(dst) samples into dst and returns how many. Only the
// consumer calls it.
func (q *ring) read(dst []float32) int {
	w, r := q.w.Load(), q.r.Load()
	n := min(uint64(len(dst)), w-r)
	start := r & q.mask
	first := min(n, uint64(len(q.buf))-start)
	copy(dst, q.buf[start:start+first])
	copy(dst[first:n], q.buf)
	q.r.Store(r + n)
	return int(n)
}

// buffered returns how many samples are waiting to be read.
func (q *ring) buffered() int { return int(q.w.Load() - q.r.Load()) }

// history keeps the last samples it was given, for pre-roll.
type history struct {
	buf  []float32
	next int  // where the next sample goes
	full bool // buf has wrapped at least once
}

func newHistory(n int) *history { return &history{buf: make([]float32, n)} }

func (h *history) add(p []float32) {
	if len(h.buf) == 0 {
		return
	}
	if len(p) >= len(h.buf) {
		copy(h.buf, p[len(p)-len(h.buf):])
		h.next, h.full = 0, true
		return
	}
	n := copy(h.buf[h.next:], p)
	if n < len(p) {
		copy(h.buf, p[n:])
		h.full = true
	}
	h.next = (h.next + len(p)) % len(h.buf)
	if h.next == 0 {
		h.full = true
	}
}

// take returns the kept samples, oldest first, and forgets them.
func (h *history) take() []float32 {
	var out []float32
	if h.full {
		out = append(append(out, h.buf[h.next:]...), h.buf[:h.next]...)
	} else {
		out = append(out, h.buf[:h.next]...)
	}
	h.next, h.full = 0, false
	return out
}
