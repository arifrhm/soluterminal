package main

import (
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"soluterminal/grid"
)

const codexMcpPermissionObservedPrompt = `Field 1/1
Allow the soluterminal MCP server to run tool
"get_workflow_task"?

task_id: wft_cabe0a63

› 1. Allow                   Run the tool and continue.
  2. Allow for this session  Run the tool and remember this
                             choice for this session.
  3. Always allow            Run the tool and remember this
                             choice for future tool calls.
  4. Cancel                  Cancel this tool call
enter to submit | esc to cancel`

func codexMcpPermissionPromptFor(tool, taskID string) string {
	prompt := strings.Replace(codexMcpPermissionObservedPrompt,
		`"get_workflow_task"`, `"`+tool+`"`, 1)
	return strings.Replace(prompt, "wft_cabe0a63", taskID, 1)
}

func TestLooksLikeClaudePrompt(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		in   string
		want bool
	}{
		{
			name: "claude v4.7+ prompt (real-world tail sample)",
			in: "●hi\n" +
				"─────────────────────────────────────────\n" +
				"❯                                         \n" +
				"─────────────────────────────────────────\n" +
				"  ~/wsl-projects/unified-inbox [Opus 4.7 (1M context) | ctx: 3% | unified-…]\n",
			want: true,
		},
		{
			name: "claude v4.7 truncated context (window-narrow)",
			in: "❯                                          \n" +
				"─────────────────────────────────\n" +
				"  ~/proj [Opus 4.7 (1M context) …\n",
			want: true,
		},
		{
			name: "claude v4.7 with typed user input still visible",
			in: "❯ hello there\n" +
				"─────────────────\n" +
				"~/proj [Sonnet 4.6]\n",
			want: true,
		},
		{
			name: "CR-only line endings (spinner overwrites)",
			// stripANSI should have normalized \r to \n before we ever
			// see it, but run through it explicitly to mirror the path.
			in: stripANSI([]byte(
				"\r●hi                   \r✻ Tinkeri…\r\r" +
					"─────────────────────\r\r" +
					"~/proj [Opus 4.7 …\r\r" +
					"❯                    \r\r" +
					"─────────────────────\r",
			)),
			want: true,
		},
		{
			name: "legacy box drawing prompt",
			in: "some content above\n" +
				"╭──────────────────╮\n" +
				"│ >                │\n" +
				"╰──────────────────╯\n" +
				"  ? for shortcuts\n",
			want: true,
		},
		{
			name: "lone ❯ without context footer is not enough",
			in:   "❯\nno context here\njust some text",
			want: false,
		},
		{
			name: "context footer without ❯ is not enough (e.g. help output)",
			in:   "somewhere | ctx: 99% | foo\nbut no prompt char",
			want: false,
		},
		{
			name: "partial legacy box — missing bottom",
			in:   "│ >                │\nno bottom yet",
			want: false,
		},
		{
			name: "random shell output",
			in:   "bash> some command\n$ echo hi",
			want: false,
		},
		{
			name: "empty",
			in:   "",
			want: false,
		},
	}
	for _, c := range cases {
		got := looksLikeClaudePrompt(c.in)
		if got != c.want {
			t.Errorf("%s: got %v want %v", c.name, got, c.want)
		}
	}
}

func TestChoicePromptRegex(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		in   string
		want bool
	}{
		{"ascii spaces", "❯ 1. Yes, trust\n  2. No, exit", true},
		{"indented with ascii", "  ❯ 1. Option one\n  2. Option two", true},
		{"NBSP around arrow", "\u00a0❯\u00a01. Yes\n\u00a02. No", true},
		{"real claude ws approval prompt", " ❯ 1. Yes\n   2. Yes, allow all edits\n   3. No", true},
		{"current codex exec approval prompt", "› 1. Yes, proceed (y)\n  2. Yes, and don't ask again for commands that start with `go test` (p)\n  3. No, and tell Codex what to do differently (esc)", true},
		{"current codex patch approval prompt", "  Would you like to make the following edits?\n\n› 1. Yes, proceed (y)\n  2. No, and tell Codex what to do differently (esc)\n\n  Press enter to confirm or esc to cancel", true},
		{"claude optional feedback prompt is not approval", "●   How is Claude doing this session? (optional)\n  1 : Bad    2 : Fine   3 : Good   0 : Dismiss", false},
		{"codex approval without option row is codex-only", "Would you like to run the following command?\n\nReason: Push release.\n\n$ git push origin HEAD:main", false},
		{"ansi-collapsed arrow-digit (no space)", "❯1.Yes\n2.No", true},
		{"ansi-collapsed with leading whitespace", "  ❯1.Yes", true},
		{"not anchored", "just a ❯ marker in text with 1. list", false},
		{"nothing", "some output", false},
		{"missing arrow", "1. first\n2. second", false},
		{"arrow without 1.", "❯\nhello", false},
		{"period dropped, menu padding intact", "   ❯  1  Yes\n  2.  No", true},
		{"period dropped, single-space prose stays unmatched", "❯ 1 thing", false},
		{"claude idle composer + footer hint is not a menu", claudeIdleFooterFixture, false},
		{"real approval menu above the same idle hint still matches", claudeApprovalAboveIdleFooterFixture, true},
	}
	for _, c := range cases {
		got := isChoicePromptText(c.in)
		if got != c.want {
			t.Errorf("%s: got %v want %v", c.name, got, c.want)
		}
	}
}

// Captured from pane_a2e1a65282c20cbf and pane_3b4e69a4f3fc6b2c via
// GET /api/panes/{id}/debug-tail on 2026-09-07. The false choice-prompt
// classification originally burned the auto-fire cap on incident pane
// pane_eab1cef2ed51f934 on 2026-08-25.
const claudeIdleFooterFixture = "  ❯  1. why is soluterminal's build 10 jobs per push. to double\n" +
	"──────\n" +
	"  ~/wsl-projects/soluterminal [Opus 4.7 (1M context) | ctx: 67% | main …]\n" +
	"  ⏸ manual mode on · 1 monitor\n" +
	"            new task? /clear to save 671.6k tokens\n"

const claudeApprovalAboveIdleFooterFixture = "Do you want to proceed?\n" +
	"❯ 1. Yes\n" +
	"  2. Yes, and don't ask again for git commit commands in ~/wsl-projects/soluterminal\n" +
	"  3. No, and tell Claude what to do differently (esc)\n" +
	claudeIdleFooterFixture

func TestComputeStateIgnoresClaudeIdleFooterHint(t *testing.T) {
	t.Parallel()

	newPane := func(id, text string) *Pane {
		t.Helper()
		p := &Pane{
			ID:             id,
			Status:         PaneRunning,
			LastActivityMs: time.Now().Add(-5 * time.Second).UnixMilli(),
			buffer:         NewRingBuffer(1 << 16),
			subs:           map[int64]*subEntry{},
		}
		if _, err := p.buffer.Write([]byte(text)); err != nil {
			t.Fatalf("buffer.Write: %v", err)
		}
		atomic.StoreInt64(&p.TotalStrippedChars, int64(len(stripANSI([]byte(text)))))
		return p
	}

	idle := newPane("pane-claude-idle-footer", claudeIdleFooterFixture)
	if got := idle.computeState(); got != StateAwaitingInput {
		t.Fatalf("idle footer computeState = %q, want %q", got, StateAwaitingInput)
	}
	if got := choicePromptFingerprint(claudeIdleFooterFixture); got != 0 {
		t.Fatalf("choicePromptFingerprint(idle footer) = %d, want 0", got)
	}
	if got := approvalPromptFingerprint(claudeIdleFooterFixture, false); got != 0 {
		t.Fatalf("approvalPromptFingerprint(idle footer) = %d, want 0", got)
	}

	approval := newPane("pane-claude-approval-above-idle-footer", claudeApprovalAboveIdleFooterFixture)
	if got := approval.computeState(); got != StateChoicePrompt {
		t.Fatalf("approval menu computeState = %q, want %q", got, StateChoicePrompt)
	}
	if !isAutoApprovalPromptText(claudeApprovalAboveIdleFooterFixture, false) {
		t.Fatal("real approval menu above idle footer did not remain auto-approvable")
	}
}

