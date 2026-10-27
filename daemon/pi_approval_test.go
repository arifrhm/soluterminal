package main

import (
	"strings"
	"testing"
)

// piApprovalMenuFixture is the EXACT text daemon/harness_assets/pi-approval.mjs
// renders via ctx.ui.custom(). It is duplicated here on purpose: this test is
// the cross-language contract. SoluTerminal's per-pane AutoMode only works for
// a managed Pi pane if this text is classified as an approval prompt, so a
// reword in the extension's menu must fail here.
const piApprovalMenuFixture = `Tool approval: bash
❯ 1. Yes, allow once
  2. No, reject
  3. Always allow bash
Enter to approve · Esc to cancel`

func TestPiApprovalMenuIsAutoApprovable(t *testing.T) {
	if !isGridChoicePromptText(piApprovalMenuFixture, false) {
		t.Fatal("Pi approval menu is not a grid choice prompt")
	}
	if matches := approvalPromptMatches(piApprovalMenuFixture, false); len(matches) == 0 {
		t.Fatal("approvalPromptMatches missed the Pi approval menu")
	}
	if !isAutoApprovalPromptText(piApprovalMenuFixture, false) {
		t.Fatal("isAutoApprovalPromptText rejected the Pi approval menu; AutoMode would not fire")
	}
}

func TestPiApprovalMenuSurvivesTranscriptChrome(t *testing.T) {
	// The menu renders in place of the editor, below whatever the agent already
	// printed, and the daemon scans the visible grid/tail.
	screen := "● Running bash command\n\nsome tool output\n\n" + piApprovalMenuFixture + "\n\n▸ "
	if !isAutoApprovalPromptText(screen, false) {
		t.Fatal("Pi approval menu was not detected below transcript output")
	}
}

func TestManagedPiCmdlineMarker(t *testing.T) {
	managed := "node /home/u/.local/share/soluterminal/harnesses/pi/node_modules/@earendil-works/pi-coding-agent/dist/bundle/cli.js " +
		"--provider deepseek --model deepseek-flash --extension /home/u/.solu/harness-launchers/pi-approval.mjs"
	if !isManagedPiCmdline(managed) {
		t.Fatal("managed Pi argv was not recognized")
	}
	for _, unmanaged := range []string{
		"",
		"node /usr/bin/some-server --port 3000",
		"node /home/u/.local/share/soluterminal/harnesses/pi/node_modules/@earendil-works/pi-coding-agent/dist/bundle/cli.js --provider deepseek",
		"node /home/u/.solu/harness-launchers/launch.mjs pi --data-dir /home/u/.solu",
	} {
		if isManagedPiCmdline(unmanaged) {
			t.Fatalf("unmanaged argv %q was misclassified as managed Pi", unmanaged)
		}
	}
}

func TestPiApprovalMenuDoesNotMatchPiSelectors(t *testing.T) {
	// Pi's own ExtensionSelector (arrow prefix, keybinding-hint footer) is a
	// generic menu SoluTerminal must leave to the user; only our custom
	// component's numbered shape is auto-approvable.
	piSelector := `Trust project?
→ Trust
  Trust (this session only)
  Do not trust
↑↓ navigate  Enter select  Esc cancel`
	if isAutoApprovalPromptText(piSelector, false) {
		t.Fatal("Pi's built-in selector was misclassified as an approval prompt")
	}
	if strings.Contains(strings.ToLower(piSelector), "❯ 1.") {
		t.Fatal("fixture unexpectedly contains the daemon's approval marker")
	}
}
