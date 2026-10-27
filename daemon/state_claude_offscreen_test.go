package main

import (
	"sync/atomic"
	"testing"
)

const claudeOffscreenMenu = "Bash command\n  printf 1 > /tmp/f1\n\nDo you want to proceed?\n❯ 1. Yes\n  2. Yes, and don't ask again for printf commands\n  3. No\n\nEsc to cancel · Tab to amend"

// After an answered Claude dialog, Claude repaints the rows it occupied and
// the raw tail matches the menu again at a newer position. With the rendered
// screen showing only the composer, that is not a pending prompt.
func TestComputeStateIgnoresClaudeMenuOnlyInScrollback(t *testing.T) {
	for _, tc := range []struct {
		name   string
		claude bool
		screen string
		want   PaneRuntimeState
	}{
		{"claude, menu cleared from screen", true, "● Done.\n\n❯ \n  ? for shortcuts", ""},
		{"claude, menu on screen", true, claudeOffscreenMenu, StateChoicePrompt},
		{"other TUI keeps the raw scan", false, "● Done.\n\n❯ \n  ? for shortcuts", StateChoicePrompt},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := testPaneWithTail(t, "pane-offscreen", "Offscreen", "● Running\n"+claudeOffscreenMenu+"\n")
			if tc.claude {
				p.ClaudeSessionID = "session-offscreen"
			}
			seedGridMenu(t, p, tc.screen)
			got := p.computeStateWithHasCodex(false)
			if tc.want == StateChoicePrompt && got != StateChoicePrompt {
				t.Fatalf("state = %s, want choice-prompt", got)
			}
			if tc.want != StateChoicePrompt && got == StateChoicePrompt {
				t.Fatal("raw-only Claude menu reported as a pending choice prompt")
			}
		})
	}
}

// The menu reappears on screen later (a genuinely new dialog): the raw match
// acknowledged while off screen must not suppress it.
func TestClaudeOffscreenMenuDoesNotSuppressLaterRealMenu(t *testing.T) {
	p := testPaneWithTail(t, "pane-offscreen-later", "Offscreen later", "● Running\n"+claudeOffscreenMenu+"\n")
	p.ClaudeSessionID = "session-offscreen-later"
	seedGridMenu(t, p, "● Done.\n\n❯ ")
	if p.computeStateWithHasCodex(false) == StateChoicePrompt {
		t.Fatal("off-screen menu reported")
	}
	next := "\n● Next\n" + claudeOffscreenMenu + "\n"
	if _, err := p.buffer.Write([]byte(next)); err != nil {
		t.Fatal(err)
	}
	atomic.AddInt64(&p.TotalStrippedChars, int64(len(stripANSI([]byte(next)))))
	seedGridMenu(t, p, claudeOffscreenMenu)
	if got := p.computeStateWithHasCodex(false); got != StateChoicePrompt {
		t.Fatalf("real menu state = %s, want choice-prompt", got)
	}
}