func TestChoicePromptFingerprint(t *testing.T) {
	t.Parallel()
	a := "noise\n› 1. Yes, proceed (y)\n  2. No\n"
	b := "noise changed\n› 1. Yes, proceed (y)\n  2. No\n"
	c := "noise\n› 1. Yes, proceed (y)\n  2. No\n  3. More\n"

	fpA := choicePromptFingerprint(a)
	if fpA == 0 {
		t.Fatal("choicePromptFingerprint returned 0 for prompt")
	}
	if got := choicePromptFingerprint("no prompt here"); got != 0 {
		t.Fatalf("choicePromptFingerprint without prompt = %d, want 0", got)
	}
	if got := choicePromptFingerprint(b); got != fpA {
		t.Fatalf("fingerprint included pre-prompt noise: got %d want %d", got, fpA)
	}
	if got := choicePromptFingerprint(c); got == fpA {
		t.Fatal("fingerprint did not change when prompt choices changed")
	}
	feedback := "noise\n●   How is Claude doing this session? (optional)\n  1 : Bad    2 : Fine   3 : Good   0 : Dismiss\n"
	if got := choicePromptFingerprint(feedback); got != 0 {
		t.Fatalf("choicePromptFingerprint for feedback prompt = %d, want 0", got)
	}
	codexNoOption := "Would you like to run the following command?\n\nReason: Push release.\n\n$ git push origin HEAD:main"
	if got := choicePromptFingerprint(codexNoOption); got != 0 {
		t.Fatalf("choicePromptFingerprint for Codex approval without option row = %d, want 0", got)
	}
	if got := approvalPromptFingerprint(codexNoOption, false); got != 0 {
		t.Fatalf("approvalPromptFingerprint without Codex = %d, want 0", got)
	}
	if got := approvalPromptFingerprint(codexNoOption, true); got == 0 {
		t.Fatal("approvalPromptFingerprint returned 0 for Codex approval without option row")
	}
	if !isCodexApprovalPromptText(codexNoOption) {
		t.Fatal("isCodexApprovalPromptText returned false for Codex command approval")
	}
	if isCodexApprovalPromptText(feedback) {
		t.Fatal("isCodexApprovalPromptText returned true for Claude feedback prompt")
	}
}

// TestChoicePromptFingerprintIgnoresSpinner pins that the post-match
// status footer (Codex's "Working…" spinner, bullet-cursor residue,
// blank chrome) does NOT enter the fingerprint. Without trimming, each
// spinner frame produced a fresh hash and tripped the
// choicePromptIdentityMatches recheck in aria_auto.go, which surfaced
// as "pane prompt changed before Enter" loops that backed ARIA off
// after 3 retries on long-running Codex sessions.
func TestChoicePromptFingerprintIgnoresSpinner(t *testing.T) {
	t.Parallel()
	base := "› 1. Yes, proceed (y)\n  2. No, and tell Codex what to do differently (esc)\n"
	frame1 := base + "\n  •    •    •\n  Working\n"
	frame2 := base + "\n  •  •  •  •\n  Working\n"
	frame3 := base + "\n      •         •\n  Working...\n"

	fp1 := approvalPromptFingerprint(frame1, true)
	if fp1 == 0 {
		t.Fatal("approvalPromptFingerprint returned 0 for menu+spinner")
	}
	if got := approvalPromptFingerprint(frame2, true); got != fp1 {
		t.Fatalf("spinner frame change altered fingerprint: %d vs %d", got, fp1)
	}
	if got := approvalPromptFingerprint(frame3, true); got != fp1 {
		t.Fatalf("spinner frame change altered fingerprint: %d vs %d", got, fp1)
	}

	// Sanity: a real menu change still flips the hash (the trim must
	// not be so aggressive that it eats the menu itself).
	changed := "› 1. Yes, proceed (y)\n  2. Yes and don't ask again\n  3. No\n\n  Working\n"
	if got := approvalPromptFingerprint(changed, true); got == fp1 {
		t.Fatal("fingerprint unchanged across genuine menu change")
	}
}

func TestCodexMcpPermissionPromptIdentity(t *testing.T) {
	t.Parallel()
	base := approvalPromptFingerprint(codexMcpPermissionObservedPrompt, true)
	if base == 0 {
		t.Fatal("approvalPromptFingerprint returned 0 for Codex MCP permission prompt")
	}

	frame1 := codexMcpPermissionObservedPrompt + "\n  •  •  •\nWorking…\n"
	frame2 := codexMcpPermissionObservedPrompt + "\n✶ Working (2m 03s · esc to interrupt)\n"
	for i, frame := range []string{frame1, frame2} {
		if got := approvalPromptFingerprint(frame, true); got != base {
			t.Fatalf("animated frame %d altered fingerprint: %d vs %d", i+1, got, base)
		}
	}

	padded := strings.ReplaceAll(codexMcpPermissionObservedPrompt,
		"› 1. Allow                   Run", "› 1. Allow          Run")
	padded = strings.ReplaceAll(padded,
		"  2. Allow for this session  Run", "  2. Allow for this session        Run")
	padded = strings.ReplaceAll(padded,
		"  3. Always allow            Run", "  3. Always allow     Run")
	padded = strings.ReplaceAll(padded,
		"  4. Cancel                  Cancel", "  4. Cancel       Cancel")
	if got := approvalPromptFingerprint(padded, true); got != base {
		t.Fatalf("option-column padding altered fingerprint: %d vs %d", got, base)
	}

	withoutCancel := strings.ReplaceAll(codexMcpPermissionObservedPrompt,
		"  4. Cancel                  Cancel this tool call\n", "")
	if got := approvalPromptFingerprint(withoutCancel, true); got == base {
		t.Fatal("fingerprint unchanged after dropping the Cancel option")
	}
	if got := approvalPromptFingerprint(codexMcpPermissionObservedPrompt, false); got == 0 {
		t.Fatal("numbered MCP menu fingerprint unexpectedly depends on hasCodex")
	}

	createPrompt := codexMcpPermissionPromptFor("create_workflow_task", "wft_cabe0a63")
	if got := approvalPromptFingerprint(createPrompt, true); got == base {
		t.Fatal("different MCP tool with identical option rows shared a fingerprint")
	}

	differentTask := codexMcpPermissionPromptFor("get_workflow_task", "wft_f0a87099")
	if got := approvalPromptFingerprint(differentTask, true); got == base {
		t.Fatal("same MCP tool with a different task_id shared a fingerprint")
	}

	narrow := strings.Replace(codexMcpPermissionObservedPrompt,
		"Allow the soluterminal MCP server to run tool\n\"get_workflow_task\"?",
		"Allow the soluterminal MCP server to run\ntool \"get_workflow_task\"?", 1)
	narrow = strings.Replace(narrow, "task_id: wft_cabe0a63", "task_id:\nwft_cabe0a63", 1)
	if got := approvalPromptFingerprint(narrow, true); got != base {
		t.Fatalf("narrow-terminal wrapping altered fingerprint: %d vs %d", got, base)
	}

	interleavedTimer := strings.Replace(codexMcpPermissionObservedPrompt,
		"\n\ntask_id:",
		"\n\nWaiting for background terminal · (1m 44s · esc to interrupt)\ntask_id:", 1)
	if interleavedTimer == codexMcpPermissionObservedPrompt {
		t.Fatal("timer fixture replacement did not change the prompt")
	}
	if got := approvalPromptFingerprint(interleavedTimer, true); got != base {
		t.Fatalf("interleaved timer entered MCP identity token: %d vs %d", got, base)
	}

	// A later non-MCP Codex command modal must have the same identity in a long
	// adaptive tail (where the prior MCP modal remains in scrollback) and in the
	// current 24-row grid (where it has scrolled off). Otherwise watcher capture
	// and directAutoFire's just-before-Enter recheck can disagree and abort.
	commandPrompt := "Would you like to run the following command?\n\nReason: Run tests.\n\n$ go test ./daemon"
	commandBase := approvalPromptFingerprint(commandPrompt, true)
	if commandBase == 0 {
		t.Fatal("approvalPromptFingerprint returned 0 for Codex command prompt")
	}
	staleMCPQuestion := strings.Split(codexMcpPermissionObservedPrompt, "› 1. Allow")[0]
	nearbyTailCommand := staleMCPQuestion + commandPrompt
	if got := approvalPromptFingerprint(nearbyTailCommand, true); got != commandBase {
		t.Fatalf("nearby stale MCP question contaminated later command identity: %d vs %d", got, commandBase)
	}
	longTailCommand := staleMCPQuestion +
		strings.Repeat("completed background output remains in scrollback\n", 30) +
		commandPrompt
	if got := approvalPromptFingerprint(longTailCommand, true); got != commandBase {
		t.Fatalf("stale MCP question contaminated later command identity: %d vs %d", got, commandBase)
	}

	// The adaptive tail and reconstructed grid can differ in leading transcript
	// context and option-column padding. They must still agree so watcher
	// admission and directAutoFire's just-before-Enter recheck share identity.
	tailShaped := "older transcript output\n\n" + padded
	if got := approvalPromptFingerprint(tailShaped, true); got != base {
		t.Fatalf("grid/adaptive-tail renderings disagreed on fingerprint: %d vs %d", got, base)
	}
}

