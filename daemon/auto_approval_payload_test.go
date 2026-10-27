package main

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// The live Launcher stall: these are code literals inside a pending Bash
// permission, immediately above the current confirmation question.
func claudePayloadPermission(payload string) string {
	return fixtureScreen(
		" Bash command · from the general-purpose agent 3",
		" │ python3 - <<'EOF'",
		" │ "+payload,
		" │ EOF",
		" ╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌",
		" Do you want to proceed?",
		" ❯ 1. Yes",
		"   2. Yes, allow reading from /tmp/project from this project",
		"   3. Yes, and switch to auto mode · auto mode handles these prompts for you",
		"   4. No",
		" Esc to cancel · Tab to amend",
	)
}

func TestAutoApprovalPayloadIsNotMenuQuestion(t *testing.T) {
	for _, phrase := range autoApprovalDenyHeaderPhrases {
		t.Run(phrase.needle, func(t *testing.T) {
			for name, screen := range map[string]string{
				"claude bash":   claudePayloadPermission("button = '" + phrase.needle + " Google'"),
				"claude create": strings.ReplaceAll(claudePayloadPermission("label = '"+phrase.needle+"'"), "Do you want to proceed?", "Do you want to create GoogleSetup.tsx?"),
				"codex command": strings.ReplaceAll(codexCommandApprovalScreen, "$ go test -C daemon ./...", "$ printf '%s' '"+phrase.needle+"'"),
				"codex edit":    strings.ReplaceAll(codexPatchApprovalScreen, "12 +new line", "12 +button = '"+phrase.needle+"'"),
			} {
				if reason := autoApprovalDeclineReason(screen, true); reason != "" {
					t.Errorf("%s payload declined: %s", name, reason)
				}
				if reason := ariaJevScopeDecline(screen); reason != "" {
					t.Errorf("%s Jev payload declined: %s", name, reason)
				}
			}
		})
	}
}

func TestAutoApprovalRealDecisionAfterPayloadStillDeclines(t *testing.T) {
	for _, phrase := range autoApprovalDenyHeaderPhrases {
		screen := fixtureScreen(phrase.needle, " ❯ 1. Yes", "   2. Yes, and don't ask again", "   3. No", " Esc to cancel")
		for _, prefix := range []string{"", codexCommandApprovalScreen + "\n"} {
			if reason := autoApprovalDeclineReason(prefix+screen, true); reason == "" {
				t.Errorf("actual %q decision approved after stale permission=%v", phrase.needle, prefix != "")
			}
		}
		// A real decision can use the same generic confirmation wording.
		proceed := strings.Replace(screen, " ❯ 1.", " Do you want to proceed?\n ❯ 1.", 1)
		if autoApprovalDeclineReason(proceed, true) == "" || autoApprovalDeclineReason(codexCommandApprovalScreen+"\n"+proceed, true) == "" {
			t.Errorf("actual %q decision hidden by generic proceed question", phrase.needle)
		}
		boxed := strings.Replace(proceed, phrase.needle, "╭──────────────────────╮\n│ "+phrase.needle+"\n╰──────────────────────╯", 1)
		if autoApprovalDeclineReason(boxed, true) == "" || autoApprovalDeclineReason(codexCommandApprovalScreen+"\n"+boxed, true) == "" {
			t.Errorf("actual boxed %q decision approved", phrase.needle)
		}
	}
}

// PR #529 review: payload-looking scrollback (an old diff row or "$ " line)
// with no menu between it and a real decision must not strip the decision's
// own deny phrase from the question.
func TestAutoApprovalStaleScrollbackPayloadDoesNotHideDecision(t *testing.T) {
	for _, phrase := range autoApprovalDenyHeaderPhrases {
		for name, stale := range map[string]string{
			"diff row":   "12 +x := 1",
			"shell line": "$ git status",
			"bash title": "Bash command",
		} {
			screen := fixtureScreen(
				stale,
				"some earlier transcript output",
				phrase.needle,
				" Do you want to proceed?",
				" ❯ 1. Yes",
				"   2. No",
				" Esc to cancel",
			)
			if reason := autoApprovalDeclineReason(screen, true); reason == "" {
				t.Errorf("%q decision approved behind stale %s", phrase.needle, name)
			}
			boxed := strings.Replace(screen, phrase.needle, "│ "+phrase.needle, 1)
			if reason := autoApprovalDeclineReason(boxed, true); reason == "" {
				t.Errorf("boxed %q decision approved behind stale %s", phrase.needle, name)
			}
		}
	}
}

func TestDirectAutoFireClaudeLoginLiteralQueue(t *testing.T) {
	p, frames := newTypingHoldPane(t, "claude-payload-queue")
	p.forceCodexForTest = false
	p.Cols, p.Rows = 160, 48
	a := testAriaWithPane(t, p)
	a.sweepStreaks = map[string]*sweepStreak{}
	a.evalLog = newAriaEvalLog()
	for _, payload := range []string{
		"{connections.data?.length ? 'Add Google account' : 'Sign in with Google'}",
		"{connections.data?.length ? 'Add Google account' : 'Log in with Google'}",
		"button = 'usage credits'",
	} {
		seedGridMenu(t, p, claudePayloadPermission(payload))
		a.directAutoFire(context.Background(), p.ID, p.Title)
		expectFrame(t, frames, "queued permission containing "+payload, "\r")
	}
}

func TestManualDeclinedPermissionDoesNotCreateDraft(t *testing.T) {
	for _, existingDraft := range []string{"", "real unfinished message"} {
		t.Run(existingDraft, func(t *testing.T) {
			p, frames := newTypingHoldPane(t, "declined-manual")
			p.Cols, p.Rows = 160, 48
			a := testAriaWithPane(t, p)
			a.sweepStreaks = map[string]*sweepStreak{}
			a.evalLog = newAriaEvalLog()
			if existingDraft != "" {
				_, _ = p.Write([]byte(existingDraft))
				expectFrame(t, frames, "draft", existingDraft)
			}
			// Human answers a real denied confirmation. The digit is consumed
			// by the dialog, independently of autonomous approval eligibility.
			seedGridMenu(t, p, fixtureScreen("Login method: Sign in with Google?", " ❯ 1. Yes", "   2. No", " Esc to cancel"))
			if _, err := p.Write([]byte("1")); err != nil {
				t.Fatal(err)
			}
			expectFrame(t, frames, "manual choice", "1")
			if got := string(p.promptRec.buf); got != existingDraft {
				t.Fatalf("menu key changed draft: got %q, want %q", got, existingDraft)
			}
			atomic.StoreInt64(&p.LastInputMs, time.Now().Add(-ariaAutoTypingSettle-time.Second).UnixMilli())
			seedGridMenu(t, p, codexNarrowCommandMenu("cat next.js"))
			a.directAutoFire(context.Background(), p.ID, p.Title)
			if existingDraft == "" {
				expectFrame(t, frames, "next approval without two-minute hold", "\r")
			} else {
				expectNoFrame(t, frames, "real draft remains protected")
			}
		})
	}
}
