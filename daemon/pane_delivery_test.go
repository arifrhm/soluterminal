package main

import (
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const (
	deliveryClaudeFooter = "\n ~ [Opus 4.8 (1M context) | ctx: 3%]\n"
	deliveryCodexFooter  = "\n  Context 0% used · GPT-6.1-Sol high · ~/wsl-projects/soluterminal\n"
)

func deliveryClaudeScreen(composer string) string {
	return "Previous response\n✻ Brewed for 1s\n❯ " + composer + deliveryClaudeFooter
}

func deliveryCodexScreen(composer string) string {
	return "• Earlier turn output\n\n› " + composer + "\n" + deliveryCodexFooter
}

func TestComposerHoldsReadsBothAgents(t *testing.T) {
	const body = "Please review the diff on branch engine-shelve and list must-fix items only"
	cases := []struct {
		name   string
		screen string
		codex  bool
		holds  bool
	}{
		{"claude empty", deliveryClaudeScreen(""), false, false},
		{"claude our text", deliveryClaudeScreen(body), false, true},
		{"claude collapsed paste", deliveryClaudeScreen("[Pasted text #3 +12 lines]"), false, true},
		{"claude suggestion is not ours", deliveryClaudeScreen("is it safe to close this session now"), false, false},
		{"codex placeholder", deliveryCodexScreen("Ask Codex to do anything"), true, false},
		{"codex our text", deliveryCodexScreen(body), true, true},
		{"codex stuck paste", deliveryCodexScreen("[Pasted Content 1024 chars]ending of the message"), true, true},
	}
	long := strings.Repeat(body+" ", 4)
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			msg := body
			if strings.Contains(c.screen, "[Pasted") {
				msg = long // only long or multi-line messages collapse
			}
			holds, known := composerHolds(c.screen, c.codex, msg)
			if !known || holds != c.holds {
				t.Fatalf("composerHolds = %v (known %v), want %v", holds, known, c.holds)
			}
		})
	}
	if _, known := composerHolds("plain shell output\n$ ", true, body); known {
		t.Fatal("a screen without a Codex composer must be unreadable, not empty")
	}
}

func TestAgentMessageBodyKeepsMultilineMessages(t *testing.T) {
	cases := []struct {
		in   string
		body string
		ok   bool
	}{
		{"line one\nline two\n", "line one\nline two", true},
		{"single message\r\n", "single message", true},
		{"no enter", "", false},
		{"y\n", "", false},
		{"1\n", "", false},
		{"\n", "", false},
	}
	for _, c := range cases {
		body, ok := agentMessageBody(c.in)
		if ok != c.ok || body != c.body {
			t.Errorf("agentMessageBody(%q) = %q, %v; want %q, %v", c.in, body, ok, c.body, c.ok)
		}
	}
}

// stubDeliveryScreen makes the delivery loop read *screen and run fast.
func stubDeliveryScreen(t *testing.T, screen *atomic.Value) {
	t.Helper()
	prev := []any{deliveryScreenText, deliverySleep, deliveryVisibleTimeout, deliverySubmitTimeout, deliveryRetryAfter, deliveryPollInterval, deliveryMenuLive}
	deliveryScreenText = func(*Pane) string { return screen.Load().(string) }
	deliveryMenuLive = func(*Pane) bool { return true }
	deliverySleep = func(time.Duration) { time.Sleep(time.Millisecond) }
	deliveryVisibleTimeout, deliverySubmitTimeout, deliveryRetryAfter, deliveryPollInterval = 300*time.Millisecond, 300*time.Millisecond, 300*time.Millisecond, time.Millisecond
	t.Cleanup(func() {
		deliveryScreenText = prev[0].(func(*Pane) string)
		deliveryMenuLive = prev[6].(func(*Pane) bool)
		deliverySleep = prev[1].(func(time.Duration))
		deliveryVisibleTimeout, deliverySubmitTimeout, deliveryRetryAfter, deliveryPollInterval = prev[2].(time.Duration), prev[3].(time.Duration), prev[4].(time.Duration), prev[5].(time.Duration)
	})
}

func runDelivery(p *Pane, body string) <-chan deliveryOutcome {
	done := make(chan deliveryOutcome, 1)
	go func() {
		_, outcome, _ := submitAgentMessage(p, body, false)
		done <- outcome
	}()
	return done
}