// TestApprovalPromptFingerprintIgnoresTimerLines pins that
// alphanumeric-bearing animated status footers (Codex's "Waiting for
// background terminal · (1m 45s · esc to interrupt)" and similar) do
// NOT enter the fingerprint. trimTrailingStatusJunk preserves these
// lines because they pass the alnum + length>=20 heuristic, so without
// further filtering they animated the hash every second and trapped
// pane_356b3aa48e4123dc in a "pane prompt changed before Enter" loop
// for 3 retries until ARIA backed off (2026-05-07).
func TestApprovalPromptFingerprintIgnoresTimerLines(t *testing.T) {
	t.Parallel()
	base := "› 1. Yes, proceed (y)\n  2. No, and tell Codex what to do differently (esc)\n"
	frame1 := base + "\n  Waiting for background terminal · (1m 44s · esc to interrupt) · 1 background terminal running\n"
	frame2 := base + "\n  Waiting for background terminal · (1m 45s · esc to interrupt) · 1 background terminal running\n"
	frame3 := base + "\n  ✶ Working (2m 03s · esc to interrupt)\n"

	fp1 := approvalPromptFingerprint(frame1, true)
	if fp1 == 0 {
		t.Fatal("approvalPromptFingerprint returned 0 for menu+timer-line")
	}
	if got := approvalPromptFingerprint(frame2, true); got != fp1 {
		t.Fatalf("timer tick altered fingerprint: %d vs %d", got, fp1)
	}
	if got := approvalPromptFingerprint(frame3, true); got != fp1 {
		t.Fatalf("Working spinner timer altered fingerprint: %d vs %d", got, fp1)
	}

	// And the Codex command-approval modal: digit-bearing reason text
	// near the body must not flip the hash either, because ratatui can
	// re-emit the modal with shifted column padding while the command
	// itself is unchanged.
	cmdA := "Would you like to run the following command?\n\nReason: Push release v0.0.436.\n\n$ git push origin HEAD:main"
	cmdB := "Would you like to run the following command?\n\nReason: Push release v0.0.437.\n\n$ git push origin HEAD:main"
	hA := approvalPromptFingerprint(cmdA, true)
	if hA == 0 {
		t.Fatal("zero hash for codex command approval")
	}
	if hB := approvalPromptFingerprint(cmdB, true); hA != hB {
		t.Fatalf("reason-line digit change altered fingerprint: %d vs %d", hB, hA)
	}

	// Sanity: a different command still flips the hash.
	cmdC := "Would you like to run the following command?\n\nReason: Push release.\n\n$ rm -rf /tmp/release"
	if hC := approvalPromptFingerprint(cmdC, true); hA == hC {
		t.Fatal("fingerprint unchanged across genuine command change")
	}
}

func TestApprovalPromptMatchesCodexOnly(t *testing.T) {
	t.Parallel()
	codexNoOption := "Would you like to run the following command?\n\nReason: Push release.\n\n$ git push origin HEAD:main"
	if got := approvalPromptMatches(codexNoOption, false); len(got) != 0 {
		t.Fatalf("approvalPromptMatches without Codex returned %d matches, want 0", len(got))
	}
	if got := approvalPromptMatches(codexNoOption, true); len(got) == 0 {
		t.Fatal("approvalPromptMatches with Codex returned no matches")
	}

	claudeFeedback := "●   How is Claude doing this session? (optional)\n  1 : Bad    2 : Fine   3 : Good   0 : Dismiss"
	if got := approvalPromptMatches(claudeFeedback, true); len(got) != 0 {
		t.Fatalf("approvalPromptMatches matched Claude feedback prompt: %d", len(got))
	}

	numberedApproval := "› 1. Yes, proceed (y)\n  2. No, and tell Codex what to do differently (esc)"
	if got := approvalPromptMatches(numberedApproval, false); len(got) == 0 {
		t.Fatal("approvalPromptMatches missed normal numbered approval")
	}
}

