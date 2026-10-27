package main

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// deepseekApprovalMenuFixture is the EXACT text
// daemon/harness_assets/deepseek-terminal.mjs renderPermission() writes for a
// tool with no "always allow" category. It is duplicated here on purpose: this
// test is the cross-language contract. SoluTerminal's per-pane AutoMode only
// works for a DeepSeek pane if this text is classified as an approval prompt,
// so a reword in the client's menu must fail here.
const deepseekApprovalMenuFixture = `Tool approval: tool
Do you want to proceed?
❯ 1. Yes, allow once
  2. No, reject`

// The client draws its raw composer directly under the menu after rendering
// it, so computeState scrapes the menu with the "Select [1/2]:" prefix and the
// selector status row still below it. Keep this shape in the fixture: a
// composer line that made trimTrailingStatusJunk swallow the menu, or that
// contained a generic-menu needle ("enter to select", "↑/↓ to navigate"),
// would silently break auto-approval.
const deepseekApprovalMenuFixtureWithComposer = deepseekApprovalMenuFixture + `
Select [1/2]: 
[1] Yes, allow once · ↑/↓ choose · Enter · 1/2`

func TestDeepseekApprovalMenuIsAutoApprovable(t *testing.T) {
	for name, screen := range map[string]string{
		"menu only":          deepseekApprovalMenuFixture,
		"with raw composer":  deepseekApprovalMenuFixtureWithComposer,
		"with labeled tool":  strings.Replace(deepseekApprovalMenuFixture, "Tool approval: tool", "Tool approval: Bash", 1),
		"with always option": deepseekApprovalMenuFixture + "\n  3. Always allow Bash",
		// A classified tool adds "3. Always allow" and the selector status then
		// advertises 1/2/3. The ↑/↓ choose wording must not read as the daemon's
		// generic-menu needle "↑/↓ to navigate".
		"selector on three choices":  deepseekApprovalMenuFixture + "\n  3. Always allow Bash\nSelect [1/2/3]: \n[1] Yes, allow once · ↑/↓ choose · Enter · 1/2/3",
		"selector on the second row": deepseekApprovalMenuFixtureWithComposer + "\n[2] No, reject · ↑/↓ choose · Enter · 1/2",
	} {
		if !isGridChoicePromptText(screen, false) {
			t.Fatalf("%s: DeepSeek approval menu is not a grid choice prompt", name)
		}
		if matches := approvalPromptMatches(screen, false); len(matches) == 0 {
			t.Fatalf("%s: approvalPromptMatches missed the DeepSeek approval menu", name)
		}
		if !isAutoApprovalPromptText(screen, false) {
			t.Fatalf("%s: isAutoApprovalPromptText rejected the DeepSeek menu; AutoMode would not fire", name)
		}
	}
}

func TestDeepseekApprovalMenuSurvivesTranscriptChrome(t *testing.T) {
	// The menu renders below whatever the agent already printed; the daemon
	// scans the visible grid/tail, not just a bare menu.
	screen := "The admission-off path needs /run/user/1000, which the sandbox denied.\n\n" +
		"• $ bash\ncd /repo && ./publish.sh --skip-tests 2>&1\n\n" +
		deepseekApprovalMenuFixtureWithComposer
	if !isAutoApprovalPromptText(screen, false) {
		t.Fatal("DeepSeek approval menu was not detected below transcript output")
	}
}

func TestManagedDeepseekCmdlineMarker(t *testing.T) {
	managed := "node /home/u/.solu/harness-launchers/deepseek-terminal.mjs " +
		"/home/u/.local/share/soluterminal/harnesses/deepseek/node_modules/@deepseek-ai/dsh/lib/bin.js deepseek-flash"
	if !isManagedDeepseekCmdline(managed) {
		t.Fatal("managed DeepSeek client argv was not recognized")
	}
	for _, other := range []string{
		"",
		"node /usr/bin/some-server --port 3000",
		"node /home/u/.local/share/soluterminal/harnesses/deepseek/node_modules/@deepseek-ai/dsh/lib/bin.js --profile acp",
		"node /home/u/.solu/harness-launchers/launch.mjs deepseek --data-dir /home/u/.solu",
	} {
		if isManagedDeepseekCmdline(other) {
			t.Fatalf("unmanaged argv %q was misclassified as the DeepSeek client", other)
		}
	}
}

