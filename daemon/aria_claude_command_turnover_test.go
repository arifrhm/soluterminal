package main

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
)

func claudeQueuedCommandMenu(command string, ordinal, total int) string {
	return fmt.Sprintf("Bash command · from the general-purpose agent          %d of %d\n", ordinal, total) +
		"Tip: auto mode handles these prompts for you — choose \"switch to auto mode\"\n" +
		"below\nInspect draft save + history refetch\n╌╌╌╌╌╌╌╌╌╌╌╌\n│ " + command + "\n╌╌╌╌╌╌╌╌╌╌╌╌\n" +
		"Do you want to proceed?\n❯ 1. Yes\n" +
		"  2. Yes, allow reading from\n" +
		"     /home/user/wsl-projects/shinoque-wt-notifications/apps/web/src from\n" +
		"     this project\n" +
		"  3. Yes, and switch to auto mode · auto mode handles these prompts for you\n" +
		"  4. No\nEsc to cancel · Tab to amend · ctrl+x ctrl+k twice to stop background agents"
}

// Claude paints cell diffs without ratatui's synchronized-output markers.
// Keep the byte tail degraded, as it was in shinoque3 pane #2, and never
// manufacture a lifecycle edge on behalf of the fixture.
func paintClaudePermission(t *testing.T, p *Pane, menu string) {
	t.Helper()
	p.ensureGrid()
	p.subsMu.Lock()
	p.feedGridPayloadLocked([]byte("\x1b[2J\x1b[H"+strings.ReplaceAll(menu, "\n", "\r\n")), true)
	p.subsMu.Unlock()
}

func TestClaudeQueuedCommandTurnoverApprovesEachRequestOnce(t *testing.T) {
	a, p, frames, _ := newDirectAutoRepeatHarness(t, "claude-queued-command-turnover")
	p.forceCodexForTest = false
	p.Cols, p.Rows = 82, 56
	p.resizeGridEmu(82, 56)
	initialGeneration := atomic.LoadUint64(&p.GridChoicePromptGeneration)
	for i, command := range []string{"cat routes/Settings.tsx", "cat routes/chats/use-conversation-drafts.ts", "cat routes/chats/use-conversation-drafts.ts", "cat routes/chats/use-conversation-drafts.ts"} {
		menu := claudeQueuedCommandMenu(command, i+11, 17)
		paintClaudePermission(t, p, menu)
		assertDirectAutoPromptReady(t, p, command)
		appendDirectAutoMenuRedraw(t, p, "  • redraw residue")
		a.directAutoFire(context.Background(), p.ID, p.Title)
		expectFrame(t, frames, command, "\r")
		paintClaudePermission(t, p, menu)
		if state := p.computeState(); state == StateChoicePrompt {
			t.Fatal("answered Claude permission rearmed on redraw")
		}
		a.directAutoFire(context.Background(), p.ID, p.Title)
		expectNoFrame(t, frames, "answered Claude permission")
	}
	if generation := atomic.LoadUint64(&p.GridChoicePromptGeneration); generation != initialGeneration {
		t.Fatalf("fixture must reproduce Claude's untracked frame lifecycle, got %d", generation)
	}
}

func TestClaudeVisiblePermissionIdentityWinsOverStaleTail(t *testing.T) {
	_, p, _, _ := newDirectAutoRepeatHarness(t, "claude-visible-request")
	p.forceCodexForTest = false
	p.Cols, p.Rows = 82, 56
	p.resizeGridEmu(82, 56)
	old := claudeQueuedCommandMenu("cat old.ts", 11, 17)
	current := claudeQueuedCommandMenu("cat current.ts", 12, 17)
	appendDirectAutoMenuRedraw(t, p, old)
	paintClaudePermission(t, p, current)
	_, _, hash, ok := currentChoicePromptIdentity(p, old)
	if !ok || hash != approvalPromptFingerprint(current, false) {
		t.Fatal("stale raw output replaced the currently visible Claude request")
	}
}

func TestClaudeChangedRequestInvalidatesPendingEnter(t *testing.T) {
	_, p, _, _ := newDirectAutoRepeatHarness(t, "claude-changed-request")
	p.forceCodexForTest = false
	p.Cols, p.Rows = 82, 56
	p.resizeGridEmu(82, 56)
	paintClaudePermission(t, p, claudeQueuedCommandMenu("cat old.ts", 11, 17))
	_, pos, hash, ok := currentChoicePromptIdentity(p, "")
	if !ok || hash == 0 {
		t.Fatal("missing initial request identity")
	}
	paintClaudePermission(t, p, claudeQueuedCommandMenu("cat current.ts", 12, 17))
	if choicePromptIdentityMatches(p, pos, hash) {
		t.Fatal("a pending Enter for the old command still matches its replacement")
	}
}

func TestClaudeVisibleQuestionCannotInheritStalePermission(t *testing.T) {
	a, p, frames, _ := newDirectAutoRepeatHarness(t, "claude-stale-permission")
	p.forceCodexForTest = false
	p.Cols, p.Rows = 82, 56
	p.resizeGridEmu(82, 56)
	appendDirectAutoMenuRedraw(t, p, claudeQueuedCommandMenu("cat old.ts", 11, 17))
	paintClaudePermission(t, p, "Which approach should I implement?\n❯ 1. Redesign\n  2. Keep existing UI\nEnter to select · Esc to cancel")
	a.directAutoFire(context.Background(), p.ID, p.Title)
	expectNoFrame(t, frames, "current question above stale permission tail")
}