func TestAutoApprovalPromptText(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		in       string
		hasCodex bool
		want     bool
	}{
		{
			name: "claude edit approval",
			in: strings.Join([]string{
				"Edit file",
				"src/agent/loop.ts",
				"Do you want to make this edit to loop.ts?",
				"❯ 1. Yes",
				"  2. Yes, allow all edits during this session (shift+tab)",
				"  3. No",
				"Esc to cancel · Tab to amend",
			}, "\n"),
			want: true,
		},
		{
			name: "claude bash approval",
			in: strings.Join([]string{
				"Bash command",
				"echo hello",
				"Do you want to proceed?",
				"❯ 1. Yes",
				"  2. Yes, and don't ask again for similar commands",
				"  3. No",
				"Esc to cancel · Tab to amend",
			}, "\n"),
			want: true,
		},
		{
			name: "generic claude selection menu",
			in: strings.Join([]string{
				"How should I handle this?",
				"❯ 1. Fix it now",
				"  2. Explain the tradeoffs",
				"  3. Ask a sub-agent",
				"  4. Show details",
				"  5. Chat about this",
				"Enter to select · ↑/↓ to navigate · Esc to cancel",
			}, "\n"),
			want: false,
		},
		{
			name: "preference question",
			in: strings.Join([]string{
				"Which implementation approach do you prefer?",
				"❯ 1. Small patch",
				"  2. Larger refactor",
				"  3. Explain options first",
			}, "\n"),
			want: false,
		},
		{
			name:     "codex header approval",
			in:       "Would you like to run the following command?\n\n$ go test ./...",
			hasCodex: true,
			want:     true,
		},
		{
			// Plan-exit menu. Option 1 switches the session into auto-accept
			// edits, so a bare Enter here is a mode change, not a permission
			// grant. Never auto-answered (mirrors the native path's
			// ExitPlanMode block). This expectation used to be true.
			name: "plan-mode ready to execute stays excluded",
			in: strings.Join([]string{
				"Claude has written up a plan and is ready to execute. Would you like to proceed?",
				"❯ 1. Yes, auto-accept edits",
				"  2. Yes, manually approve edits",
				"  3. No, keep planning",
			}, "\n"),
			want: false,
		},
		{
			// Login/account decision (moves billing onto the API key), not
			// a tool permission. This expectation used to be true.
			name: "use this api key stays excluded",
			in: strings.Join([]string{
				"Do you want to use this API key?",
				"sk-ant-api03-redacted",
				"❯ 1. Yes",
				"  2. No",
			}, "\n"),
			want: false,
		},
		{
			// Guard: a preference menu that happens to say "proceed" but
			// renders the navigate footer must STILL be excluded — the
			// genericMenuNeedles gate runs before the approval needles.
			name: "navigate-footer menu mentioning proceed stays excluded",
			in: strings.Join([]string{
				"Would you like to proceed with the refactor or keep the patch?",
				"❯ 1. Refactor",
				"  2. Keep patch",
				"Enter to select · ↑/↓ to navigate · Esc to cancel",
			}, "\n"),
			want: false,
		},
		{
			// Writing a file Claude Code treats as its own settings swaps the
			// usual "Yes, allow all edits during this session" option for
			// "Yes, AND allow …" — breaking the "yes, allow" needle on one
			// word. No "ask again" option either, and "do you want to create"
			// matches no phrasing, so always-mode panes parked on this.
			// Matched now via the numbered "Yes, <qualifier>" option row.
			name: "settings-file write approval",
			in: strings.Join([]string{
				"Do you want to create capture.json?",
				"❯ 1. Yes",
				"  2. Yes, and allow Claude to edit its own settings for this session",
				"  3. No",
				"Esc to cancel · Tab to amend",
			}, "\n"),
			want: true,
		},
		{
			name: "bypass-permissions approval",
			in: strings.Join([]string{
				"Do you want to run this command?",
				"❯ 1. Yes",
				"  2. Yes, and bypass permissions",
				"  3. No",
			}, "\n"),
			want: true,
		},
		{
			name: "trust-directory approval",
			in: strings.Join([]string{
				"Do you trust this directory?",
				"❯ 1. Yes",
				"  2. Yes, and remember this directory",
				"  3. No",
			}, "\n"),
			want: true,
		},
		{
			// Safety boundary: AskUserQuestion option labels are MODEL-authored,
			// so one can legitimately read "Yes, <direction>". Those are opinion
			// menus and must never be auto-answered. They always render the
			// selection footer, which genericMenuNeedles rejects before the
			// "Yes, …" option row is ever consulted. Pin that ordering here.
			name: "model-authored yes-option in selection menu stays excluded",
			in: strings.Join([]string{
				"Which database should we use?",
				"❯ 1. Yes, use Postgres",
				"  2. No, stick with SQLite",
				"↑/↓ to navigate · Enter to select · Esc to cancel",
			}, "\n"),
			want: false,
		},
		{
			// Exact live grid from Codex v0.0.985: this is a permission grant,
			// despite using Codex's newer Allow vocabulary and submit footer.
			name:     "codex mcp permission modal (exact observed)",
			in:       codexMcpPermissionObservedPrompt,
			hasCodex: true,
			want:     true,
		},
		{
			// Narrow terminals wrap the question before both "server" and the
			// quoted tool name; flattening whitespace preserves its structure.
			name: "codex mcp permission modal (narrower wrap)",
			in: strings.Join([]string{
				"Field 1/1",
				"Allow the soluterminal MCP",
				"server to run tool",
				"\"create_workflow_task\"?",
				"",
				"› 1. Allow  Run the tool and continue.",
				"  2. Allow for this session  Run the tool and remember this",
				"     choice for this session.",
				"  3. Always allow  Run the tool and remember this choice",
				"     for future tool calls.",
				"  4. Cancel  Cancel this tool call",
				"enter to submit | esc to cancel",
			}, "\n"),
			hasCodex: true,
			want:     true,
		},
		{
			// ANSI cursor movement becomes wide space runs after stripping;
			// Fields-based flattening treats those runs like ordinary padding.
			name: "codex mcp permission modal (ANSI-strip padding)",
			in: strings.Join([]string{
				"Field 1/1",
				"Allow   the    soluterminal   MCP   server   to   run   tool",
				"\"get_workflow_task\"?",
				"",
				"›   1.      Allow                   Run the tool and continue.",
				"  2.   Allow for this session       Run the tool and remember this",
				"  3.   Always allow                 Run the tool and remember this",
				"  4.   Cancel                       Cancel this tool call",
				"enter   to   submit | esc   to   cancel",
			}, "\n"),
			hasCodex: true,
			want:     true,
		},
		{
			// The structural classifier is Codex-only; identical text in any
			// other pane remains an ordinary choice menu.
			name:     "codex mcp permission modal without codex descendant",
			in:       codexMcpPermissionObservedPrompt,
			hasCodex: false,
			want:     false,
		},
		{
			// A question without numbered choices is an incomplete redraw, not
			// a safely focused approval action.
			name: "codex mcp permission question without option rows",
			in: strings.Join([]string{
				"Allow the soluterminal MCP server to run tool \"get_workflow_task\"?",
				"Allow",
				"Allow for this session",
				"Always allow",
				"Cancel",
			}, "\n"),
			hasCodex: true,
			want:     false,
		},
		{
			// Permission-looking rows alone are insufficient without the
			// narrowly identified MCP-server question.
			name: "codex allow rows without mcp permission question",
			in: strings.Join([]string{
				"Allow this tool?",
				"› 1. Allow",
				"  2. Allow for this session",
				"  3. Always allow",
				"  4. Cancel",
			}, "\n"),
			hasCodex: true,
			want:     false,
		},
		{
			// A partial render with no explicit cancel path must never fire.
			name: "codex mcp permission modal without cancel path",
			in: strings.Join([]string{
				"Allow the soluterminal MCP server to run tool \"get_workflow_task\"?",
				"› 1. Allow",
				"  2. Allow for this session",
				"  3. Always allow",
				"enter to submit",
			}, "\n"),
			hasCodex: true,
			want:     false,
		},
		{
			// Model-authored policy wording is still a generic selection menu;
			// "always allow" alone is deliberately not an approval signal.
			name: "generic logging menu with always allow on codex pane",
			in: strings.Join([]string{
				"Which logging policy?",
				"❯ 1. Allow verbose logs",
				"  2. Always allow telemetry",
				"  3. Never",
				"↑/↓ to navigate · Enter to select",
			}, "\n"),
			hasCodex: true,
			want:     false,
		},
		{
			// Similar permission-flavored prose without the MCP structure stays
			// governed by the pre-existing approval rules.
			name: "always allow future runs question is not codex mcp approval",
			in: strings.Join([]string{
				"Always allow future runs?",
				"❯ 1. Yes",
				"  2. No",
			}, "\n"),
			hasCodex: true,
			want:     false,
		},
		{
			// DeepSeek terminal approvals render this focused menu so
			// SoluTerminal's auto-approval can answer them with the same
			// bare-Enter keystroke it uses for Claude and Codex. Keep the
			// shape (❯ 1. + "Yes," option + a "No" escape) in step with
			// daemon/harness_assets/deepseek-terminal.mjs renderPermission.
			name: "deepseek terminal approval menu",
			in: strings.Join([]string{
				"Tool approval: Write a file",
				"{ \"path\": \"test.txt\" }",
				"Do you want to proceed?",
				"❯ 1. Yes, allow once",
				"  2. No, reject",
				"Select [1/2]:",
			}, "\n"),
			hasCodex: false,
			want:     true,
		},
	}
	for _, c := range cases {
		if got := isAutoApprovalPromptText(c.in, c.hasCodex); got != c.want {
			t.Fatalf("%s: isAutoApprovalPromptText = %v, want %v", c.name, got, c.want)
		}
	}
}

// TestApprovalScanTailRecoversBuriedMenu reproduces the multi-subagent
// burst that motivated widening directAutoFire's recheck window: a real,
// still-pending approval menu followed by many non-junk progress lines that
// push it past the 30-line tail. The narrow tail (what the recheck used to
// look at) misses the menu while computeState's wide scan window still finds
// it — exactly the admit-vs-confirm disagreement that parked panes.
func TestApprovalScanTailRecoversBuriedMenu(t *testing.T) {
	t.Parallel()
	p := &Pane{
		ID:     "pane-burst",
		Cols:   84,
		Rows:   31,
		Status: PaneRunning,
		buffer: NewRingBuffer(1 << 20),
		subs:   map[int64]*subEntry{},
	}
	var b strings.Builder
	b.WriteString(strings.Join([]string{
		"Bash command",
		"go test ./...",
		"Do you want to proceed?",
		"❯ 1. Yes",
		"  2. Yes, and don't ask again for similar commands",
		"  3. No",
		"Esc to cancel · Tab to amend",
		"",
	}, "\n"))
	// Background terminals from a multi-subagent run keep emitting while the
	// agent blocks on the menu. These survive trimTrailingStatusJunk (alnum,
	// long enough), so they bury the menu well past a 30-line window.
	for i := 0; i < 60; i++ {
		b.WriteString("subagent progress: scanning files and collecting results for analysis\n")
	}
	prompt := b.String()
	if _, err := p.buffer.Write([]byte(prompt)); err != nil {
		t.Fatalf("buffer.Write: %v", err)
	}
	atomic.StoreInt64(&p.TotalStrippedChars, int64(len(prompt)))

	// Precondition: the narrow tail no longer surfaces the menu (the burst
	// buried it and the progress lines defeat the adaptive expansion).
	if isAutoApprovalPromptText(tailOfPaneAdaptive(p, 30), false) {
		t.Fatalf("precondition failed: 30-line tail unexpectedly still sees the buried menu")
	}
	// The wide scan window recovers it, keeping the confirm gate in lockstep
	// with computeState's admit gate.
	if wide := approvalScanTail(p); !isAutoApprovalPromptText(wide, false) {
		t.Fatalf("approvalScanTail did not recover the buried approval menu; got:\n%s", wide)
	}
}