// TestDeepseekBadgeWithoutLiveProcessScan pins the badge DTO to the pane's
// managed-harness identity, not just the live /proc scan. The auto-approve
// button renders from HasDeepseek, but the scan misses whenever the client
// argv is unreadable (Windows stub, foreign uid, non-node wrapper) or simply
// hasn't appeared yet — while the daemon already knows from the spawn argv
// (launch.mjs deepseek) or the adopted sidecar session that this pane IS a
// managed DeepSeek pane. A pane with either signal and an empty scan must
// still report HasDeepseek, or the button never appears.
func TestDeepseekBadgeWithoutLiveProcessScan(t *testing.T) {
	t.Parallel()
	empty := paneProcessSnapshot{}
	for name, pane := range map[string]*Pane{
		"adopted sidecar session": func() *Pane {
			p := switchTestPane("pane-ds-badge-sidecar", "", []string{"bash"}, "")
			p.HarnessProvider = "deepseek"
			p.HarnessSessionID = "sess-1"
			return p
		}(),
		"managed launch argv": switchTestPane("pane-ds-badge-cmd", "",
			[]string{"node", "/home/u/.solu/harness-launchers/launch.mjs", "deepseek", "--data-dir", "/home/u/.solu"}, ""),
		"direct client argv": switchTestPane("pane-ds-badge-direct", "",
			[]string{"node", "/home/u/.solu/harness-launchers/deepseek-terminal.mjs", "runtime", "model"}, ""),
	} {
		if dto := makePaneDTOWithProcessSnapshot(pane, nil, nil, empty); !dto.HasDeepseek {
			t.Fatalf("%s: HasDeepseek=false with empty process scan; auto-approve button hidden", name)
		}
	}
	plain := switchTestPane("pane-ds-badge-plain", "", []string{"bash"}, "")
	if dto := makePaneDTOWithProcessSnapshot(plain, nil, nil, empty); dto.HasDeepseek {
		t.Fatal("plain shell pane reports HasDeepseek; button would show with nothing to approve")
	}
	pi := switchTestPane("pane-pi-badge-cmd", "",
		[]string{"node", "/home/u/.solu/harness-launchers/launch.mjs", "pi", "--data-dir", "/home/u/.solu"}, "")
	if dto := makePaneDTOWithProcessSnapshot(pi, nil, nil, empty); !dto.HasPi {
		t.Fatal("managed Pi launch reports HasPi=false with empty process scan; auto-approve button hidden")
	}
}

// TestHarnessScanIgnoresCommRename is the regression test for the missing
// DeepSeek auto-approve button. Node >=24 reports its main thread as
// "MainThread" in /proc/pid/comm, so the scan's comm=="node" prefilter
// skipped the client argv carrying the managed-only marker and HasDeepseek
// stayed false on live panes. A descendant whose argv holds the marker but
// whose comm is NOT node must still mark the pane. Exercised with a real
// process (bash exec -a renames argv[0] only, comm stays "sleep") through
// the real /proc readers and the real snapshotPaneProcessesFromTree, with a
// fake tree edge grafting it under the pane.
func TestHarnessScanIgnoresCommRename(t *testing.T) {
	t.Parallel()
	spawnRenamed := func(argv0 string) *os.Process {
		t.Helper()
		cmd := exec.Command("bash", "-c", "exec -a "+argv0+" sleep 60")
		if err := cmd.Start(); err != nil {
			t.Skipf("cannot spawn helper process: %v", err)
		}
		t.Cleanup(func() { _ = cmd.Process.Kill() })
		// Wait for the exec to COMPLETE, not merely for the marker to appear:
		// the `bash -c` string already contains argv0, so a marker-only wait
		// returns during the bash phase. A scan that then lands in execve's
		// transient window — /proc/pid/cmdline is briefly empty while the old
		// argv is torn down and the new one installed — reads no marker and
		// fails spuriously (seen in the -race lane). Requiring the target comm
		// proves the renamed argv is the live, stable one before we scan.
		const helperComm = "sleep"
		deadline := time.Now().Add(5 * time.Second)
		for {
			if comm := readComm(cmd.Process.Pid); comm == helperComm &&
				strings.Contains(readHarnessCmdline(cmd.Process.Pid), argv0) {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("helper never exec'd %s with argv %q (comm=%q)",
					helperComm, argv0, readComm(cmd.Process.Pid))
			}
			time.Sleep(10 * time.Millisecond)
		}
		return cmd.Process
	}
	deepseek := spawnRenamed("deepseek-terminal.mjs")
	pi := spawnRenamed("pi-approval.mjs")
	p := switchTestPane("pane-harness-comm-rename", "", []string{"bash"}, "")
	p.Pid = os.Getpid()
	tree := processTreeSnapshot{children: map[int][]int{
		os.Getpid(): {deepseek.Pid, pi.Pid},
	}}
	snap := snapshotPaneProcessesFromTree(p, tree)
	if !snap.hasDeepseek {
		t.Fatal("live descendant with deepseek-terminal.mjs argv and non-node comm did not mark hasDeepseek")
	}
	if !snap.hasPi {
		t.Fatal("live descendant with pi-approval.mjs argv and non-node comm did not mark hasPi")
	}
}

// TestDeepseekApprovalMenuDirectAutoFire is the end-to-end contract: with
// AutoMode "always" the no-LLM fast path must press Enter on the DeepSeek
// client's focused option 1 exactly as it does for Claude/Codex/Pi.
func TestDeepseekApprovalMenuDirectAutoFire(t *testing.T) {
	t.Parallel()
	p, frames := newTypingHoldPane(t, "pane-deepseek-auto")
	// The DeepSeek client is not Codex: clear the helper's forced identity so
	// the pane exercises the plain numbered-menu path.
	p.forceCodexForTest = false
	p.Title = "shell"
	a := testAriaWithPane(t, p)
	a.sweepStreaks = map[string]*sweepStreak{}
	a.evalLog = newAriaEvalLog()

	seedGridMenu(t, p, deepseekApprovalMenuFixtureWithComposer)
	if got := p.computeState(); got != StateChoicePrompt {
		t.Fatalf("computeState = %q, want %q", got, StateChoicePrompt)
	}
	if _, _, _, ok := currentChoicePromptIdentity(p, ""); !ok {
		t.Fatal("no stable prompt identity; the test would pass for the wrong reason")
	}
	a.directAutoFire(context.Background(), p.ID, p.Title)
	expectFrame(t, frames, "deepseek auto-fire", "\r")
}
