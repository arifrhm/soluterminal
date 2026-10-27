package main

import "testing"

// nextNativeEpoch must return strictly-increasing, process-unique values above
// daemonEpoch so each native (re)spawn gets a distinct NativeEpoch the client can
// detect.
func TestNextNativeEpochUnique(t *testing.T) {
	t.Parallel()
	seen := map[uint64]bool{}
	var prev uint64
	for i := 0; i < 1000; i++ {
		e := nextNativeEpoch()
		if e <= daemonEpoch {
			t.Fatalf("epoch %d not above daemonEpoch %d", e, daemonEpoch)
		}
		if seen[e] {
			t.Fatalf("duplicate epoch %d", e)
		}
		if i > 0 && e <= prev {
			t.Fatalf("epoch not strictly increasing: %d <= %d", e, prev)
		}
		seen[e] = true
		prev = e
	}
}

// Each native spawn (ensureNative on a fresh *Pane) must stamp a DISTINCT epoch —
// this is the signal that makes the client re-seed instead of rendering blank
// after a Term↔Focus switch. And a repeat ensureNative on the SAME pane must NOT
// churn it (else every poll would look like a respawn).
func TestEnsureNativeStampsDistinctEpoch(t *testing.T) {
	t.Parallel()
	p1 := &Pane{}
	p1.ensureNative()
	p2 := &Pane{}
	p2.ensureNative()

	e1, e2 := p1.nativeEpoch.Load(), p2.nativeEpoch.Load()
	if e1 == 0 || e2 == 0 {
		t.Fatalf("ensureNative did not stamp nativeEpoch: p1=%d p2=%d", e1, e2)
	}
	if e1 == e2 {
		t.Fatalf("two spawns share epoch %d — the client can't detect the respawn", e1)
	}

	// Idempotent: ensureNative early-returns when already wired, so the epoch must
	// be unchanged on a repeat call.
	p1.ensureNative()
	if got := p1.nativeEpoch.Load(); got != e1 {
		t.Fatalf("idempotent ensureNative re-stamped epoch: %d -> %d", e1, got)
	}
}