func TestGridChoicePromptTextCodexGate(t *testing.T) {
	t.Parallel()
	commandApproval := "Would you like to run the following command?\n\nReason: Push release.\n\n$ git push origin HEAD:main"
	if isGridChoicePromptText(commandApproval, false) {
		t.Fatal("isGridChoicePromptText treated header-only Codex command approval as generic choice prompt")
	}
	if !isGridChoicePromptText(commandApproval, true) {
		t.Fatal("isGridChoicePromptText missed Codex command approval")
	}

	editApproval := "Would you like to make the following edits?\n\nM daemon/state.go\n\nPress enter to confirm or Esc to cancel"
	if isGridChoicePromptText(editApproval, false) {
		t.Fatal("isGridChoicePromptText treated header-only Codex edit approval as generic choice prompt")
	}
	if !isGridChoicePromptText(editApproval, true) {
		t.Fatal("isGridChoicePromptText missed Codex edit approval")
	}

	claudeFeedback := "●   How is Claude doing this session? (optional)\n  1 : Bad    2 : Fine   3 : Good   0 : Dismiss"
	if isGridChoicePromptText(claudeFeedback, true) {
		t.Fatal("isGridChoicePromptText matched Claude feedback prompt")
	}

	otherFocusedMenu := "› 1. Open settings\n  2. Back"
	if !isGridChoicePromptText(otherFocusedMenu, false) {
		t.Fatal("documenting current behavior: focused numbered menus remain choice prompts")
	}
	if isAutoApprovalPromptText(otherFocusedMenu, false) {
		t.Fatal("generic focused numbered menu must not be auto-approvable")
	}
}

// TestComputeStateRecoversMenuBuriedUnderSpinner reproduces the
// 2026-05-02 codex limits/caps regression: a real `git add` approval
// menu followed by ~17 KB of bullet/whitespace spinner output ended up
// classified as StateBusy because (a) the byte-tail regex only saw the
// last 1200 chars of stripped output (all spinner) and (b) the codex
// grid fallback failed for unknown reasons. The fix in computeState
// trims trailing status-junk lines before the screenful crop, so the
// menu re-enters the regex window.
func TestComputeStateRecoversMenuBuriedUnderSpinner(t *testing.T) {
	t.Parallel()
	p := &Pane{
		ID:     "pane-test",
		Cols:   84,
		Rows:   31,
		Status: PaneRunning,
		buffer: NewRingBuffer(1 << 20), // 1 MB plenty for this scenario
		subs:   map[int64]*subEntry{},
	}

	// Real menu observed in the wild — exact strings from
	// pane_4aa85895… buffer dump.
	menu := strings.Join([]string{
		"  Would you like to run the following command?",
		"",
		"  Reason: Stage the verified pane find hook refactor.",
		"",
		"  $  git add web/src/components/Pane.tsx web/src/components/usePaneFind.ts",
		"",
		"› 1. Yes, proceed (y)",
		"  2. Yes, and don't ask again for commands that start with `git add` ( p )",
		"  3. No, and tell Codex what to do differently ( esc )",
		"",
		"  Press enter to confirm or esc to cancel",
		"",
	}, "\n") + "\n"

	// Simulate codex's spinner re-render pile-up: many lines of
	// bullet/whitespace produced by ANSI-stripping cursor-position
	// escape runs. ~17 KB matches what we observed live.
	var spinner strings.Builder
	for i := 0; i < 200; i++ {
		spinner.WriteString(strings.Repeat(" ", 20))
		spinner.WriteString("•")
		spinner.WriteString(strings.Repeat(" ", 60))
		spinner.WriteString("•")
		spinner.WriteString(strings.Repeat(" ", 20))
		spinner.WriteByte('\n')
	}
	if spinner.Len() < 17000 {
		t.Fatalf("spinner setup too short: %d", spinner.Len())
	}

	combined := menu + spinner.String()
	if _, err := p.buffer.Write([]byte(combined)); err != nil {
		t.Fatalf("buffer.Write: %v", err)
	}
	atomic.StoreInt64(&p.TotalStrippedChars, int64(len(combined)))

	got := p.computeState()
	if got != StateChoicePrompt {
		// Surface the symptom for debugging: where does
		// trimTrailingStatusJunk land, and does the regex match the
		// recovered tail?
		s := stripANSI([]byte(combined))
		trimmedEnd := trimTrailingStatusJunk(s)
		recovered := s[:trimmedEnd]
		if len(recovered) > 600 {
			recovered = recovered[len(recovered)-600:]
		}
		t.Fatalf("computeState = %q, want %q.\nrecovered tail (last 600 chars):\n%s",
			got, StateChoicePrompt, recovered)
	}
}

// Unlike the spinner case above (trimTrailingStatusJunk strips the
// trailing junk), a multi-subagent run buries the menu under REAL
// progress lines that survive the trim. Those pushed the menu past the
// old 1200-char scan window so auto-approve missed it; the wide
// approvalScanChars window must still recover it.
func TestComputeStateRecoversMenuBuriedUnderNonJunkProgress(t *testing.T) {
	t.Parallel()
	p := &Pane{
		ID:     "pane-test",
		Cols:   84,
		Rows:   31,
		Status: PaneRunning,
		buffer: NewRingBuffer(1 << 20),
		subs:   map[int64]*subEntry{},
	}

	// Canonical Claude approval menu (matches choicePromptMatches even
	// with hasCodex=false).
	menu := strings.Join([]string{
		"Do you want to proceed?",
		" ❯ 1. Yes",
		"   2. Yes, allow reading from lib/ during this session",
		"   3. No",
	}, "\n") + "\n"

	// A background terminal keeps streaming AFTER the menu is up. These
	// are genuine progress lines (alnum, len>=20), so trimTrailingStatusJunk
	// does NOT trim them — they really push the menu back. ~5 KB comfortably
	// exceeds the old 1200-char window while staying within approvalScanChars.
	var progress strings.Builder
	for i := 0; i < 60; i++ {
		progress.WriteString("  Read file /home/user/wsl-projects/aria/desktop/src/lib/schedule.ts (lines 741-800)\n")
	}
	if progress.Len() < 1200 {
		t.Fatalf("progress setup too short to exercise the wide window: %d", progress.Len())
	}

	combined := menu + progress.String()
	if _, err := p.buffer.Write([]byte(combined)); err != nil {
		t.Fatalf("buffer.Write: %v", err)
	}
	atomic.StoreInt64(&p.TotalStrippedChars, int64(len(combined)))

	if got := p.computeState(); got != StateChoicePrompt {
		t.Fatalf("computeState = %q, want %q (menu buried %d chars behind non-junk progress should still be found by the wide approval window)",
			got, StateChoicePrompt, progress.Len())
	}
}

func TestComputeStateDoesNotTreatClaudeFeedbackAsChoicePrompt(t *testing.T) {
	t.Parallel()
	p := &Pane{
		ID:     "pane-feedback",
		Cols:   84,
		Rows:   31,
		Status: PaneRunning,
		buffer: NewRingBuffer(1 << 20),
		subs:   map[int64]*subEntry{},
	}
	prompt := strings.Join([]string{
		"●   How is Claude doing this session? (optional)",
		"  1 : Bad    2 : Fine   3 : Good   0 : Dismiss",
		"",
		"❯",
		"────────────────────────────────────────────────────────────────────────────────────",
		"  ~/wsl-projects/soluterminal [Opus 4.7 (1M context) | ctx: 6% | soluterminal]",
	}, "\n") + "\n"
	if _, err := p.buffer.Write([]byte(prompt)); err != nil {
		t.Fatalf("buffer.Write: %v", err)
	}
	atomic.StoreInt64(&p.TotalStrippedChars, int64(len(prompt)))

	if got := p.computeState(); got != StateAwaitingInput {
		t.Fatalf("computeState = %q, want %q", got, StateAwaitingInput)
	}
}

func TestStripANSI(t *testing.T) {
	t.Parallel()
	// CSI/OSC replacements now insert a SPACE rather than dropping to
	// empty — see stripANSI's comment for why (Claude Code positions
	// text with cursor-move escapes instead of literal spaces, so
	// dropping to empty concatenated every word in prompts). Expected
	// outputs here reflect the space-preserving behavior.
	cases := []struct {
		name string
		in   []byte
		want string
	}{
		{
			name: "CSI colors",
			in:   []byte("\x1b[38;5;6m│\x1b[0m > \x1b[38;5;6m│\x1b[0m"),
			want: " │  >  │ ",
		},
		{
			name: "OSC title (BEL-terminated)",
			in:   []byte("before\x1b]0;window title\x07after"),
			want: "before after",
		},
		{
			name: "OSC title (ST-terminated)",
			in:   []byte("before\x1b]0;window title\x1b\\after"),
			want: "before after",
		},
		{
			name: "mixed CSI and OSC",
			in:   []byte("\x1b]0;solu\x07\x1b[31mred\x1b[0m"),
			want: "  red ",
		},
		{
			name: "cursor-move between words (the bug this fix targets)",
			in:   []byte("\x1b[32mApp\x1b[5Crunning\x1b[9Cat\x1b[0m"),
			want: " App running at ",
		},
	}
	for _, c := range cases {
		got := stripANSI(c.in)
		if got != c.want {
			t.Errorf("%s: got %q want %q", c.name, got, c.want)
		}
	}
}

