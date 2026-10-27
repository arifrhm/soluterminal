package main

import (
	"sync/atomic"
	"testing"
)

// The real-time turn-state edge-publish must fire onTurnStateEdge EXACTLY once per
// nativeInTurn true↔false transition (turn start, turn end) and NEVER on every
// streamed delta — otherwise the pane-state SSE channel would spam ~60×/sec. Covers
// the emitNativeEvent edges (streaming-start site B, settle site C).
func TestTurnStateEdgeFiresOncePerTransition(t *testing.T) {
	t.Parallel()
	var edges int32
	p := &Pane{ID: "edge1", Kind: "native"}
	p.onTurnStateEdge = func(*Pane) { atomic.AddInt32(&edges, 1) }

	// First delta flips false→true → 1 edge; subsequent deltas are already-true → none.
	p.emitNativeEvent(NativeEvent{Type: EvTextDelta, Text: "a"})
	p.emitNativeEvent(NativeEvent{Type: EvTextDelta, Text: "b"})
	if got := atomic.LoadInt32(&edges); got != 1 {
		t.Fatalf("turn start: want 1 edge across 2 deltas, got %d", got)
	}

	// turn_end flips true→false → 1 edge; a second turn_end is an already-false no-op.
	p.emitNativeEvent(NativeEvent{Type: EvTurnEnd})
	p.emitNativeEvent(NativeEvent{Type: EvTurnEnd})
	if got := atomic.LoadInt32(&edges); got != 2 {
		t.Fatalf("turn end: want 2 edges total, got %d", got)
	}
}

// markNativeUserTurn (the optimistic submit edge, site D) fires once on false→true
// and not again on a mid-turn re-mark (a steer while already in-turn).
func TestTurnStateEdgeUserTurnFiresOnce(t *testing.T) {
	t.Parallel()
	var edges int32
	p := &Pane{ID: "edge2", Kind: "native"}
	p.onTurnStateEdge = func(*Pane) { atomic.AddInt32(&edges, 1) }

	p.markNativeUserTurn() // false→true → 1 edge
	p.markNativeUserTurn() // already true → no edge
	if got := atomic.LoadInt32(&edges); got != 1 {
		t.Fatalf("markNativeUserTurn: want 1 edge, got %d", got)
	}
}

// A nil onTurnStateEdge (tests / paths that don't wire it) must be a safe no-op.
func TestTurnStateEdgeNilSafe(t *testing.T) {
	t.Parallel()
	p := &Pane{ID: "edge3", Kind: "native"} // onTurnStateEdge left nil
	p.markNativeUserTurn()
	p.emitNativeEvent(NativeEvent{Type: EvTextDelta, Text: "x"})
	p.emitNativeEvent(NativeEvent{Type: EvTurnEnd})
	// reaching here without a panic is the assertion
}
