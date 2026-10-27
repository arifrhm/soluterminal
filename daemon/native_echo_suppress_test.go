package main

import "testing"

// The daemon emits a FRESH-turn prompt as user_text at INPUT time (paneNativeInput)
// so it precedes the reply in seq order — Claude's --replay-user-messages echo is
// NOT ordered before the reply on stdout, so a late echo would render the prompt
// below the answer. The matching live echo is then dropped in processNativeLine.
// These tests pin the suppression mechanism.

const fxUserEcho = `{"type":"user","message":{"content":"hi there"}}`

func TestNativeFreshTurnEchoIsSuppressed(t *testing.T) {
	t.Parallel()
	p := newTestNativePane()
	// Simulate the input path: the daemon registered this text (it emitted the
	// user_text at input time) so the CLI's later echo must be dropped.
	p.expectEchoSuppress("hi there")

	// The CLI's --replay-user-messages echo arrives LIVE → suppressed (no dup).
	p.processNativeLine([]byte(fxUserEcho), false, false)
	if got := ringTypes(p); len(got) != 0 {
		t.Fatalf("fresh-turn echo should be suppressed, ring=%v", got)
	}

	// A steer / any unregistered echo flows through and renders.
	p.processNativeLine([]byte(`{"type":"user","message":{"content":"different"}}`), false, false)
	got := ringTypes(p)
	if len(got) != 1 || got[0] != EvUserText {
		t.Fatalf("unregistered echo should render, ring=%v", got)
	}
}

func TestNativeSeedUserTextNeverSuppressed(t *testing.T) {
	t.Parallel()
	p := newTestNativePane()
	p.expectEchoSuppress("hi there")

	// A transcript SEED (fromReplay=true) user_text must ALWAYS render, even when
	// its text matches a pending suppression — the seed is history, not the echo.
	p.processNativeLine([]byte(fxUserEcho), true, false)
	got := ringTypes(p)
	if len(got) != 1 || got[0] != EvUserText {
		t.Fatalf("seed user_text must always render, ring=%v", got)
	}
	// The seed must NOT have consumed the suppression entry (still armed for the
	// real live echo).
	if !p.consumeEchoSuppress("hi there") {
		t.Fatal("seed wrongly consumed the suppress entry")
	}
}

func TestNativeEchoSuppressFIFOAndCap(t *testing.T) {
	t.Parallel()
	p := newTestNativePane()

	// Content-FIFO: two identical fresh prompts each suppress exactly one echo.
	p.expectEchoSuppress("yes")
	p.expectEchoSuppress("yes")
	if !p.consumeEchoSuppress("yes") || !p.consumeEchoSuppress("yes") {
		t.Fatal("both registered 'yes' echoes should match")
	}
	if p.consumeEchoSuppress("yes") {
		t.Fatal("a third 'yes' must NOT match (only two were registered)")
	}
	if p.consumeEchoSuppress("nope") {
		t.Fatal("an unregistered text must never suppress")
	}

	// Bounded against a pathological never-echoing stream.
	for i := 0; i < nativeEchoSuppressCap+5; i++ {
		p.expectEchoSuppress("x")
	}
	p.nativeEchoMu.Lock()
	n := len(p.nativeEchoSuppress)
	p.nativeEchoMu.Unlock()
	if n != nativeEchoSuppressCap {
		t.Fatalf("suppress queue cap not enforced: len=%d want=%d", n, nativeEchoSuppressCap)
	}
}

// End-to-end ordering: the input-time emit precedes the reply, and the late echo
// is dropped — exactly one user_text, ahead of the assistant text.
func TestNativeInputTimeUserTextPrecedesReply(t *testing.T) {
	t.Parallel()
	p := newTestNativePane()

	// Fresh-turn input path (what paneNativeInput does): register + emit early.
	p.expectEchoSuppress("hi there")
	p.emitNativeEvent(NativeEvent{Type: EvUserText, Text: "hi there"})

	// CLI now streams: reply text FIRST, then the late user echo.
	p.processNativeLine([]byte(`{"type":"assistant","message":{"id":"m1","role":"assistant","content":[{"type":"text","text":"Hey there"}]}}`), false, false)
	p.processNativeLine([]byte(fxUserEcho), false, false)

	types := ringTypes(p)
	users := 0
	for _, ty := range types {
		if ty == EvUserText {
			users++
		}
	}
	if users != 1 {
		t.Fatalf("want exactly one user_text (the input-time emit), got %d (types=%v)", users, types)
	}
	if len(types) == 0 || types[0] != EvUserText {
		t.Fatalf("user_text must precede the reply, types=%v", types)
	}
}