// seedGridMenu wires up a grid emulator on p and feeds it `text` so
// gridVisibleText() reconstructs it. The raw byte buffer is left holding
// only non-menu residue, so computeState's raw-buffer scan does NOT match
// and execution reaches the grid-emulator branch under test.
func gridSynchronizedFrame(text string) string {
	return "\x1b[?2026h\x1b[2J\x1b[H" +
		strings.ReplaceAll(text, "\n", "\r\n") + "\x1b[?2026l"
}

func seedGridMenu(t *testing.T, p *Pane, text string) {
	t.Helper()
	p.ensureGrid()
	p.subsMu.Lock()
	// Clear and paint inside one ratatui synchronized-output frame. The live
	// feed observer sees only the committed screen, never the mid-repaint clear.
	p.feedGridPayloadLocked([]byte(gridSynchronizedFrame(text)), true)
	_ = p.gridEmu.DrainReplies()
	p.subsMu.Unlock()
}

// TestComputeStateGridChoicePromptInputGuard pins the grid-branch
// "user has answered" guard (P0 Fix C): a grid-reconstructed choice prompt
// must fire once, must stop firing after the user answers it (same
// fingerprint), and must fire again for a genuinely new (different
// fingerprint) menu. Mirrors the raw-buffer branch's inputPos/ackedPos
// semantics.
func TestComputeStateGridChoicePromptInputGuard(t *testing.T) {
	t.Parallel()
	p := &Pane{
		ID:     "pane-grid-guard",
		Cols:   80,
		Rows:   24,
		Status: PaneRunning,
		buffer: NewRingBuffer(1 << 16),
		subs:   map[int64]*subEntry{},
		done:   make(chan struct{}),
	}
	defer close(p.done)
	defer p.shutdownGrid()

	// Raw byte tail: degraded cursor-residue with NO matchable menu, so the
	// raw-buffer scan can't short-circuit before the grid branch.
	raw := strings.Repeat("   •      \n", 12)
	if _, err := p.buffer.Write([]byte(raw)); err != nil {
		t.Fatalf("buffer.Write: %v", err)
	}
	atomic.StoreInt64(&p.TotalStrippedChars, int64(len(raw)))

	menu := "Do you want to proceed?\n❯ 1. Yes\n  2. No, keep going\n"
	seedGridMenu(t, p, menu)

	// (a) Fresh grid menu fires choice-prompt.
	if got := p.computeState(); got != StateChoicePrompt {
		text, _ := p.gridVisibleText()
		t.Fatalf("(a) first grid menu: computeState = %q, want %q; gridText:\n%s", got, StateChoicePrompt, text)
	}
	ackedPos := atomic.LoadInt64(&p.LastAckedChoicePromptPos)
	if ackedPos <= 0 {
		t.Fatalf("(a) LastAckedChoicePromptPos not advanced: %d", ackedPos)
	}

	// (b) User answers: their PTY write lands past the acked menu position.
	// Same fingerprint => must NOT re-fire choice-prompt.
	atomic.StoreInt64(&p.LastInputStrippedPos, ackedPos+1)
	// More degraded residue arrives (no new menu identity), advancing the
	// stripped counter — the grid still shows the SAME menu though.
	more := strings.Repeat("  spinner working ...\n", 4)
	if _, err := p.buffer.Write([]byte(more)); err != nil {
		t.Fatalf("buffer.Write more: %v", err)
	}
	atomic.AddInt64(&p.TotalStrippedChars, int64(len(more)))
	if got := p.computeState(); got == StateChoicePrompt {
		t.Fatalf("(b) answered grid menu (unchanged fingerprint) still reports %q; the input-position guard did not suppress it", got)
	}

	// (c) A genuinely NEW grid menu (different options => different
	// fingerprint) must fire again even though the user already answered the
	// previous one.
	newMenu := "Do you want to make this edit?\n❯ 1. Yes, apply it\n  2. Cancel the edit\n  3. Show diff\n"
	seedGridMenu(t, p, newMenu)
	if got := p.computeState(); got != StateChoicePrompt {
		text, _ := p.gridVisibleText()
		t.Fatalf("(c) new grid menu: computeState = %q, want %q; gridText:\n%s", got, StateChoicePrompt, text)
	}
}

// TestComputeStateGridMcpPromptTurnover pins the grid prompt-instance
// lifecycle across different and byte-identical back-to-back Codex MCP
// permission modals. The no-prompt frame and replacement are both fed BEFORE
// computeState runs, reproducing production's slow-poller miss: turnover must
// come from the live grid feed, not a synthetic computeState absence sample.
func TestComputeStateGridMcpPromptTurnover(t *testing.T) {
	t.Parallel()
	p := &Pane{
		ID:                "pane-grid-mcp-turnover",
		Cols:              80,
		Rows:              24,
		Status:            PaneRunning,
		buffer:            NewRingBuffer(1 << 16),
		subs:              map[int64]*subEntry{},
		done:              make(chan struct{}),
		forceCodexForTest: true,
	}
	defer close(p.done)
	defer p.shutdownGrid()

	raw := strings.Repeat("   •      \n", 12)
	if _, err := p.buffer.Write([]byte(raw)); err != nil {
		t.Fatalf("buffer.Write: %v", err)
	}
	atomic.StoreInt64(&p.TotalStrippedChars, int64(len(raw)))

	appendAnsweredResidue := func(acked int64) {
		t.Helper()
		more := "  spinner redraw residue ...\n"
		if _, err := p.buffer.Write([]byte(more)); err != nil {
			t.Fatalf("buffer.Write residue: %v", err)
		}
		atomic.AddInt64(&p.TotalStrippedChars, int64(len(more)))
		atomic.StoreInt64(&p.LastInputStrippedPos, acked+1)
	}
	createPrompt := codexMcpPermissionPromptFor("create_workflow_task", "wft_create123")
	getPrompt := codexMcpPermissionPromptFor("get_workflow_task", "wft_f0a87099")

	// (a) First recognized MCP modal is a fresh prompt instance.
	seedGridMenu(t, p, createPrompt)
	if got := p.computeState(); got != StateChoicePrompt {
		t.Fatalf("(a) create_workflow_task modal: computeState = %q, want %q", got, StateChoicePrompt)
	}
	acked1 := atomic.LoadInt64(&p.LastAckedChoicePromptPos)
	if acked1 <= 0 {
		t.Fatalf("(a) LastAckedChoicePromptPos not advanced: %d", acked1)
	}

	// A committed absent→identical-present edge can land after admission but
	// before directAutoFire writes Enter. Re-confirming the still-unanswered
	// instance must consume that generation so the eventual answer cannot turn
	// the already-seen redraw into a second prompt.
	ackedGeneration1 := atomic.LoadUint64(&p.LastAckedGridChoicePromptGeneration)
	p.subsMu.Lock()
	p.feedGridPayloadLocked([]byte(
		gridSynchronizedFrame("✶ Working…\n")+gridSynchronizedFrame(createPrompt)), true)
	p.subsMu.Unlock()
	redrawGeneration := atomic.LoadUint64(&p.GridChoicePromptGeneration)
	if redrawGeneration <= ackedGeneration1 {
		t.Fatalf("(a) pre-answer absent→present redraw did not advance generation: acked=%d current=%d", ackedGeneration1, redrawGeneration)
	}
	if got := p.computeState(); got != StateChoicePrompt {
		t.Fatalf("(a) still-unanswered redrawn modal: computeState = %q, want %q", got, StateChoicePrompt)
	}
	if got := atomic.LoadUint64(&p.LastAckedGridChoicePromptGeneration); got != redrawGeneration {
		t.Fatalf("(a) unanswered recheck did not consume redraw generation: got=%d want=%d", got, redrawGeneration)
	}

	// (b) Once answered, the continuously visible instance is stale and must
	// not fire again.
	appendAnsweredResidue(acked1)
	if got := p.computeState(); got == StateChoicePrompt {
		t.Fatalf("(b) answered continuously visible modal re-fired as %q", got)
	}

	// (c-d) Commit a no-prompt frame and then the different-tool modal without
	// polling between them. Its tool/argument-aware fingerprint changes too.
	generation1 := atomic.LoadUint64(&p.GridChoicePromptGeneration)
	seedGridMenu(t, p, "✶ Working…\n")
	seedGridMenu(t, p, getPrompt)
	if got := atomic.LoadUint64(&p.GridChoicePromptGeneration); got <= generation1 {
		t.Fatalf("(d) live feed did not advance prompt generation: before=%d after=%d", generation1, got)
	}
	if got := p.computeState(); got != StateChoicePrompt {
		t.Fatalf("(d) get_workflow_task modal: computeState = %q, want %q", got, StateChoicePrompt)
	}
	acked2 := atomic.LoadInt64(&p.LastAckedChoicePromptPos)
	if acked2 <= acked1 {
		t.Fatalf("(d) acked position did not advance: first=%d second=%d", acked1, acked2)
	}

	// (e) An answered, byte-identical repeat after a committed no-prompt frame
	// is a new prompt instance even though its fingerprint is unchanged. Again,
	// no computeState call is allowed to sample the gap.
	appendAnsweredResidue(acked2)
	generation2 := atomic.LoadUint64(&p.GridChoicePromptGeneration)
	p.subsMu.Lock()
	// One socket payload can coalesce both committed frames. The feed parser
	// must retain the absent→present edge even when no read boundary exposes it.
	p.feedGridPayloadLocked([]byte(
		gridSynchronizedFrame("✶ Working…\n")+gridSynchronizedFrame(getPrompt)), true)
	p.subsMu.Unlock()
	if got := atomic.LoadUint64(&p.GridChoicePromptGeneration); got <= generation2 {
		t.Fatalf("(e) identical repeat did not advance live generation: before=%d after=%d", generation2, got)
	}
	if got := p.computeState(); got != StateChoicePrompt {
		t.Fatalf("(e) identical get_workflow_task repeat: computeState = %q, want %q", got, StateChoicePrompt)
	}
	acked3 := atomic.LoadInt64(&p.LastAckedChoicePromptPos)
	if acked3 <= acked2 {
		t.Fatalf("(e) turnover did not advance acked position: second=%d third=%d", acked2, acked3)
	}

	// (f) A clear/spinner/repaint inside ONE synchronized-output transaction is
	// a ratatui redraw, not an observed no-prompt frame. The feed observer sees
	// only the committed final menu, so it must not advance or re-fire.
	appendAnsweredResidue(acked3)
	generation3 := atomic.LoadUint64(&p.GridChoicePromptGeneration)
	p.subsMu.Lock()
	flicker := "\x1b[?2026h\x1b[2J\x1b[H✶ Working…\r\n\x1b[2J\x1b[H" +
		strings.ReplaceAll(getPrompt, "\n", "\r\n") + "\x1b[?2026l"
	p.feedGridPayloadLocked([]byte(flicker), true)
	p.subsMu.Unlock()
	if got := atomic.LoadUint64(&p.GridChoicePromptGeneration); got != generation3 {
		t.Fatalf("(f) mid-frame repaint advanced generation: before=%d after=%d", generation3, got)
	}
	if got := p.computeState(); got == StateChoicePrompt {
		t.Fatalf("(f) synchronized repaint re-fired identical modal")
	}

	// (g) LastAckedChoicePromptPos remains the shared instance key: advancing
	// it on turnover re-arms a sweeper suspended on the previous instance.
	a := &Aria{sweepStreaks: map[string]*sweepStreak{
		p.ID: {consecutive: ariaSweeperSuspendThreshold},
	}}
	if skip := a.shouldSkipSweeper(p.ID, acked2); !skip {
		t.Fatal("(g) sweeper did not suspend at the previous prompt position")
	}
	if skip := a.shouldSkipSweeper(p.ID, acked3); skip {
		t.Fatal("(g) advanced turnover position did not re-arm sweeper")
	}
}

