package main

import (
	"context"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestNativeStreamEmitsEpochFrame pins the wire contract behind the
// "Focus loses the latest messages after a daemon auto-update" fix: the native
// SSE handler must announce the pane's nativeEpoch as a NAMED `epoch` frame
// right after `: connected` and BEFORE the ring snapshot. A reconnecting client
// (the browser's EventSource silently re-attaching to the swapped daemon) uses
// it to reset its fold cache before the fresh seq-1 ring folds — otherwise the
// swap's low-seq tail is deduped against the stale cache and goes missing.
func TestNativeStreamEmitsEpochFrame(t *testing.T) {
	t.Parallel()
	p := newTestNativePane()
	p.nativeEpoch.Store(424242)
	// A ring event so we can assert the epoch frame precedes the snapshot `data:`.
	p.emitNativeEvent(NativeEvent{Type: EvText, Text: "hello"})

	mgr := NewManager(t.TempDir())
	mgr.panes[p.ID] = p
	a := &apiServer{mgr: mgr}

	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest("GET", "/api/panes/"+p.ID+"/native", nil).WithContext(ctx)
	req.SetPathValue("id", p.ID)
	rec := httptest.NewRecorder()

	// The handler writes connected+epoch+snapshot synchronously, THEN blocks in
	// its select loop. Cancelling the request context makes it return; the writes
	// before the loop are unconditional, so all initial frames land regardless.
	done := make(chan struct{})
	go func() {
		a.paneNativeStream(rec, req)
		close(done)
	}()
	cancel()
	<-done

	body := rec.Body.String()
	wantEpoch := fmt.Sprintf("event: epoch\ndata: {\"nativeEpoch\":%d}\n\n", 424242)
	if !strings.Contains(body, wantEpoch) {
		t.Fatalf("SSE stream missing epoch frame %q; got:\n%s", wantEpoch, body)
	}
	iConn := strings.Index(body, ": connected")
	iEpoch := strings.Index(body, "event: epoch")
	iData := strings.Index(body, "data: {\"seq\"")
	if iConn < 0 || iEpoch < 0 {
		t.Fatalf("missing connected/epoch frames; got:\n%s", body)
	}
	if !(iConn < iEpoch) {
		t.Fatalf("epoch frame must come AFTER `: connected` (conn=%d epoch=%d)", iConn, iEpoch)
	}
	if iData >= 0 && iEpoch > iData {
		t.Fatalf("epoch frame must come BEFORE the ring snapshot data frame (epoch=%d data=%d)", iEpoch, iData)
	}
}

// TestNativeStreamEpochFallsBackToDaemonEpoch: a pane not yet ensureNative'd has
// nativeEpoch==0; the frame must fall back to the per-process daemonEpoch (never
// announce 0, which the client treats as "no epoch" and would ignore). Mirrors
// the DTO fallback in api.go.
func TestNativeStreamEpochFallsBackToDaemonEpoch(t *testing.T) {
	t.Parallel()
	p := newTestNativePane() // nativeEpoch left at 0
	mgr := NewManager(t.TempDir())
	mgr.panes[p.ID] = p
	a := &apiServer{mgr: mgr}

	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest("GET", "/api/panes/"+p.ID+"/native", nil).WithContext(ctx)
	req.SetPathValue("id", p.ID)
	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		a.paneNativeStream(rec, req)
		close(done)
	}()
	cancel()
	<-done

	want := fmt.Sprintf("data: {\"nativeEpoch\":%d}", daemonEpoch)
	if !strings.Contains(rec.Body.String(), want) {
		t.Fatalf("epoch frame should fall back to daemonEpoch %d; got:\n%s", daemonEpoch, rec.Body.String())
	}
}

// TestHistoryBackfillSeedsAfterFullRollover is the regression for the
// ring-rollover gap: when the native ring has rotated ENTIRELY past the client's
// `since`, NativeSince(since) returns an empty slice. The old truncation test
// (`events[0].Seq > since+1`) therefore reported truncated=false and a `since>0`
// reconnect got NOTHING — silently losing every event between `since` and now.
//
// historyBackfill now derives truncation from NativeRingFloor (the lowest Seq the
// ring can still serve) instead of events[0], so the empty-ring case is detected
// and the response is flagged truncated (which is what makes the caller run the
// transcript seed and the client rebuild its cache).
func TestHistoryBackfillSeedsAfterFullRollover(t *testing.T) {
	t.Parallel()
	p := newTestNativePane()

	// Push enough events to roll the ring well past a low `since`. After this the
	// ring holds only the most-recent nativeRingMaxEvents events; everything at or
	// below ~500 has aged out.
	total := nativeRingMaxEvents + 1000
	for i := 0; i < total; i++ {
		p.emitNativeEvent(NativeEvent{Type: EvText, Text: "x"})
	}

	// A client that last saw seq=100 reconnects. Those events are long gone.
	const since = 100

	// Sanity: the ring genuinely cannot serve `since` — NativeSince returns the
	// recent tail (all Seq > since, but starting far above since+1), and the ring
	// floor is well above since+1.
	tail := p.NativeSince(since)
	if len(tail) == 0 {
		t.Fatal("precondition: NativeSince should still return the recent tail")
	}
	if floor := p.NativeRingFloor(); floor <= since+1 {
		t.Fatalf("precondition: ring floor=%d should be above since+1=%d (ring must have rolled past since)", floor, since)
	}

	_, truncated := historyBackfill(p, since)
	if !truncated {
		t.Fatal("historyBackfill must report truncated=true once the ring rolls past `since`")
	}
}

