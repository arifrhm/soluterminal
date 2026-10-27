package main

import (
	"context"
	"log/slog"
	"strings"
	"sync"
)

// This module keeps the last ~1000 daemon log lines in a bounded, in-memory
// circular buffer so a future "send diagnostics" bundle can attach recent logs
// without shipping secrets. Every line is REDACTED at capture time via
// redactSensitive (the same scrub the crash-report path uses) and truncated to
// bound memory. There is no disk, no rotation, and no remote streaming — just a
// fixed-size ring.

const (
	// logRingSize is the number of lines retained in the global ring.
	logRingSize = 1000
	// logRingMaxLineLen caps a single stored line so one pathological record
	// (a giant stack trace, a base64 blob) cannot blow the memory budget.
	logRingMaxLineLen = 2000
)

// logRing is a fixed-size circular buffer of redacted log lines, safe for
// concurrent use. head points at the slot the next add will write; count tracks
// how many slots are populated (it saturates at len(buf)).
type logRing struct {
	mu    sync.Mutex
	buf   []string
	head  int
	count int
}

// globalLogRing is the process-wide ring every slog record lands in.
var globalLogRing = newLogRing(logRingSize)

// newLogRing returns an empty ring that retains the newest size lines. A size
// <= 0 is coerced to 1 so the buffer is always usable.
func newLogRing(size int) *logRing {
	if size <= 0 {
		size = 1
	}
	return &logRing{buf: make([]string, size)}
}

// add redacts the line, truncates it to logRingMaxLineLen, and ring-writes it
// under the mutex. The newest line overwrites the oldest once the ring is full.
func (r *logRing) add(line string) {
	line = redactSensitive(line)
	if len(line) > logRingMaxLineLen {
		line = line[:logRingMaxLineLen]
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.buf[r.head] = line
	r.head = (r.head + 1) % len(r.buf)
	if r.count < len(r.buf) {
		r.count++
	}
}

// tail returns the newest n lines in chronological order (oldest -> newest). If
// n exceeds the number of stored lines, all stored lines are returned. n <= 0
// returns an empty slice.
func (r *logRing) tail(n int) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if n <= 0 || r.count == 0 {
		return []string{}
	}
	if n > r.count {
		n = r.count
	}
	out := make([]string, n)
	// The newest line sits at head-1; walk back n slots to find the start, then
	// copy forward so the result is oldest -> newest.
	start := (r.head - n + len(r.buf)) % len(r.buf)
	for i := 0; i < n; i++ {
		out[i] = r.buf[(start+i)%len(r.buf)]
	}
	return out
}

// ringHandler is a slog.Handler that formats each record to a compact,
// human-readable single line, pushes it (redacted) into ring, then delegates to
// inner so the record still reaches stderr/the configured handler.
type ringHandler struct {
	inner slog.Handler
	ring  *logRing
}

// Enabled defers entirely to the wrapped handler.
func (h *ringHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.inner.Enabled(ctx, level)
}

// Handle formats the record to one line, captures it in the ring, then forwards
// to the inner handler. Capture never blocks delegation: a record always
// reaches inner even though it was also stored.
func (h *ringHandler) Handle(ctx context.Context, record slog.Record) error {
	h.ring.add(formatRecordLine(record))
	return h.inner.Handle(ctx, record)
}

// WithAttrs wraps the inner handler's WithAttrs result while preserving the same
// ring pointer so attrs added downstream still land in the same buffer.
func (h *ringHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &ringHandler{inner: h.inner.WithAttrs(attrs), ring: h.ring}
}

// WithGroup wraps the inner handler's WithGroup result, preserving the ring.
func (h *ringHandler) WithGroup(name string) slog.Handler {
	return &ringHandler{inner: h.inner.WithGroup(name), ring: h.ring}
}

// formatRecordLine renders a slog.Record to a compact single line:
//
//	2006-01-02T15:04:05.000Z07:00 LEVEL message key=value key2=value2
//
// It is intentionally simple and human-readable; the canonical, structured
// output still comes from the inner handler. Newlines in any field are flattened
// to spaces so a multi-line value cannot split one record across ring slots.
func formatRecordLine(record slog.Record) string {
	var b strings.Builder
	if !record.Time.IsZero() {
		b.WriteString(record.Time.Format("2006-01-02T15:04:05.000Z07:00"))
		b.WriteByte(' ')
	}
	b.WriteString(record.Level.String())
	b.WriteByte(' ')
	b.WriteString(flattenLine(record.Message))
	record.Attrs(func(a slog.Attr) bool {
		b.WriteByte(' ')
		b.WriteString(a.Key)
		b.WriteByte('=')
		b.WriteString(flattenLine(a.Value.String()))
		return true
	})
	return b.String()
}

// flattenLine collapses CR/LF to spaces so a value cannot span lines.
func flattenLine(s string) string {
	if !strings.ContainsAny(s, "\r\n") {
		return s
	}
	return strings.NewReplacer("\r", " ", "\n", " ").Replace(s)
}

// Compile-time assertion that ringHandler satisfies slog.Handler.
var _ slog.Handler = (*ringHandler)(nil)