// TestCountStrippedCharsParity asserts countStrippedChars produces a
// byte-identical count to len(stripANSI(...)) across ANSI-laden samples.
// TotalStrippedChars (which countStrippedChars now feeds in the readLoop)
// drives absolute-position math in computeState, so any drift would re-break
// answered-menu suppression.
func TestCountStrippedCharsParity(t *testing.T) {
	t.Parallel()
	samples := [][]byte{
		[]byte(""),
		[]byte("plain text no escapes"),
		[]byte("\x1b[38;5;6m│\x1b[0m > \x1b[38;5;6m│\x1b[0m"),
		[]byte("before\x1b]0;window title\x07after"),
		[]byte("before\x1b]0;window title\x1b\\after"),
		[]byte("\x1b]0;solu\x07\x1b[31mred\x1b[0m"),
		[]byte("\x1b[32mApp\x1b[5Crunning\x1b[9Cat\x1b[0m"),
		[]byte("line one\r\nline two\r\nline three"),
		[]byte("spinner\rspinner2\rspinner3"),
		[]byte("mixed\r\nplus lone\rcr and \nlf"),
		// Truncated CSI at end of buffer (no final byte) — must stay literal.
		[]byte("text then \x1b[38;5"),
		// Truncated OSC at end of buffer (no terminator) — must stay literal.
		[]byte("title \x1b]0;unterminated"),
		// Bare ESC at very end.
		[]byte("ends with esc\x1b"),
		// ESC not starting a known sequence (\x1bX).
		[]byte("esc\x1bXmiddle"),
		// Adjacent escapes, one valid one not.
		[]byte("\x1b[\x1b[1mboldish"),
		// CSI with private-mode + intermediate bytes.
		[]byte("\x1b[?25l\x1b[?25hcursor"),
		// CR immediately before a CSI then LF (no \r\n collapse across the space).
		[]byte("a\r\x1b[0m\nb"),
		// OSC with embedded ESC that is not the ST terminator.
		[]byte("x\x1b]0;ab\x1bcd\x07y"),
		// Multibyte UTF-8 content (count is byte length, both sides agree).
		[]byte("héllo ❯ 1. Yés\r\n  2. Nö"),
		// Pure escape soup.
		[]byte("\x1b[2J\x1b[H\x1b[1;1H\x1b]0;t\x07\x1b[0m"),
	}
	for i, s := range samples {
		want := len(stripANSI(s))
		got := countStrippedChars(s)
		if got != want {
			t.Errorf("sample %d %q: countStrippedChars = %d, want len(stripANSI)=%d\n  stripANSI=%q",
				i, s, got, want, stripANSI(s))
		}
	}
}

// TestCountStrippedCharsParityGenerated fuzzes parity by splicing a small
// alphabet of bytes that includes ESC, '[', ']', CSI terminators, BEL, ST,
// CR, LF, and ordinary text — the bytes most likely to expose a divergence
// between the one-pass counter and the two-pass stripANSI.
func TestCountStrippedCharsParityGenerated(t *testing.T) {
	t.Parallel()
	alphabet := []byte{
		0x1b, '[', ']', '0', ';', '?', '5', 'm', 'C', 'H', 'l', 'h',
		0x07, '\\', '\r', '\n', ' ', 'A', '/', '@', '~', 0x00, 0xff,
	}
	// Deterministic LCG so failures reproduce.
	var seed uint64 = 0x9e3779b97f4a7c15
	next := func() byte {
		seed = seed*6364136223846793005 + 1442695040888963407
		return alphabet[(seed>>33)%uint64(len(alphabet))]
	}
	for iter := 0; iter < 5000; iter++ {
		n := int((seed >> 17) % 40)
		buf := make([]byte, n)
		for j := range buf {
			buf[j] = next()
		}
		want := len(stripANSI(buf))
		got := countStrippedChars(buf)
		if got != want {
			t.Fatalf("iter %d: countStrippedChars=%d want %d for %q\n  stripANSI=%q",
				iter, got, want, buf, stripANSI(buf))
		}
	}
}

// TestClaudeActiveSpinnerRE locks in the live-turn spinner detection that
// keeps mid-turn switch protection working on claude 2.1.204+ (which renders
// the composer WHILE generating, so the prompt box alone no longer means the
// turn was handed back). Fixtures lifted from a real 2.1.204 capture.
func TestClaudeActiveSpinnerRE(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		in   string
		want bool
	}{
		{"2.1.204 token-counter spinner", "·   Actioning …   (17s · ↓   677 tokens)", true},
		{"minutes + seconds variant", "· Reticulating … (1m 12s · ↓ 2,301 tokens)", true},
		{"uparrow variant", "✻ Cerebrating… (7s · ↑ 12 tokens)", true},
		{"legacy esc-to-interrupt spinner", "✻ Baking… (esc to interrupt)", true},
		{"backgrounded tool status", "Waiting for background terminal · (1m 45s · esc to interrupt)", true},
		{"settled brew marker is NOT live", "✻ Brewed for 17s", false},
		{"plain parenthesized number", "finished in (17s) flat", false},
		{"program output with tokens word", "parsed 677 tokens from input", false},
		{"idle prompt frame", "❯\n ~ [Opus 4.8 (1M context) | ctx: 3%]", false},
	}
	for _, c := range cases {
		if got := claudeActiveSpinnerRE.MatchString(c.in); got != c.want {
			t.Errorf("%s: got %v want %v (in=%q)", c.name, got, c.want, c.in)
		}
	}
}

