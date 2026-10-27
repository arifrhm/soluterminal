package main

import (
	"fmt"
	"testing"
	"time"
)

func TestUIBusCoalescingPreservesTrailingDelivery(t *testing.T) {
	bus := newUIBus()
	const interval = 50 * time.Millisecond
	bus.coalesce["claude-accounts-changed"] = interval
	ch := bus.subscribe()
	defer bus.unsubscribe(ch)

	const flood = 10
	bus.publish(uiEvent{Type: "claude-accounts-changed", Ts: time.Now().UnixMilli()})
	select {
	case e := <-ch:
		if e.Type != "claude-accounts-changed" || e.Seq != 1 {
			t.Fatalf("leading event = %#v, want accounts-changed seq 1", e)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("leading coalesced event was not delivered immediately")
	}

	// Flood the coalesced type while proving an interleaved non-coalesced type
	// still takes the unchanged immediate-delivery path every time.
	for i := 1; i < flood; i++ {
		bus.publish(uiEvent{Type: "claude-accounts-changed", Ts: time.Now().UnixMilli()})
		bus.publish(uiEvent{Type: "toast", Text: fmt.Sprintf("toast-%d", i)})
	}

	// All nine suppressed invalidations collapse into exactly one trailing
	// delivery after the window, preserving the final re-fetch signal. Accept
	// either ordering with the immediate toasts so scheduler latency cannot make
	// this timing-sensitive.
	toasts := map[string]bool{}
	trailing := 0
	deadline := time.After(5 * interval)
	for len(toasts)+trailing < flood {
		select {
		case e := <-ch:
			switch e.Type {
			case "toast":
				toasts[e.Text] = true
			case "claude-accounts-changed":
				trailing++
			default:
				t.Fatalf("unexpected flood event: %#v", e)
			}
		case <-deadline:
			t.Fatalf("flood deliveries timed out: %d toasts, %d trailing", len(toasts), trailing)
		}
	}
	if len(toasts) != flood-1 {
		t.Fatalf("non-coalesced toast deliveries = %d, want %d", len(toasts), flood-1)
	}
	if trailing != 1 {
		t.Fatalf("trailing accounts-changed deliveries = %d, want 1", trailing)
	}
	select {
	case e := <-ch:
		t.Fatalf("coalesced flood delivered more than leading+trailing: %#v", e)
	case <-time.After(interval + 20*time.Millisecond):
	}

	recent := bus.Recent()
	if len(recent) != flood+1 { // 2 delivered invalidations + 9 toasts
		t.Fatalf("Recent length = %d, want %d delivered events", len(recent), flood+1)
	}
	for i, e := range recent {
		if e.Seq != int64(i+1) {
			t.Fatalf("Recent[%d].Seq = %d, want %d (suppressed publishes consumed seq)", i, e.Seq, i+1)
		}
	}

	// Once a full quiet window has elapsed, a lone invalidation is leading-edge
	// delivered immediately rather than waiting for another timer.
	time.Sleep(interval + 10*time.Millisecond)
	bus.publish(uiEvent{Type: "claude-accounts-changed", Ts: time.Now().UnixMilli()})
	select {
	case e := <-ch:
		if e.Type != "claude-accounts-changed" || e.Seq != int64(flood+2) {
			t.Fatalf("post-quiet event = %#v, want immediate seq %d", e, flood+2)
		}
	case <-time.After(interval / 2):
		t.Fatal("single post-quiet publish was not delivered immediately")
	}
}
