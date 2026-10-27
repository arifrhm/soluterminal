package main

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

// TestLogRingTailOrder confirms the ring keeps only the newest lines and
// returns them oldest -> newest. Adding "a".."e" to a size-3 ring should evict
// "a" and "b" and leave ["c","d","e"].
func TestLogRingTailOrder(t *testing.T) {
	t.Parallel()
	r := newLogRing(3)
	for _, s := range []string{"a", "b", "c", "d", "e"} {
		r.add(s)
	}
	got := r.tail(3)
	want := []string{"c", "d", "e"}
	if len(got) != len(want) {
		t.Fatalf("tail(3) len = %d (%v), want %d (%v)", len(got), got, len(want), want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("tail(3)[%d] = %q, want %q (full %v)", i, got[i], want[i], got)
		}
	}
}

// TestLogRingRedactsAtCapture proves the ring scrubs at add() time: a stored
// line must carry the redaction markers and must NOT contain the raw token or
// the raw username.
func TestLogRingRedactsAtCapture(t *testing.T) {
	t.Parallel()
	const secret = "sk-ant-api03-AbCdEf0123456789ZyXwVu"
	const user = "benjamin"
	raw := "loaded creds " + secret + " from /home/" + user + "/.claude/.credentials.json"

	r := newLogRing(4)
	r.add(raw)

	lines := r.tail(1)
	if len(lines) != 1 {
		t.Fatalf("tail(1) len = %d, want 1", len(lines))
	}
	stored := lines[0]

	if strings.Contains(stored, secret) {
		t.Fatalf("stored line still contains the raw secret: %q", stored)
	}
	if strings.Contains(stored, "/home/"+user) {
		t.Fatalf("stored line still contains the raw home path / username: %q", stored)
	}
	if !strings.Contains(stored, "[REDACTED]") {
		t.Fatalf("stored line missing [REDACTED] marker: %q", stored)
	}
	if !strings.Contains(stored, "~") {
		t.Fatalf("stored line missing ~ home-path marker: %q", stored)
	}
}

// TestLogRingTruncatesLongLines confirms a pathologically long line is capped so
// one record cannot blow the memory budget.
func TestLogRingTruncatesLongLines(t *testing.T) {
	t.Parallel()
	r := newLogRing(2)
	r.add(strings.Repeat("x", 5000))

	lines := r.tail(1)
	if len(lines) != 1 {
		t.Fatalf("tail(1) len = %d, want 1", len(lines))
	}
	if got := len(lines[0]); got > logRingMaxLineLen {
		t.Fatalf("stored line length = %d, want <= %d", got, logRingMaxLineLen)
	}
}

// TestLogRingTailMoreThanCount confirms tail(n) clamps to the number of stored
// lines instead of returning empty/garbage slots.
func TestLogRingTailMoreThanCount(t *testing.T) {
	t.Parallel()
	r := newLogRing(8)
	r.add("one")
	r.add("two")

	got := r.tail(100)
	if len(got) != 2 {
		t.Fatalf("tail(100) len = %d (%v), want 2", len(got), got)
	}
	if got[0] != "one" || got[1] != "two" {
		t.Fatalf("tail(100) = %v, want [one two]", got)
	}
}

// TestLogRingHandlerWritesThroughAndCaptures proves the ringHandler both
// delegates to the inner handler (the line shows up in the buffer) AND captures
// the record into the ring.
func TestLogRingHandlerWritesThroughAndCaptures(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	inner := slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})
	ring := newLogRing(16)
	logger := slog.New(&ringHandler{inner: inner, ring: ring})

	const msg = "logring handler probe"
	logger.Info(msg, "k", "v")

	// Delegation: the inner text handler wrote to the buffer.
	if out := buf.String(); !strings.Contains(out, msg) {
		t.Fatalf("inner handler did not write through; buffer = %q", out)
	}

	// Capture: the ring holds the formatted line.
	lines := ring.tail(1)
	if len(lines) != 1 {
		t.Fatalf("ring tail(1) len = %d, want 1", len(lines))
	}
	stored := lines[0]
	if !strings.Contains(stored, msg) {
		t.Fatalf("ring line missing message: %q", stored)
	}
	if !strings.Contains(stored, "k=v") {
		t.Fatalf("ring line missing attr k=v: %q", stored)
	}
	if !strings.Contains(stored, "INFO") {
		t.Fatalf("ring line missing level: %q", stored)
	}
}
