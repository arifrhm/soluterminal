package main

import (
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

// Delivering a message into another agent's input box.
//
// Writing the body and a separate Enter (submitLineToPane) is necessary but
// not sufficient: a Codex paste burst can still be absorbing the body when the
// Enter arrives (the Enter becomes a newline inside the paste), and a message
// longer than one read used to sit in the composer as "[Pasted Content …]"
// with nobody told. So an agent message is delivered as:
//
//  1. paste the body (bracketed paste for non-Claude TUIs);
//  2. wait until the input box shows it, then press Enter as its own write;
//  3. confirm the text left the input box; if it is still there, press Enter
//     once more — never retype the body;
//  4. report what happened, so callers can say "not submitted" instead of
//     pretending the message was sent.
//
// "Left the input box" is the signal, not pane busyness: our own paste echo
// makes the pane look busy before any turn starts.

// errDeliveryLeftInComposer marks a message that reached the input box but
// was not submitted. The text is still there, so callers must not retype or
// re-deliver it elsewhere (that duplicates the message); they surface it.
var errDeliveryLeftInComposer = errors.New("message left in the input box, not submitted")
var errDeliveryChoicePrompt = errors.New("pane is showing a choice menu; message was not sent")

type deliveryOutcome string

const (
	// deliverySubmitted: the text left the input box after Enter.
	deliverySubmitted deliveryOutcome = "submitted"
	// deliveryUnconfirmed: Enter was sent but the input box could not be
	// read, so delivery could not be checked (unknown layout).
	deliveryUnconfirmed deliveryOutcome = "sent_unconfirmed"
	// deliveryLeftInComposer: the text is still in the input box after a
	// second Enter; the user (or caller) has to act on it.
	deliveryLeftInComposer deliveryOutcome = "left_in_composer"
)

// Timing and screen access are variables so tests can drive the loop.
var (
	deliveryVisibleTimeout = 3 * time.Second
	deliverySubmitTimeout  = 3 * time.Second
	deliveryRetryAfter     = 1200 * time.Millisecond
	deliveryPollInterval   = 150 * time.Millisecond
	deliverySleep          = time.Sleep
	deliveryScreenText     = paneVisibleScreen
	// deliveryMenuLive confirms a menu on screen is still unanswered.
	deliveryMenuLive = func(p *Pane) bool { return p.computeStateCached() == StateChoicePrompt }
)

// paneVisibleScreen prefers the grid model the state machine reads (menus,
// prompts); the replay emulator is not always attached.
func paneVisibleScreen(p *Pane) string {
	if screen, ok := p.gridVisibleText(); ok {
		return screen
	}
	if screen, ok := p.screenVisibleText(); ok {
		return screen
	}
	if p.buffer == nil {
		return ""
	}
	return stripANSI(p.buffer.SnapshotTail(8192))
}

// composerDraft returns the whitespace-free text of the agent's input box and
// whether the layout was recognised.
func composerDraft(screen string, codex bool) (string, bool) {
	if !codex {
		_, draft, known := conditionalClaudeComposer(screen)
		return draft, known
	}
	lines := strings.Split(screen, "\n")
	start := -1
	for i, line := range lines {
		if codexComposerLineRE.MatchString(line) {
			start = i
		}
	}
	if start < 0 {
		return "", false
	}
	var parts []string
	for i, line := range lines[start:] {
		if codexFooterLineRE.MatchString(line) {
			break
		}
		if i == 0 {
			line = strings.TrimPrefix(strings.TrimSpace(line), "›")
		}
		parts = append(parts, line)
	}
	return conditionalDraftText(strings.Join(parts, "")), true
}

// composerHolds reports whether the input box still shows body (or the paste
// placeholder a long body collapses into), and whether the box was readable.
func composerHolds(screen string, codex bool, body string) (holds, known bool) {
	draft, known := composerDraft(screen, codex)
	if !known || draft == "" {
		return false, known
	}
	// Claude and Codex collapse a long or multi-line paste into a
	// "[Pasted …]" placeholder; a short single line never collapses, so a
	// placeholder then is someone else's paste, not ours.
	if strings.Contains(draft, "[Pasted") && (len(body) > 200 || strings.Contains(body, "\n")) {
		return true, true
	}
	return strings.Contains(draft, deliveryMarker(body)), true
}

// deliveryMarker is the start of the body as the composer renders it: no
// whitespace, at most 24 runes (wrapping and collapsing keep the start).
func deliveryMarker(body string) string {
	m := conditionalDraftText(body)
	if utf8.RuneCountInString(m) <= 24 {
		return m
	}
	return string([]rune(m)[:24])
}

// waitComposer polls until composerHolds(...) == want or the timeout passes.
// It returns the last observation. An unreadable input box returns at once:
// waiting cannot observe anything, so the caller falls back to the plain
// gap-then-Enter timing instead of adding a dead delay.
func waitComposer(p *Pane, codex bool, body string, want bool, timeout time.Duration) (holds, known bool) {
	deadline := time.Now().Add(timeout)
	for {
		holds, known = composerHolds(deliveryScreenText(p), codex, body)
		if !known || holds == want || !time.Now().Before(deadline) {
			return holds, known
		}
		deliverySleep(deliveryPollInterval)
	}
}

// submitAgentMessage types body into a Claude or Codex pane and submits it,
// following the contract at the top of this file. It returns the bytes
// written and the outcome; an error means a PTY write failed.
func submitAgentMessage(p *Pane, body string, hasClaude bool, senders ...string) (wrote int, outcome deliveryOutcome, err error) {
	p.deliveryMu.Lock()
	defer p.deliveryMu.Unlock()
	defer func() {
		ev := coordinationEvent{Kind: "deliver", Pane: p.ID, Outcome: string(outcome)}
		if len(senders) > 0 {
			ev.By = senders[0]
		}
		switch {
		case errors.Is(err, errDeliveryChoicePrompt):
			ev.Outcome = "menu"
		case err != nil:
			ev.Outcome = "error"
		}
		coordLog.record(ev)
	}()
	// A queued delivery may have waited behind a whole turn. Recheck the
	// current screen after acquiring the delivery lock, before typing anything.
	// The pane state confirms the menu is live: an answered menu can stay on
	// screen, and computeState tracks which ones were answered.
	if isGridChoicePromptText(deliveryScreenText(p), !hasClaude) && deliveryMenuLive(p) {
		return 0, "", errDeliveryChoicePrompt
	}
	body = strings.TrimRight(body, "\r\n")
	var revision uint64
	defer func() {
		if outcome == deliveryLeftInComposer {
			sender := "SoluTerminal"
			if len(senders) > 0 && senders[0] != "" {
				sender = senders[0]
			}
			p.mu.Lock()
			p.unsentMessage = &unsentPaneMessage{ID: fmt.Sprintf("%d", time.Now().UnixNano()), Sender: sender, body: body, claude: hasClaude, revision: revision, connection: p.agentConn}
			p.mu.Unlock()
		}
	}()
	codex := !hasClaude
	first := []byte(body)
	if codex {
		first = []byte("\x1b[200~" + body + "\x1b[201~")
	}
	wrote, err = p.Write(first)
	if err != nil {
		return wrote, "", errors.New("write failed: " + err.Error())
	}
	// Enter must not land inside the paste: wait until the box shows it, and
	// never less than the established gap.
	deliverySleep(EnterGap(hasClaude))
	waitComposer(p, codex, body, true, deliveryVisibleTimeout)
	n, err := p.Write([]byte("\r"))
	wrote += n
	if err != nil {
		return wrote, "", errors.New("submit-Enter failed: " + err.Error())
	}
	holds, known := waitComposer(p, codex, body, false, deliveryRetryAfter)
	if !known {
		return wrote, deliveryUnconfirmed, nil
	}
	if !holds {
		return wrote, deliverySubmitted, nil
	}
	// Still in the box: the first Enter became a newline inside the paste.
	revision = p.inputRevision.Load() + 1
	n, err = p.Write([]byte("\r"))
	wrote += n
	if err != nil {
		return wrote, "", errors.New("second submit-Enter failed: " + err.Error())
	}
	holds, known = waitComposer(p, codex, body, false, deliverySubmitTimeout)
	switch {
	case !known:
		return wrote, deliveryUnconfirmed, nil
	case holds:
		return wrote, deliveryLeftInComposer, nil
	}
	return wrote, deliverySubmitted, nil
}

// paneAgentProvider reports whether p runs Claude or Codex in its terminal;
// other panes (shells, REPLs) keep raw keystroke semantics.
func paneAgentProvider(p *Pane) (agent bool, hasClaude bool) {
	snap := p.Snapshot()
	if snap.Kind == "native" {
		return false, false
	}
	// The recorded agent wins: a Codex pane whose tool spawned a claude child
	// still needs Codex delivery (bracketed paste).
	switch strings.TrimSpace(snap.Agent) {
	case agentCodex:
		return true, false
	case agentClaude:
		return true, true
	}
	if p.HasClaudeDescendant() {
		return true, true
	}
	if p.HasCodexDescendant() {
		return true, false
	}
	return false, false
}

// deliveryNotice is the caller-facing sentence for an outcome.
func deliveryNotice(o deliveryOutcome) string {
	switch o {
	case deliverySubmitted:
		return "the agent received it"
	case deliveryLeftInComposer:
		return "NOT submitted: the text is still in that pane's input box (pressing Enter twice did not send it); tell the user, do not resend"
	case deliveryUnconfirmed:
		return "Enter was sent but delivery could not be confirmed from the screen"
	}
	return string(o)
}
