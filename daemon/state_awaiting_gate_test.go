package main

import (
	"sync/atomic"
	"testing"
	"time"
)

// promptPane builds a running pane whose buffer shows a settled Claude
// composer (❯ line + model footer), quiet for `quiet` so the age gates in
// computeState see an idle prompt.
func promptPane(t *testing.T, quiet time.Duration) *Pane {
	t.Helper()
	p := &Pane{
		ID:     "pane-awaiting-gate",
		Status: PaneRunning,
		buffer: NewRingBuffer(1 << 16),
	}
	text := "claude finished its reply here\n❯ \n~/proj [Opus 4.8] | ctx: 42%\n"
	if _, err := p.buffer.Write([]byte(text)); err != nil {
		t.Fatalf("buffer.Write: %v", err)
	}
	atomic.StoreInt64(&p.TotalStrippedChars, int64(len(text)))
	atomic.StoreInt64(&p.LastActivityMs, time.Now().Add(-quiet).UnixMilli())
	return p
}

// TestComputeStateAwaitingInputGateCloses pins the answered-gate fix: when
// the last user input landed AFTER the prompt's buffer position (input with
// no echo following it — e.g. terminal chatter before the filter existed, or
// typed-ahead bytes), acking only the prompt position left
// inputPos > ackedPos true on every poll, so the pane re-classified as
// awaiting-input forever — the infinite "Claude is waiting for you" repeat.
// One classification must close the gate.
func TestComputeStateAwaitingInputGateCloses(t *testing.T) {
	t.Parallel()
	p := promptPane(t, 4*time.Second)
	total := atomic.LoadInt64(&p.TotalStrippedChars)
	// Input position past every buffer byte: no echo followed it.
	atomic.StoreInt64(&p.LastInputStrippedPos, total)

	if got := p.computeState(); got != StateAwaitingInput {
		t.Fatalf("(a) first classification = %q, want %q", got, StateAwaitingInput)
	}
	if got := p.computeState(); got == StateAwaitingInput {
		t.Fatalf("(b) second classification still %q — the answered-gate stuck open", got)
	}

	// A NEW user turn (input past the ack) re-opens the gate exactly once.
	acked := atomic.LoadInt64(&p.LastAckedAwaitingInputPos)
	atomic.StoreInt64(&p.LastInputStrippedPos, acked+1)
	if got := p.computeState(); got != StateAwaitingInput {
		t.Fatalf("(c) new input did not re-arm: got %q, want %q", got, StateAwaitingInput)
	}
	if got := p.computeState(); got == StateAwaitingInput {
		t.Fatalf("(d) re-armed gate did not close again: still %q", got)
	}
}

// The healthy ordering (prompt painted AFTER the user's input, the normal
// echo case) must keep its exact pre-fix behavior: fire once, ack at the
// prompt position, stay closed.
func TestComputeStateAwaitingInputHealthyOrderUnchanged(t *testing.T) {
	t.Parallel()
	p := promptPane(t, 4*time.Second)
	// User typed early; everything in the buffer (including the prompt)
	// painted after those bytes.
	atomic.StoreInt64(&p.LastInputStrippedPos, 10)

	if got := p.computeState(); got != StateAwaitingInput {
		t.Fatalf("(a) first classification = %q, want %q", got, StateAwaitingInput)
	}
	acked := atomic.LoadInt64(&p.LastAckedAwaitingInputPos)
	if acked <= 10 {
		t.Fatalf("(a) acked at %d, want the prompt's buffer position (>10)", acked)
	}
	if got := p.computeState(); got == StateAwaitingInput {
		t.Fatalf("(b) second classification still %q", got)
	}
}
