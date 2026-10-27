package main

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
)

// At 49 columns the changing command in option 2 wraps onto a continuation
// row. The option-row fingerprint used to miss both that row and the action
// above the menu, stranding the next permission as an already answered menu.
func codexNarrowCommandMenu(command string) string {
	return "Would you like to run the following command?\n\n" +
		"  Environment: local\n\n  Reason: Run the release checks?\n\n  $ " + command + "\n\n" +
		"› 1. Yes, proceed (y)\n" +
		"  2. Yes, and don't ask again for commands that\n" +
		"     start with `" + command + "` (p)\n" +
		"  3. No, and tell Codex what to do differently\n" +
		"     (esc)\n\n  Press enter to confirm or esc to cancel"
}

func TestCodexNarrowCommandTurnoverApprovesEachNewActionOnce(t *testing.T) {
	a, p, frames, _ := newDirectAutoRepeatHarness(t, "pane-codex-command-turnover")
	p.forceCodexForTest = true
	for _, command := range []string{"GOMAXPROCS=4 go vet ./...", "GOMAXPROCS=4 go vet -p 2 ./...", "GOMAXPROCS=4 go test ./..."} {
		seedGridMenu(t, p, codexNarrowCommandMenu(command))
		assertDirectAutoPromptReady(t, p, command)
		// A cell-diff repaint advances output without repeating the menu in
		// the raw stream. No absent frame occurs between these modal requests.
		appendDirectAutoMenuRedraw(t, p, "  •  redraw residue")
		a.directAutoFire(context.Background(), p.ID, p.Title)
		expectFrame(t, frames, command, "\r")
		if input, ack := atomic.LoadInt64(&p.LastInputStrippedPos), atomic.LoadInt64(&p.LastAckedChoicePromptPos); input <= ack {
			t.Fatalf("fixture input=%d must exceed acknowledged position=%d", input, ack)
		}
		seedGridMenu(t, p, codexNarrowCommandMenu(command))
		if p.computeState() == StateChoicePrompt {
			t.Fatal("unchanged answered command was rearmed")
		}
		a.directAutoFire(context.Background(), p.ID, p.Title)
		expectNoFrame(t, frames, "unchanged answered command")
	}
}

func TestCodexCommandIdentityIncludesWrappedAction(t *testing.T) {
	first := codexNarrowCommandMenu("go test ./...\n    -run First")
	second := strings.ReplaceAll(first, "-run First", "-run Second")
	if approvalPromptFingerprint(first, true) == approvalPromptFingerprint(second, true) {
		t.Fatal("different command continuation shared a prompt identity")
	}
}