func TestSubmitAgentMessagePressesEnterAgainWhenTextStaysInTheBox(t *testing.T) {
	const body = "Run the review and report must-fix findings"
	p, frames := newTypingHoldPane(t, "delivery-retry")
	var screen atomic.Value
	screen.Store(deliveryCodexScreen("[Pasted Content 1024 chars]" + body))
	stubDeliveryScreen(t, &screen)
	done := runDelivery(p, body+"\n")
	expectFrame(t, frames, "bracketed body", "\x1b[200~"+body+"\x1b[201~")
	expectFrame(t, frames, "first Enter", "\r")
	expectFrame(t, frames, "second Enter", "\r")
	screen.Store(deliveryCodexScreen("Ask Codex to do anything"))
	if got := <-done; got != deliverySubmitted {
		t.Fatalf("outcome = %q, want submitted", got)
	}
	expectNoFrame(t, frames, "no retyping")
}

func TestSubmitAgentMessageReportsTextLeftInTheBox(t *testing.T) {
	const body = "Hand this task back to Benjamin with the summary"
	p, frames := newTypingHoldPane(t, "delivery-stuck")
	var screen atomic.Value
	screen.Store(deliveryCodexScreen(body))
	stubDeliveryScreen(t, &screen)
	done := runDelivery(p, body)
	expectFrame(t, frames, "body", "\x1b[200~"+body+"\x1b[201~")
	expectFrame(t, frames, "first Enter", "\r")
	expectFrame(t, frames, "second Enter", "\r")
	if got := <-done; got != deliveryLeftInComposer {
		t.Fatalf("outcome = %q, want left_in_composer", got)
	}
	if msg := p.Snapshot().UnsentMessage; msg == nil || msg.body != body || msg.Sender != "SoluTerminal" {
		t.Fatalf("missing unsent receipt: %+v", msg)
	}
	expectNoFrame(t, frames, "gives up after two Enters, never retypes")
	if !strings.Contains(deliveryNotice(deliveryLeftInComposer), "NOT submitted") {
		t.Fatal("callers must be told plainly that the message was not submitted")
	}
}

func TestSubmitAgentMessageSingleEnterWhenItGoesThrough(t *testing.T) {
	const body = "Summarise what changed"
	p, frames := newTypingHoldPane(t, "delivery-ok")
	var screen atomic.Value
	screen.Store(deliveryCodexScreen(body))
	stubDeliveryScreen(t, &screen)
	done := runDelivery(p, body)
	expectFrame(t, frames, "body", "\x1b[200~"+body+"\x1b[201~")
	expectFrame(t, frames, "Enter", "\r")
	screen.Store(deliveryCodexScreen("Ask Codex to do anything"))
	if got := <-done; got != deliverySubmitted {
		t.Fatalf("outcome = %q, want submitted", got)
	}
	expectNoFrame(t, frames, "no second Enter")
}

