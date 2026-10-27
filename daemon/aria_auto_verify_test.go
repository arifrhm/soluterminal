package main

import (
	"context"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// claudeReadDialog is the queued Read permission from the 2026-10-15 incident:
// two parallel Reads in one directory render dialogs whose option rows match.
func claudeReadDialog(position, lines string) string {
	return "⏺ Reading 1 file… (ctrl+o to expand)\n\n" +
		"────────────────────────────────────────────────────────────\n" +
		" Read file                                        " + position + "\n\n" +
		"  Read(/home/u/repo/apps/core/src/integrations/files-policy.ts · " + lines + ")\n\n" +
		" Do you want to proceed?\n" +
		" ❯ 1. Yes\n" +
		"   2. Yes, allow reading from integrations/ during this session\n" +
		"   3. No\n\n" +
		" Esc to cancel · Tab to amend"
}

// newClaudeVerifyHarness builds an always-mode Claude-style pane. Claude paints
// without DEC 2026 frames, so the grid prompt generation never advances.
func newClaudeVerifyHarness(t *testing.T, id string) (*Aria, *Pane, <-chan directAutoFrameRead) {
	t.Helper()
	p := &Pane{
		ID:       id,
		Title:    "shell",
		Cols:     100,
		Rows:     30,
		Status:   PaneRunning,
		AutoMode: "always",
		buffer:   NewRingBuffer(1 << 16),
		subs:     map[int64]*subEntry{},
		done:     make(chan struct{}),
	}
	t.Cleanup(func() {
		close(p.done)
		p.shutdownGrid()
	})
	paneEnd, agentEnd := net.Pipe()
	p.agentConn = paneEnd
	t.Cleanup(func() { paneEnd.Close(); agentEnd.Close() })
	frames := make(chan directAutoFrameRead, 8)
	go func() {
		for {
			type_, payload, err := ReadFrame(agentEnd)
			frames <- directAutoFrameRead{type_: type_, payload: payload, err: err}
			if err != nil {
				return
			}
		}
	}()
	a := testAriaWithPane(t, p)
	a.sweepStreaks = map[string]*sweepStreak{}
	a.evalLog = newAriaEvalLog()
	a.autoFirePacingOverride = &autoFirePacing{
		verify: []time.Duration{50 * time.Millisecond, 100 * time.Millisecond, 250 * time.Millisecond},
	}
	p.ensureGrid()
	return a, p, frames
}

// paintClaude repaints the visible screen the way Claude does: no synchronized
// frame. raw is what reaches the byte ring; an in-place diff carries only the
// changed rows, not a fresh "Do you want to proceed?" menu.
func paintClaude(t *testing.T, p *Pane, screen, raw string) {
	t.Helper()
	p.subsMu.Lock()
	p.feedGridPayloadLocked([]byte("\x1b[2J\x1b[H"+strings.ReplaceAll(screen, "\n", "\r\n")), true)
	_ = p.gridEmu.DrainReplies()
	p.subsMu.Unlock()
	if _, err := p.buffer.Write([]byte(raw)); err != nil {
		t.Fatalf("buffer.Write: %v", err)
	}
	atomic.AddInt64(&p.TotalStrippedChars, int64(len(stripANSI([]byte(raw)))))
}

func fireFirstClaudeDialog(t *testing.T, a *Aria, p *Pane, frames <-chan directAutoFrameRead) {
	t.Helper()
	first := claudeReadDialog("1 of 2", "lines 1-20")
	paintClaude(t, p, first, "\n"+first+"\n")
	if got := p.computeState(); got != StateChoicePrompt {
		t.Fatalf("first dialog state = %q, want choice-prompt", got)
	}
	a.directAutoFire(context.Background(), p.ID, p.Title)
	expectFrame(t, frames, "first approval", "\r")
}

// fireAgainFromGrid reproduces the second Enter in the incident log, 160ms
// after the first: the raw branch now reads the menu as answered, and the grid
// branch, whose last admitted fingerprint predates this menu, admits the
// rendered dialog as new. Claude drops this Enter while the dialog mounts, and
// from here every poller reads the pane as answered.
func fireAgainFromGrid(t *testing.T, a *Aria, p *Pane, frames <-chan directAutoFrameRead) {
	t.Helper()
	if got := p.computeState(); got != StateChoicePrompt {
		t.Fatalf("grid re-admission state = %q, want choice-prompt", got)
	}
	a.directAutoFire(context.Background(), p.ID, p.Title)
	expectFrame(t, frames, "grid re-admission", "\r")
	if got := p.computeState(); got == StateChoicePrompt {
		t.Fatal("precondition: the still-rendered menu should read as answered")
	}
}

// The incident shape: "1 of 2" is answered and Claude swaps in "2 of 2" in
// place. Its option rows match, the raw diff holds no new menu header, and the
// Enter for it lands while it mounts. Before the fix the pane parked until a
// resize repainted the whole menu.
func TestAutoFireVerifyRetriesDroppedQueuedDialog(t *testing.T) {
	t.Parallel()
	a, p, frames := newClaudeVerifyHarness(t, "pane-verify-queued")
	fireFirstClaudeDialog(t, a, p, frames)

	second := claudeReadDialog("2 of 2", "lines 112-167")
	paintClaude(t, p, second, "\x1b[4;52H2 of 2\x1b[6;3HRead(files-policy.ts · lines 112-167)")
	fireAgainFromGrid(t, a, p, frames)
	expectFrame(t, frames, "retry of the dropped queued approval", "\r")
}

// A TUI that keeps ignoring Enter on one unchanged screen is bounded by the
// unchanged-screen limit and handed to the user instead of parking silently.
func TestAutoFireVerifySurfacesRepeatedlyIgnoredMenu(t *testing.T) {
	t.Parallel()
	a, p, frames := newClaudeVerifyHarness(t, "pane-verify-ignored")
	fireFirstClaudeDialog(t, a, p, frames)
	fireAgainFromGrid(t, a, p, frames)

	time.Sleep(200 * time.Millisecond)
	expectNoFrame(t, frames, "third Enter on an unchanged screen")
	if att, reason := p.AttentionNow(); att != AttentionNeedsUser || reason == "" {
		t.Fatalf("attention after exhausted retries = %q/%q, want needs-user", att, reason)
	}
}

func TestAutoFireVerifyLeavesAcceptedEnterAlone(t *testing.T) {
	t.Parallel()
	a, p, frames := newClaudeVerifyHarness(t, "pane-verify-accepted")
	fireFirstClaudeDialog(t, a, p, frames)

	done := "⏺ Read(apps/core/src/integrations/files-policy.ts)\n  ⎿  Read 20 lines\n\n✻ Mulling… (3s)"
	paintClaude(t, p, done, "\n"+done+"\n")
	time.Sleep(200 * time.Millisecond)
	expectNoFrame(t, frames, "accepted Enter")
}

func TestAutoFireVerifyStopsAfterOtherInput(t *testing.T) {
	t.Parallel()
	a, p, frames := newClaudeVerifyHarness(t, "pane-verify-user-input")
	fireFirstClaudeDialog(t, a, p, frames)

	// The user answered or typed after the auto-approval; the menu is theirs.
	atomic.AddInt64(&p.LastInputMs, 1)
	time.Sleep(200 * time.Millisecond)
	expectNoFrame(t, frames, "menu after user input")
}

// Codex frames its menus (DEC 2026) and its fingerprint carries the command,
// but an Enter it drops while the approval overlay mounts parks the pane the
// same way: same fingerprint, no new frame, input past the ack.
func TestAutoFireVerifyRetriesDroppedCodexEnter(t *testing.T) {
	t.Parallel()
	a, p, frames, _ := newDirectAutoRepeatHarness(t, "pane-verify-codex")
	a.autoFirePacingOverride = &autoFirePacing{
		verify: []time.Duration{50 * time.Millisecond, 100 * time.Millisecond, 250 * time.Millisecond},
	}
	assertDirectAutoPromptReady(t, p, "codex menu")
	a.directAutoFire(context.Background(), p.ID, p.Title)
	expectFrame(t, frames, "codex approval", "\r")
	if got := p.computeState(); got == StateChoicePrompt {
		t.Fatal("precondition: the still-rendered codex menu should read as answered")
	}
	expectFrame(t, frames, "retry of the dropped codex Enter", "\r")
}

// A TUI that is merely slow to repaint an accepted answer still shows the menu
// at the first check. One sighting is not proof of a dropped Enter.
func TestAutoFireVerifyWaitsOutSlowRepaint(t *testing.T) {
	t.Parallel()
	a, p, frames := newClaudeVerifyHarness(t, "pane-verify-slow-repaint")
	fireFirstClaudeDialog(t, a, p, frames)

	time.Sleep(75 * time.Millisecond)
	done := "⏺ Read(apps/core/src/integrations/files-policy.ts)\n  ⎿  Read 20 lines\n\n✻ Mulling… (3s)"
	paintClaude(t, p, done, "\n"+done+"\n")
	time.Sleep(200 * time.Millisecond)
	expectNoFrame(t, frames, "slow repaint of an accepted Enter")
}
