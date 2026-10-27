package main

import (
	"sync/atomic"
	"testing"
	"time"
)

// museNetworkMenuFixture is the Muse TUI network-access approval menu in
// its on-screen (replay-grid) shape, modeled on a live capture from a
// terminal Muse pane (2026-09-21): normally spaced, `›`-led focused row,
// shortcut hints. It is duplicated here on purpose: this test is the
// cross-language contract. SoluTerminal per-pane AutoMode only works for
// a Muse pane if this text is classified as an approval prompt, so a
// reword in the client menu must fail here.
const museNetworkMenuFixture = "Would you like to allow this network access?\n" +
	"\n" +
	"  network: 127.0.0.1:8787 http\n" +
	"  requested by:\n" +
	"  $ curl -sS http://127.0.0.1:8787/api/panes | head -c 3000\n" +
	"\n" +
	"\u203a 1. Yes, proceed (y)\n" +
	"  2. Yes, don't ask again this session (p)  127.0.0.1:8787 (http)\n" +
	"  3. Always allow this network destination  127.0.0.1:8787 (http)\n" +
	"  4. No, and tell Muse Code what to do differently (esc)"

func TestMuseNetworkMenuIsAutoApprovable(t *testing.T) {
	t.Parallel()
	if matches := approvalPromptMatches(museNetworkMenuFixture, true); len(matches) == 0 {
		t.Fatal("approvalPromptMatches missed the Muse network menu")
	}
	if !isGridChoicePromptText(museNetworkMenuFixture, true) {
		t.Fatal("Muse network menu is not a grid choice prompt")
	}
	if !isAutoApprovalPromptText(museNetworkMenuFixture, true) {
		t.Fatal("isAutoApprovalPromptText rejected the Muse menu; AutoMode would not fire")
	}
	if isAutoApprovalPromptText("\u25c7 Calling tools (3m 18s \u00b7 esc to interrupt)", true) {
		t.Fatal("streaming status line classified as approval prompt")
	}
}

// museScreenPane builds a terminal pane whose raw tail carries no
// recognizable menu (Muse cell-diff residue) while the replay screen
// shows the network menu — the exact live shape that parked a Muse pane
// on busy forever with auto-approval on.
func museScreenPane(t *testing.T, id string) *Pane {
	t.Helper()
	p := &Pane{
		ID:             id,
		Status:         PaneRunning,
		LastActivityMs: time.Now().Add(-5 * time.Second).UnixMilli(),
		buffer:         NewRingBuffer(1 << 16),
		subs:           map[int64]*subEntry{},
		done:           make(chan struct{}),
	}
	tail := "\u25c7 Calling tools (3m 18s \u00b7 esc to interrupt)\nstreaming model output line\n"
	if _, err := p.buffer.Write([]byte(tail)); err != nil {
		t.Fatalf("buffer.Write: %v", err)
	}
	atomic.StoreInt64(&p.TotalStrippedChars, int64(len(stripANSI([]byte(tail)))))
	paintReplayScreen(t, p, museNetworkMenuFixture)
	return p
}

func TestComputeStateScreenFallbackAdmitsMuseMenu(t *testing.T) {
	t.Parallel()
	p := museScreenPane(t, "pane-muse-screen-fallback")
	defer close(p.done)
	if got := p.computeStateWithHasCodex(true); got != StateChoicePrompt {
		t.Fatalf("screen-menu computeState = %q, want %q", got, StateChoicePrompt)
	}
	// Answering must settle it: a same-fingerprint menu with input past
	// the ack falls through instead of re-firing every poll.
	acked := atomic.LoadInt64(&p.LastAckedChoicePromptPos)
	atomic.StoreInt64(&p.LastInputStrippedPos, acked+100)
	if got := p.computeStateWithHasCodex(true); got != StateIdle {
		t.Fatalf("answered screen menu computeState = %q, want %q", got, StateIdle)
	}
}

func TestCurrentAutoApprovalPromptTextScreenFallback(t *testing.T) {
	t.Parallel()
	p := museScreenPane(t, "pane-muse-screen-confirm")
	defer close(p.done)
	defer p.shutdownGrid()
	if text, ok := currentAutoApprovalPromptText(p); !ok || text == "" {
		t.Fatal("screen-menu currentAutoApprovalPromptText declined; directAutoFire would never fire")
	}
}
