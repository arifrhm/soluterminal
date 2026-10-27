package main

import (
	"strings"
	"testing"
	"time"
)

const claudeFolderTrustMenu = "Quick safety check: Is this a project you created or one you trust?\n" +
	"Claude Code'll be able to read, edit, and execute files here.\n" +
	" ❯ No, exit\n   Yes, I trust this folder\n Enter to confirm · Esc to cancel\n"

func TestFolderTrustIsChoiceNeedsUserAndNeverAutoApproved(t *testing.T) {
	for _, menu := range []string{
		claudeFolderTrustMenu,
		strings.ReplaceAll(claudeFolderTrustMenu, " ❯ No, exit\n   Yes", "   No, exit\n ❯ Yes"),
		"Do you trust this folder?\n❯ 1. Yes, I trust this folder\n  2. No, exit\nEnter to confirm\n",
	} {
		p := testPaneWithTail(t, "trust", "claude", menu)
		p.Agent = agentClaude
		p.AutoMode = "always"
		if !isGridChoicePromptText(menu, false) {
			t.Fatal("trust menu missed")
		}
		if state := p.computeState(); state != StateChoicePrompt {
			t.Fatalf("state %s", state)
		}
		if isAutoApprovalPromptText(menu, false) {
			t.Fatal("trust must never be auto-approved")
		}
		engine := newAttentionEngine(nil)
		att, reason := engine.CurrentFor(p, StateChoicePrompt)
		if att != AttentionNeedsUser || reason != folderTrustHint {
			t.Fatalf("attention %s: %s", att, reason)
		}
		if pushEffectiveState(StateChoicePrompt, att) != StateChoicePrompt {
			t.Fatal("push suppressed")
		}
	}
}

func TestSpawnReturnsImmediatelyOnFolderTrustWithoutSendingPrompt(t *testing.T) {
	p := testPaneWithTail(t, "trust", "claude", claudeFolderTrustMenu)
	p.Agent = agentClaude
	prev := collabSleep
	collabSleep = func(time.Duration) { t.Fatal("spawn slept on a trust menu") }
	t.Cleanup(func() { collabSleep = prev })
	for _, prompt := range []string{"", "Please investigate the repository"} {
		out := (&apiServer{}).spawnFollowUp(p, []string{"claude"}, map[string]any{"prompt": prompt})
		if out["status"] != "needs_user" || out["ready"] != false || !strings.HasPrefix(out["hint"].(string), folderTrustHint) {
			t.Fatalf("result %+v", out)
		}
		if prompt != "" && out["prompt_sent"] != false {
			t.Fatal("prompt must be retained by caller, not sent into menu")
		}
	}
}

func TestFolderTrustRecognitionRejectsProseAndHistoricalMenus(t *testing.T) {
	for _, text := range []string{
		"Do you trust this folder?",
		"No, exit\nYes, I trust this folder\nEnter to confirm",
		deliveryClaudeScreen("Do you trust this folder?"),
		claudeFolderTrustMenu + deliveryClaudeScreen(""),
	} {
		if len(folderTrustPromptMatches(text)) != 0 {
			t.Fatalf("false trust menu: %q", text)
		}
	}
}