// TestClaudePromptVisible_SpinnerOverridesComposer is the regression for the
// 2026-07-08 repro: on 2.1.204 the composer (❯ + ctx footer) is rendered
// DURING generation, so a streaming terminal pane read as "prompt visible" →
// agentMidTurn=false → no confirm, no graceful interrupt, no "interrupted —
// Continue" notice on a Term→Focus switch. A live spinner near the tail must
// override the prompt box; a settled frame (no spinner in the window) must
// still read prompt-visible so the idle-switch and 3s-recency-tail fixes hold.
func TestClaudePromptVisible_SpinnerOverridesComposer(t *testing.T) {
	t.Parallel()
	// Condensed from the real capture: story text, live spinner, then the
	// composer + footer that 2.1.204 keeps on screen mid-turn.
	streamingFrame := "the lamp turned in its long revolution, sweeping the black sea.\n" +
		"  ·   Actioning …   (17s · ↓   677 tokens)  \n" +
		"  ──────────────────────────────\n" +
		"  ❯   \n" +
		"  ──────────────────────────────\n" +
		"   ~     [Opus 4.8 (1M context) | ctx: 3%]  \n"
	settledFrame := "and the sailor turned, without hurry, toward home.\n" +
		"  ✻ Brewed for 17s  \n" +
		"  ──────────────────────────────\n" +
		"  ❯   \n" +
		"  ──────────────────────────────\n" +
		"   ~     [Opus 4.8 (1M context) | ctx: 3%]  \n"

	// Spinner-tick junk: between full spinner re-renders (~1/s) the TUI emits
	// bare animation glyphs every ~100ms. These pushed the parenthesized
	// spinner text far back in the tail — the reason the override scans the
	// whole stripped tail positionally instead of a small trailing window.
	tickJunk := strings.Repeat("  ✶ \n\n\n  ✻ \n\n\n  ✽ \n\n\n", 20)

	mk := func(content string, recent bool) *Pane {
		p := &Pane{ID: "spin", Kind: "", Status: PaneRunning, buffer: NewRingBuffer(1 << 15)}
		_, _ = p.buffer.Write([]byte(content))
		if recent {
			p.LastActivityMs = time.Now().UnixMilli()
		} else {
			p.LastActivityMs = time.Now().Add(-30 * time.Second).UnixMilli()
		}
		return p
	}

	// LIVE turn: spinner (even with a wall of tick junk after it) + recent
	// output → NOT prompt-visible → working/mid-turn.
	streaming := mk(streamingFrame+tickJunk, true)
	if streaming.claudePromptVisible() {
		t.Fatal("live spinner + composer read as prompt-visible; mid-turn switches would lose confirm/interrupt/notice")
	}
	if !streaming.IsAgentWorking() {
		t.Fatal("streaming 2.1.204 frame must read as WORKING")
	}
	if !streaming.agentMidTurnForState(StateBusy) {
		t.Fatal("agentMidTurn must be true while the spinner is live (drives the client confirm gate)")
	}

	// SETTLED: the completion marker lands AFTER the last spinner, so even
	// inside the ~3s closing-redraw recency tail the pane reads handed-back —
	// the original idle-switch bug must not return.
	settled := mk(streamingFrame+tickJunk+settledFrame, true)
	if !settled.claudePromptVisible() {
		t.Fatal("settled frame must read prompt-visible (the recency-tail fix must survive this change)")
	}
	if settled.IsAgentWorking() {
		t.Fatal("settled frame inside the recency tail must NOT read as working (the original idle-switch bug)")
	}

	// INTERRUPTED: claude paints no completion marker, but the pane has been
	// QUIET — a live spinner always animates, so stale spinner scrollback on a
	// silent pane must not block the settle signal (the deferred-switch
	// watcher polls claudePromptVisible to fire).
	interrupted := mk(streamingFrame, false)
	if !interrupted.claudePromptVisible() {
		t.Fatal("stale spinner on a quiet pane blocked prompt-visible; a deferred switch would never fire after an interrupt")
	}
}

// TestClaudePromptVisible_ScreenPath exercises the PREFERRED source: the
// rendered replay grid, against the three phases of a live claude 2.1.204
// turn (real sandbox captures, 2026-07-08):
//
//	THINKING  — bare "✢ Scampering…" spinner (no parens, no counter) below the
//	            previous turn's completion marker;
//	STREAMING — NO spinner at all: prose accumulates below the last completion
//	            marker while output flows;
//	SETTLED   — "✻ Cooked for 22s" painted at the transcript bottom.
//
// The raw ring can't see any of this reliably (the spinner animates via
// cursor-move fragments), which is why the first seconds of a turn used to
// mis-read as handed back.
func TestClaudePromptVisible_ScreenPath(t *testing.T) {
	t.Parallel()
	chrome := "  ──────────────────────────────\r\n" +
		"  ❯\r\n" +
		"  ──────────────────────────────\r\n" +
		"   ~ [Opus 4.8 (1M context) | ctx: 3%]\r\n" +
		"  ⏵⏵ auto mode on (shift+tab to cycle)\r\n"
	thinkingScreen := "  ✻ Worked for 24s\r\n" +
		"  ❯ now a 300 word story about a gardener, slowly\r\n" +
		"  ✢ Scampering…\r\n" + chrome
	streamingScreen := "  ✻ Worked for 24s\r\n" +
		"  ❯ now a 300 word story about a gardener, slowly\r\n" +
		"  Old Marta kept bees slowly, because bees, of all the world's creatures,\r\n" +
		"  punish the man in a hurry.\r\n" + chrome
	settledScreen := "  The far bank came, as it always came, gently and too soon.\r\n" +
		"  ✻ Cooked for 22s\r\n" + chrome

	mk := func(screen string, recent bool) *Pane {
		p := &Pane{ID: "scr", Kind: "", Status: PaneRunning, buffer: NewRingBuffer(1 << 12)}
		p.replayEmu = grid.New(110, 24)
		p.replayEmu.Feed([]byte(screen))
		if recent {
			p.LastActivityMs = time.Now().UnixMilli()
		} else {
			p.LastActivityMs = time.Now().Add(-30 * time.Second).UnixMilli()
		}
		return p
	}

	// THINKING: spinner below the old completion marker → working, from second
	// zero of the turn (the confirm gate depends on this).
	thinking := mk(thinkingScreen, true)
	if thinking.claudePromptVisible() {
		t.Fatal("thinking-phase screen (bare gerund spinner) read as prompt-visible")
	}
	if !thinking.agentMidTurnForState(StateBusy) {
		t.Fatal("thinking-phase screen must read mid-turn")
	}

	// STREAMING: no spinner — prose after the last completion marker + recent
	// output is the live signal.
	streaming := mk(streamingScreen, true)
	if streaming.claudePromptVisible() {
		t.Fatal("streaming-phase screen (prose after the last completion marker, output flowing) read as prompt-visible")
	}
	if !streaming.agentMidTurnForState(StateBusy) {
		t.Fatal("streaming-phase screen must read mid-turn")
	}

	// SETTLED: the fresh completion marker is the transcript bottom — even
	// inside the ~3s closing-redraw recency window the pane reads handed back
	// (the original idle-confirm bug must not return).
	settled := mk(settledScreen, true)
	if !settled.claudePromptVisible() {
		t.Fatal("settled screen must read prompt-visible during the closing-redraw window")
	}
	if settled.IsAgentWorking() {
		t.Fatal("settled screen must NOT read as working")
	}

	// INTERRUPTED: partial output after the old marker but the pane has gone
	// QUIET — must read handed back so the deferred-switch watcher can fire.
	interrupted := mk(streamingScreen, false)
	if !interrupted.claudePromptVisible() {
		t.Fatal("interrupted (quiet) screen read as working; a deferred switch would never fire")
	}
}