func TestAgentInputBoxIgnoresFirstRunTrustMenus(t *testing.T) {
	claudeTrust := "Quick safety check: Is this a project you created or one you trust?\n" +
		"Claude Code'll be able to read, edit, and execute files here.\n" +
		" ❯ No, exit\n   Yes, I trust this folder\n Enter to confirm · Esc to cancel\n"
	codexTrust := "Do you trust the contents of this directory?\n" +
		"› 1. Yes, continue\n  2. No, quit\n  Press enter to continue\n"
	cases := []struct {
		name, screen, agent string
		want                bool
	}{
		{"claude trust menu", claudeTrust, agentClaude, false},
		{"claude input box", deliveryClaudeScreen(""), agentClaude, true},
		{"claude suggestion in box", deliveryClaudeScreen("Try \"how does <filepath> work?\""), agentClaude, true},
		{"codex trust menu", codexTrust, agentCodex, false},
		{"codex input box", deliveryCodexScreen("Ask Codex to do anything"), agentCodex, true},
	}
	for _, c := range cases {
		if got := agentInputBoxVisible(c.screen, c.agent); got != c.want {
			t.Errorf("%s: agentInputBoxVisible = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestSubmitAgentMessageClaudeWritesPlainBodyThenEnter(t *testing.T) {
	const body = "First line of the request\nSecond line of the request"
	p, frames := newTypingHoldPane(t, "delivery-claude")
	var screen atomic.Value
	screen.Store(deliveryClaudeScreen("[Pasted text #1 +1 lines]"))
	stubDeliveryScreen(t, &screen)
	done := make(chan deliveryOutcome, 1)
	go func() {
		_, outcome, _ := submitAgentMessage(p, body, true)
		done <- outcome
	}()
	expectFrame(t, frames, "plain body (no bracketed paste for Claude)", body)
	expectFrame(t, frames, "Enter", "\r")
	screen.Store(deliveryClaudeScreen(""))
	if got := <-done; got != deliverySubmitted {
		t.Fatalf("outcome = %q, want submitted", got)
	}
}

func TestSubmitLineToPaneReportsStuckMessagesDistinctly(t *testing.T) {
	const body = "[solu] delegated work update: task t1 completed"
	p, frames := newTypingHoldPane(t, "delivery-sentinel")
	var screen atomic.Value
	screen.Store(deliveryCodexScreen(body))
	stubDeliveryScreen(t, &screen)
	done := make(chan error, 1)
	go func() {
		_, err := submitLineToPane(p, body, false)
		done <- err
	}()
	expectFrame(t, frames, "body", "\x1b[200~"+body+"\x1b[201~")
	expectFrame(t, frames, "first Enter", "\r")
	expectFrame(t, frames, "second Enter", "\r")
	// Callers must be able to tell "stuck in the box" from a failed write:
	// re-delivering a stuck line types it twice.
	if err := <-done; !errors.Is(err, errDeliveryLeftInComposer) {
		t.Fatalf("err = %v, want errDeliveryLeftInComposer", err)
	}
}

func TestAgentMessageBodyLeavesKeystrokesRawAndNormalisesBreaks(t *testing.T) {
	if _, ok := agentMessageBody("\x1b[A\r"); ok {
		t.Fatal("an arrow-key sequence must stay a raw keystroke")
	}
	if _, ok := agentMessageBody("abc\x03def\n"); ok {
		t.Fatal("a Ctrl chord inside text must stay raw")
	}
	if body, ok := agentMessageBody("one\r\ntwo\rthree\n"); !ok || body != "one\ntwo\nthree" {
		t.Fatalf("body = %q, %v; want CR normalised to newlines", body, ok)
	}
}

func TestComposerPlaceholderCountsOnlyForCollapsibleBodies(t *testing.T) {
	screen := deliveryCodexScreen("[Pasted Content 1500 chars]")
	if holds, _ := composerHolds(screen, true, "short reply please"); holds {
		t.Fatal("a short single-line message never collapses; the placeholder is someone else's paste")
	}
	if holds, _ := composerHolds(screen, true, strings.Repeat("long message ", 30)); !holds {
		t.Fatal("a long message collapses into the placeholder")
	}
}

func TestVerifiedDeliveryRechecksMenuAfterWaitingForAnotherDelivery(t *testing.T) {
	p, frames := newTypingHoldPane(t, "queued-delivery")
	var screen atomic.Value
	screen.Store(deliveryClaudeScreen(""))
	stubDeliveryScreen(t, &screen)
	p.deliveryMu.Lock()
	done := make(chan error, 1)
	go func() { _, _, err := submitAgentMessage(p, "Please review the code", true); done <- err }()
	screen.Store("Do you want to proceed?\n❯ 1. Yes\n  2. No\n")
	p.deliveryMu.Unlock()
	if err := <-done; !errors.Is(err, errDeliveryChoicePrompt) {
		t.Fatalf("error %v", err)
	}
	expectNoFrame(t, frames, "never type a queued message into a new menu")
}

func TestVerifiedDeliveryIgnoresAnAnsweredMenuStillOnScreen(t *testing.T) {
	p, frames := newTypingHoldPane(t, "answered-menu")
	var screen atomic.Value
	screen.Store("Do you want to proceed?\n❯ 1. Yes\n  2. No\n")
	stubDeliveryScreen(t, &screen)
	deliveryMenuLive = func(*Pane) bool { return false } // answered; still drawn
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _, _ = submitAgentMessage(p, "Please review the code", true)
	}()
	// Join before stubDeliveryScreen restores its globals, including when
	// this test fails before the complete delivery loop has returned.
	t.Cleanup(func() { <-done })
	select {
	case <-frames:
	case <-time.After(2 * time.Second):
		t.Fatal("an answered menu still on screen must not block delivery")
	}
}