func TestClaudeCommandIdentityDistinguishesRequests(t *testing.T) {
	first := claudeQueuedCommandMenu("cat reports/2026-10-01.txt", 11, 17)
	base := approvalPromptFingerprint(first, false)
	for name, next := range map[string]string{
		"command":                  strings.Replace(first, "2026-10-01", "2026-10-02", 1),
		"queued identical command": claudeQueuedCommandMenu("cat reports/2026-10-01.txt", 12, 17),
	} {
		t.Run(name, func(t *testing.T) {
			if got := approvalPromptFingerprint(next, false); got == 0 || got == base {
				t.Fatal("different Claude request shares an answered prompt identity")
			}
		})
	}
}

func TestClaudeCommandIdentityIgnoresUnrelatedOutput(t *testing.T) {
	first := claudeQueuedCommandMenu("cd /repo && cat routes/chats/drafts.ts", 12, 17)
	base := approvalPromptFingerprint(first, false)
	for name, next := range map[string]string{
		"queue grows":    claudeQueuedCommandMenu("cd /repo && cat routes/chats/drafts.ts", 12, 23),
		"wrapping":       strings.Replace(first, "&& cat", "&&\n│ cat", 1),
		"older menu":     claudeQueuedCommandMenu("cat old.ts", 11, 17) + "\n" + first,
		"spinner":        first + "\n✶ Working… (3m 21s · ↓ 146 tokens)",
		"queue wrapping": strings.Replace(first, "          12 of 17", "\n  12 of 17", 1),
	} {
		t.Run(name, func(t *testing.T) {
			if got := approvalPromptFingerprint(next, false); got != base {
				t.Fatalf("unrelated redraw changed current permission identity: %x != %x", got, base)
			}
		})
	}
}

func TestClaudeEditIdentityIncludesDiff(t *testing.T) {
	menu := "Edit file\nsrc/notifications.ts\n╌╌╌╌╌╌\n- enabled: false\n+ enabled: true\n╌╌╌╌╌╌\nDo you want to make this edit to notifications.ts?\n❯ 1. Yes\n  2. Yes, allow all edits during this session\n  3. No"
	changed := strings.Replace(menu, "+ enabled: true", "+ enabled: false", 1)
	if approvalPromptFingerprint(menu, false) == approvalPromptFingerprint(changed, false) {
		t.Fatal("different edits to the same file share an answered identity")
	}
}

func TestClaudeLongCommandTurnoverWithHeaderOffscreen(t *testing.T) {
	a, p, frames, _ := newDirectAutoRepeatHarness(t, "claude-clipped-command")
	p.forceCodexForTest = false
	p.Cols, p.Rows = 82, 56
	p.resizeGridEmu(82, 56)
	for i, command := range []string{"open('chats.ts','w').write(s)", "open('focus.ts','w').write(s)"} {
		body := strings.Repeat("│ # long command body\n", 70) + "│ " + command
		paintClaudePermission(t, p, claudeQueuedCommandMenu(body, i+1, 5))
		visible, ok := p.gridVisibleText()
		if !ok || strings.Contains(visible, "Bash command") || claudePermissionIdentity(visible) == "" {
			t.Fatal("fixture must identify the current boxed command with its header offscreen")
		}
		assertDirectAutoPromptReady(t, p, command)
		appendDirectAutoMenuRedraw(t, p, "  • partial command redraw")
		a.directAutoFire(context.Background(), p.ID, p.Title)
		expectFrame(t, frames, command, "\r")
		a.directAutoFire(context.Background(), p.ID, p.Title)
		expectNoFrame(t, frames, "answered clipped command")
	}
}

func TestClaudeClippedCommandIdentityIgnoresSurroundingConversation(t *testing.T) {
	menu := "│ cat current.ts\n╌╌╌╌╌\nThis command requires approval\n\nDo you want to proceed?\n❯ 1. Yes\n  2. No\nEsc to cancel · Tab to amend"
	base := approvalPromptFingerprint(menu, false)
	for _, prefix := range []string{"Previous activity\n", "✶ Working… (3m 21s)\n", "│ old unrelated output\n╌╌╌╌╌\n"} {
		if got := approvalPromptFingerprint(prefix+menu, false); got != base {
			t.Fatal("unrelated conversation rearmed the clipped command")
		}
	}
	if got := approvalPromptFingerprint(strings.Replace(menu, "current.ts", "next.ts", 1), false); got == base {
		t.Fatal("different clipped command shares the answered identity")
	}
	if got := claudePermissionIdentity(strings.Replace(menu, "│ cat current.ts", "Describe the implementation", 1)); got != "" {
		t.Fatal("ordinary conversation was treated as a clipped command")
	}
}

func TestClaudeClippedCommandIdentityWithApprovalAnnotations(t *testing.T) {
	command := "│ cd /repo && cat current.ts\n╌╌╌╌╌\n"
	question := "\nDo you want to proceed?\n❯ 1. Yes\n  2. No\nEsc to cancel · Tab to amend"
	base := approvalPromptFingerprint(command+question, false)
	for _, annotation := range []string{
		"Multiple directory changes in one command require approval for clarity\n",
		"┌─────────────────────┐\n│ This command may run repository hooks │\n└─────────────────────┘\n",
	} {
		menu := command + annotation + question
		if claudePermissionIdentity(menu) == "" {
			t.Fatal("approval annotation hid the clipped command identity")
		}
		if got := approvalPromptFingerprint(menu, false); got != base {
			t.Fatal("approval annotation rearmed the same command")
		}
		if got := approvalPromptFingerprint(strings.Replace(menu, "current.ts", "next.ts", 1), false); got == base {
			t.Fatal("different annotated commands share an answered identity")
		}
	}
}
