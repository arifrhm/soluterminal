package main

import (
	"testing"
	"time"
)

// The stuck-spinner fix: a native nativeInTurn flag that hangs true past a turn
// (the deny-with-reason AskUserQuestion answer path emits no turn_end) must
// settle once no tool is in flight and the agent has been silent — but a silent
// in-flight tool (a long bash run) must NEVER be mistaken for idle.
func TestNativeWorkingNowSettlesStuckFlag(t *testing.T) {
	t.Parallel()
	p := &Pane{Kind: "native"}

	if p.nativeWorkingNow() {
		t.Fatal("not-in-turn should report not working")
	}

	p.nativeInTurn.Store(true)
	p.nativeLastEventNano.Store(time.Now().UnixNano())
	if !p.nativeWorkingNow() {
		t.Fatal("recent activity should report working")
	}

	// A tool in flight + a stale clock → STILL working (long silent tool run).
	p.nativeOpenTools.Store(1)
	p.nativeLastEventNano.Store(time.Now().Add(-time.Hour).UnixNano())
	if !p.nativeWorkingNow() {
		t.Fatal("a tool in flight must keep the agent working despite silence")
	}

	// Just after a tool_result, the model sits in the event-less gap before it
	// resumes streaming — a silence shorter than the gap allowance must NOT settle.
	p.nativeOpenTools.Store(0)
	p.nativeLastWasToolResult.Store(true)
	p.nativeLastEventNano.Store(time.Now().Add(-(nativeSettleTimeout + time.Second)).UnixNano())
	if !p.nativeWorkingNow() {
		t.Fatal("the post-tool_result gap must keep working past the FAST settle timeout")
	}
	// …but a silence past the gap allowance (model never resumed) does settle.
	p.nativeLastEventNano.Store(time.Now().Add(-2 * nativeGapAllowance).UnixNano())
	if p.nativeWorkingNow() {
		t.Fatal("silence past the gap allowance should settle even after a tool_result")
	}

	// The stuck-flag safety net: a flag that hangs true (no turn_end) with the last
	// event NOT a tool_result settles once silent past the base timeout — and clears
	// the flag. (Normal turns settle instantly on turn_end; this is just the net.)
	p.nativeInTurn.Store(true)
	p.nativeLastWasToolResult.Store(false)
	p.nativeLastEventNano.Store(time.Now().Add(-2 * nativeSettleTimeout).UnixNano())
	if p.nativeWorkingNow() {
		t.Fatal("a stuck flag silent past the base settle timeout should settle")
	}
	if p.nativeInTurn.Load() {
		t.Fatal("the stuck nativeInTurn flag should have been cleared on settle")
	}
}

// stop_reason=end_turn is the positive done-signal that settles nativeInTurn
// deterministically (vs the idle-timeout net). Mid-turn tool_use must NOT settle.
func TestNativeEndTurnLine(t *testing.T) {
	t.Parallel()
	md := func(stop string) string {
		return `{"type":"stream_event","event":{"type":"message_delta","delta":{"stop_reason":"` + stop + `"}}}`
	}
	cases := []struct {
		name string
		line string
		want bool
	}{
		{"end_turn", md("end_turn"), true},
		{"stop_sequence", md("stop_sequence"), true},
		{"tool_use is mid-turn, NOT done", md("tool_use"), false},
		{"no stop_reason", `{"type":"stream_event","event":{"type":"message_delta","delta":{}}}`, false},
		{"wrong event type", `{"type":"stream_event","event":{"type":"content_block_delta","delta":{"stop_reason":"end_turn"}}}`, false},
		{"result line", `{"type":"result","subtype":"success"}`, false},
		{"malformed", `not json`, false},
	}
	for _, c := range cases {
		if got := nativeEndTurnLine([]byte(c.line)); got != c.want {
			t.Errorf("%s: nativeEndTurnLine = %v, want %v", c.name, got, c.want)
		}
	}
}

// The mid-turn spinner-drop fix: a live line that maps to NO NativeEvent (a ping /
// heartbeat / block boundary) must still reset the idle clock, because such lines
// are the only activity during a long time-to-first-token gap — and without the
// reset the backstop false-fires and drops the spinner mid-turn. Replay seeds must
// NOT reset it (a reattach would read "working" forever).
func TestNativeRawLineResetsIdleClock(t *testing.T) {
	t.Parallel()
	ping := []byte(`{"type":"stream_event","event":{"type":"ping"}}`)

	p := newTestNativePane()
	p.nativeInTurn.Store(true)
	// Stale clock — older than the backstop, so absent a reset the turn would settle.
	stale := time.Now().Add(-2 * nativeGapAllowance).UnixNano()
	p.nativeLastEventNano.Store(stale)

	p.processNativeLine(ping, false, false)
	if got := p.nativeLastEventNano.Load(); got <= stale {
		t.Fatalf("a live non-event line must reset the idle clock; nano unchanged (%d)", got)
	}
	if !p.nativeWorkingNow() {
		t.Fatal("pane should still read as working after a live line refreshed the clock")
	}

	// A replay (transcript-seed) line must leave the clock untouched.
	p.nativeLastEventNano.Store(stale)
	p.processNativeLine(ping, true, false)
	if got := p.nativeLastEventNano.Load(); got != stale {
		t.Fatalf("a replay line must NOT reset the idle clock; nano moved to %d", got)
	}
}
