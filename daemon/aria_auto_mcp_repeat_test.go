package main

import (
	"context"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type directAutoFrameRead struct {
	type_   FrameType
	payload []byte
	err     error
}

// TestDirectAutoFireBackToBackMcpPermissionModals reproduces the live repeated
// prompt failures through computeState, watcher identity capture,
// directAutoFire's rechecks, stale composer bookkeeping, and Pane.Write. The
// raw stream stays degraded so every prompt decision comes from the
// reconstructed grid.
func TestDirectAutoFireBackToBackMcpPermissionModals(t *testing.T) {
	t.Parallel()
	p := &Pane{
		ID:                "pane-grid-mcp-direct-repeat",
		Title:             "codex",
		Cols:              80,
		Rows:              24,
		Status:            PaneRunning,
		AutoMode:          "always",
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

	paneEnd, agentEnd := net.Pipe()
	p.agentConn = paneEnd
	defer paneEnd.Close()
	defer agentEnd.Close()
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

	readPayload := func(step, want string) {
		t.Helper()
		select {
		case frame := <-frames:
			if frame.err != nil {
				t.Fatalf("%s frame read: %v", step, frame.err)
			}
			if frame.type_ != FrameData {
				t.Fatalf("%s frame type = %v, want %v", step, frame.type_, FrameData)
			}
			if got := string(frame.payload); got != want {
				t.Fatalf("%s payload = %q, want %q", step, got, want)
			}
		case <-time.After(time.Second):
			t.Fatalf("%s timed out waiting for %q", step, want)
		}
	}
	readBareEnter := func(step string) {
		t.Helper()
		readPayload(step, "\r")
	}
	assertNoFrame := func(step string) {
		t.Helper()
		select {
		case frame := <-frames:
			t.Fatalf("%s unexpected frame: type=%v payload=%q err=%v", step, frame.type_, frame.payload, frame.err)
		case <-time.After(200 * time.Millisecond):
		}
	}
	appendRedrawBeforeFire := func() {
		t.Helper()
		// In production ratatui commonly emits another modal redraw between
		// watcher admission and Pane.Write. Advancing output here ensures the
		// real WriteNoPromptRecord bookkeeping places input past this instance's
		// ack, without directly mutating LastInputStrippedPos in the test.
		redraw := "  ratatui modal redraw residue ...\n"
		if _, err := p.buffer.Write([]byte(redraw)); err != nil {
			t.Fatalf("buffer.Write redraw: %v", err)
		}
		atomic.AddInt64(&p.TotalStrippedChars, int64(len(redraw)))
	}
	feedNoPromptFrame := func() {
		t.Helper()
		seedGridMenu(t, p, "✶ Working…\n")
	}
	fireGridPrompt := func(step, prompt string) int64 {
		t.Helper()
		seedGridMenu(t, p, prompt)
		if got := p.computeState(); got != StateChoicePrompt {
			gridText, _ := p.gridVisibleText()
			t.Fatalf("%s computeState = %q, want %q; grid:\n%s", step, got, StateChoicePrompt, gridText)
		}
		state, pos, fp, ok := currentChoicePromptIdentity(p, "")
		if !ok || state != StateChoicePrompt || pos == 0 || fp == 0 {
			t.Fatalf("%s identity = (state=%q pos=%d fp=%d ok=%v), want stable grid fallback", step, state, pos, fp, ok)
		}
		appendRedrawBeforeFire()
		a.directAutoFire(context.Background(), p.ID, p.Title)
		readBareEnter(step)
		if inputPos := atomic.LoadInt64(&p.LastInputStrippedPos); inputPos <= pos {
			t.Fatalf("%s Pane.Write input position = %d, want > acked prompt position %d", step, inputPos, pos)
		}
		return pos
	}

	// Seed the recorder through the production write path, then model a phantom
	// composer buffer that has outlived its safety window. LastInputMs remains
	// recent, as it did after the previous ARIA approval in the live loop, but
	// that control activity must not make the recorder's old draft fresh again.
	if _, err := p.Write([]byte("stale phantom draft")); err != nil {
		t.Fatalf("Pane.Write stale draft seed: %v", err)
	}
	readPayload("stale draft seed", "stale phantom draft")
	draftLen, recordedAt := p.composerPendingSnapshot()
	if draftLen == 0 || recordedAt <= 0 {
		t.Fatalf("seeded composer snapshot = (%d, %d), want nonempty with activity", draftLen, recordedAt)
	}
	p.agentWriteMu.Lock()
	staleRecordedAt := time.Now().Add(-2 * ariaAutoDraftStaleAfter).UnixMilli()
	p.promptRec.lastRecordedInputMs = staleRecordedAt
	p.agentWriteMu.Unlock()
	seedInputAt := atomic.LoadInt64(&p.LastInputMs)
	p.mu.Lock()
	p.AutoModeLastFiredMs = seedInputAt
	p.mu.Unlock()
	for time.Now().UnixMilli() <= seedInputAt {
		time.Sleep(time.Millisecond)
	}

	createPrompt := codexMcpPermissionPromptFor("create_workflow_task", "wft_create123")
	getPrompt := codexMcpPermissionPromptFor("get_workflow_task", "wft_f0a87099")

	acked1 := fireGridPrompt("create_workflow_task", createPrompt)
	firstAutoInputAt := atomic.LoadInt64(&p.LastInputMs)
	if firstAutoInputAt <= seedInputAt {
		t.Fatalf("first auto-fire LastInputMs = %d, want > seed input %d", firstAutoInputAt, seedInputAt)
	}
	gotDraftLen, gotRecordedAt := p.composerPendingSnapshot()
	if gotDraftLen != draftLen || gotRecordedAt != staleRecordedAt {
		t.Fatalf("composer snapshot after first auto-fire = (%d, %d), want stale (%d, %d)", gotDraftLen, gotRecordedAt, draftLen, staleRecordedAt)
	}
	feedNoPromptFrame()

	// This is the incident regression assertion: v0.0.990 returned busy here
	// because every MCP permission modal shared the numbered-row fingerprint.
	// No computeState call sampled the no-prompt frame above, matching the live
	// 2s/5s poll cadence; the grid feed lifecycle must preserve the edge.
	acked2 := fireGridPrompt("get_workflow_task", getPrompt)
	if acked2 <= acked1 {
		t.Fatalf("different-tool modal did not advance ack: first=%d second=%d", acked1, acked2)
	}
	gotDraftLen, gotRecordedAt = p.composerPendingSnapshot()
	if gotDraftLen != draftLen || gotRecordedAt != staleRecordedAt {
		t.Fatalf("composer snapshot after second auto-fire = (%d, %d), want stale (%d, %d)", gotDraftLen, gotRecordedAt, draftLen, staleRecordedAt)
	}
	feedNoPromptFrame()

	acked3 := fireGridPrompt("identical get_workflow_task repeat", getPrompt)
	if acked3 <= acked2 {
		t.Fatalf("identical-instance turnover did not advance ack: second=%d third=%d", acked2, acked3)
	}
	feedNoPromptFrame()

	// A third identical instance crosses the unchanged-screen fire limit. The
	// committed no-prompt frame proves this is a genuinely new permission
	// request, so prompt-generation turnover must rearm direct auto-fire.
	acked4 := fireGridPrompt("third identical get_workflow_task instance", getPrompt)
	if acked4 <= acked3 {
		t.Fatalf("third identical-instance turnover did not advance ack: third=%d fourth=%d", acked3, acked4)
	}
	feedNoPromptFrame()

	// Generic/model-authored choice menus can be surfaced as choice-prompt but
	// must never cross the approval classifier or write any option.
	generic := strings.Join([]string{
		"Which implementation direction should I take?",
		"› 1. Refactor the parser",
		"  2. Add a compatibility shim",
		"enter to select | esc to cancel",
	}, "\n")
	seedGridMenu(t, p, generic)
	if got := p.computeState(); got != StateChoicePrompt {
		t.Fatalf("generic menu computeState = %q, want surfaced %q", got, StateChoicePrompt)
	}
	a.directAutoFire(context.Background(), p.ID, p.Title)
	assertNoFrame("generic selection menu")
}
