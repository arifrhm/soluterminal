package main

import "sync"

// RingBuffer is a fixed-size byte ring that keeps the most recent N bytes.
// Used to replay recent PTY output to a newly-connecting WS client so the
// terminal doesn't look empty on reattach.
type RingBuffer struct {
	mu    sync.Mutex
	buf   []byte
	size  int
	start int // index of oldest byte
	n     int // number of valid bytes
	// Monotonically increasing count of bytes ever written. Used by
	// state detection to reason about "how much output has arrived
	// since event X" (e.g. since the last user keystroke) — a count
	// survives ring wraparound cleanly in a way that raw buffer
	// positions can't.
	totalWritten int64
}

func NewRingBuffer(size int) *RingBuffer {
	return &RingBuffer{size: size}
}

func (r *RingBuffer) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.totalWritten += int64(len(p))
	if len(p) == 0 || r.size <= 0 {
		return len(p), nil
	}
	// Most panes never fill their replay allowance. Grow on demand while
	// preserving the full configured retention and absolute byte offsets.
	needed := min(r.size, r.n+len(p))
	if needed > len(r.buf) {
		grown := make([]byte, min(r.size, max(4096, needed, 2*len(r.buf))))
		copy(grown, r.buf[:r.n]) // A partially allocated ring has never wrapped.
		r.buf = grown
	}

	if len(p) >= r.size {
		copy(r.buf, p[len(p)-r.size:])
		r.start = 0
		r.n = r.size
		return len(p), nil
	}

	end := (r.start + r.n) % r.size
	first := copy(r.buf[end:], p)
	copy(r.buf, p[first:])

	if r.n+len(p) > r.size {
		overflow := r.n + len(p) - r.size
		r.start = (r.start + overflow) % r.size
		r.n = r.size
	} else {
		r.n += len(p)
	}
	return len(p), nil
}

// TotalWritten is the cumulative byte count ever appended to this ring.
// Increases forever (won't reset on overflow). Callers compare two
// snapshots of this value to measure byte-distance between events.
func (r *RingBuffer) TotalWritten() int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.totalWritten
}

// Snapshot returns a copy of the current contents in chronological order.
func (r *RingBuffer) Snapshot() []byte {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]byte, r.n)
	if r.start+r.n <= r.size {
		copy(out, r.buf[r.start:r.start+r.n])
	} else {
		first := r.size - r.start
		copy(out, r.buf[r.start:])
		copy(out[first:], r.buf[:r.n-first])
	}
	return out
}

// SnapshotSince returns a copy of every byte appended after absolute
// offset `offset` (a past TotalWritten value), in chronological order.
// ok=false when the requested range is no longer fully present (ring
// wrapped past it) or the offset is from the future / a different ring
// — callers must fall back to a full replay in that case. This is the
// foundation of exact-resume: a reconnecting client reports the offset
// it had received up to, and gets precisely the bytes it missed.
func (r *RingBuffer) SnapshotSince(offset int64) ([]byte, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if offset > r.totalWritten || offset < r.totalWritten-int64(r.n) {
		return nil, false
	}
	n := int(r.totalWritten - offset)
	out := make([]byte, n)
	if n == 0 {
		return out, true
	}
	begin := (r.start + r.n - n) % r.size
	if begin+n <= r.size {
		copy(out, r.buf[begin:begin+n])
	} else {
		first := r.size - begin
		copy(out, r.buf[begin:])
		copy(out[first:], r.buf[:n-first])
	}
	return out, true
}

// ReadFrom returns up to max bytes starting at absolute offset `offset`
// (a past TotalWritten value), plus the offset the returned bytes actually
// start at. When `offset` has already been overwritten (the reader fell more
// than a full ring behind), reading resumes at the oldest retained byte and
// start > offset tells the caller how many bytes it missed. An offset from
// the future yields no data. Readers that track their own position use this
// to stream a ring without dropping anything the ring still holds.
func (r *RingBuffer) ReadFrom(offset int64, max int) (data []byte, start int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	oldest := r.totalWritten - int64(r.n)
	if offset < oldest {
		offset = oldest
	}
	if offset >= r.totalWritten || max <= 0 || r.size <= 0 {
		return nil, min(offset, r.totalWritten)
	}
	n := int(r.totalWritten - offset)
	if n > max {
		n = max
	}
	out := make([]byte, n)
	begin := (r.start + int(offset-oldest)) % r.size
	if begin+n <= r.size {
		copy(out, r.buf[begin:begin+n])
	} else {
		first := r.size - begin
		copy(out, r.buf[begin:])
		copy(out[first:], r.buf[:n-first])
	}
	return out, offset
}

// SnapshotTail returns a copy of at most the last n bytes, in chronological
// order. Hot-path callers (per-pane state detection runs on 1-2s tickers)
// only ever need a recent window — calling Snapshot there copied the full
// 8 MB ring on every tick, which across ~36 panes produced hundreds of MB/s
// of garbage and drove the daemon into a GC death-spiral (3 GB RSS, a pegged
// core). Copying just the needed tail keeps the allocation bounded.
func (r *RingBuffer) SnapshotTail(n int) []byte {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.snapshotTailLocked(n)
}

// TrySnapshotTail is the non-blocking observation seam for conditional input.
// The byte position and tail belong to the same sample. n=0 reads only position.
func (r *RingBuffer) TrySnapshotTail(n int) ([]byte, int64, bool) {
	if !r.mu.TryLock() {
		return nil, 0, false
	}
	defer r.mu.Unlock()
	return r.snapshotTailLocked(n), r.totalWritten, true
}

func (r *RingBuffer) snapshotTailLocked(n int) []byte {
	if n > r.n {
		n = r.n
	}
	out := make([]byte, n)
	if n == 0 {
		return out
	}
	begin := (r.start + r.n - n) % r.size
	if begin+n <= r.size {
		copy(out, r.buf[begin:begin+n])
	} else {
		first := r.size - begin
		copy(out, r.buf[begin:])
		copy(out[first:], r.buf[:n-first])
	}
	return out
}