// TestHistoryBackfillFullEmptyRollover proves the harder case the old heuristic
// missed entirely: the ring has rolled COMPLETELY past `since` AND is now empty
// of any event at/above `since` (here we drain the ring to empty by shrinking it
// after over-filling). Even with NativeSince returning nothing usable below the
// floor, truncated must be true so the client rebuilds rather than silently
// dropping the gap.
func TestHistoryBackfillFullEmptyRollover(t *testing.T) {
	t.Parallel()
	p := newTestNativePane()

	// Emit a batch, then forcibly empty the ring while leaving nativeSeq high —
	// the exact shape after a full rollover where the floor accessor must fall
	// back to nativeSeq+1.
	for i := 0; i < 50; i++ {
		p.emitNativeEvent(NativeEvent{Type: EvText, Text: "x"})
	}
	p.nativeMu.Lock()
	highSeq := p.nativeSeq
	p.nativeRing = nil
	p.nativeRingBytes = 0
	p.nativeMu.Unlock()

	if floor := p.NativeRingFloor(); floor != highSeq+1 {
		t.Fatalf("empty-ring floor = %d, want nativeSeq+1 = %d", floor, highSeq+1)
	}

	// since well below the high-water seq: the gap can't be healed from the ring.
	since := highSeq / 2
	if since == 0 {
		since = 1
	}
	events, truncated := historyBackfill(p, since)
	if !truncated {
		t.Fatalf("empty ring rolled past since=%d must be truncated (floor=%d)", since, highSeq+1)
	}
	// And the ring portion is genuinely empty (nothing left to serve) — proving
	// the old events[0] heuristic would have reported not-truncated here.
	if len(events) != 0 {
		// events may be non-empty only if a transcript seed ran; this test pane has
		// no ClaudeSessionID, so no seed — events must be empty.
		t.Fatalf("expected empty ring portion (no session id => no seed), got %d events", len(events))
	}
}

// TestHistoryBackfillNotTruncatedWhenSinceWithinRing confirms the common case
// (a brief disconnect whose `since` is still inside the ring) is NOT flagged
// truncated — so the client folds incrementally instead of needlessly rebuilding.
func TestHistoryBackfillNotTruncatedWhenSinceWithinRing(t *testing.T) {
	t.Parallel()
	p := newTestNativePane()
	for i := 0; i < 10; i++ {
		p.emitNativeEvent(NativeEvent{Type: EvText, Text: "x"})
	}
	// since=5 is still in the ring (floor is 1 here, nothing evicted).
	events, truncated := historyBackfill(p, 5)
	if truncated {
		t.Fatal("since within the ring must NOT be truncated")
	}
	if len(events) != 5 {
		t.Fatalf("NativeSince(5) over 10 events = %d, want 5", len(events))
	}
}

// TestNativeSessionInitPersistsSessionID verifies Task 1: a live system/init line
// stamps the pane's ClaudeSessionID (so switch-to-terminal/--resume, restart
// reattach, and the transcript seed all work) AND fires the persist hook exactly
// once, idempotently on a repeated init.
func TestNativeSessionInitPersistsSessionID(t *testing.T) {
	t.Parallel()
	p := newTestNativePane()
	persisted := 0
	p.onPromptsCommit = func(*Pane) { persisted++ }

	// Live init line -> session id captured + persisted once.
	p.consumeNativeChunk(nil, []byte(fxNativeInit+"\n"), false)
	if got := p.Snapshot().ClaudeSessionID; got != "sess-1" {
		t.Fatalf("ClaudeSessionID = %q, want sess-1", got)
	}
	if persisted != 1 {
		t.Fatalf("persist hook fired %d times, want 1", persisted)
	}

	// A repeated identical init must NOT re-persist (SetClaudeSessionID returns
	// false when unchanged).
	p.consumeNativeChunk(nil, []byte(fxNativeInit+"\n"), false)
	if persisted != 1 {
		t.Fatalf("redundant init re-persisted (%d), want still 1", persisted)
	}

	// A REPLAYED init (FromReplay=true) carrying a DIFFERENT id must never
	// overwrite the live id nor persist — seeding the transcript shouldn't
	// masquerade as a live session. Use a distinct id so the guard is actually
	// exercised (an identical id would no-op in SetClaudeSessionID regardless).
	const replayInit = `{"type":"system","subtype":"init","cwd":"/tmp/x","session_id":"replay-old","tools":["Bash"],"model":"m","permissionMode":"default","slash_commands":[]}`
	p.processNativeLine([]byte(replayInit), true, false)
	if persisted != 1 {
		t.Fatalf("replayed init persisted (%d), want still 1", persisted)
	}
	if got := p.Snapshot().ClaudeSessionID; got != "sess-1" {
		t.Fatalf("replayed init overwrote live session id: %q, want sess-1", got)
	}
}
