package main

import (
	"context"
	"encoding/json"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func attachDirectAutoTestPipe(t *testing.T, p *Pane) net.Conn {
	t.Helper()
	paneEnd, agentEnd := net.Pipe()
	p.agentConn = paneEnd
	p.done = make(chan struct{})
	t.Cleanup(func() {
		p.shutdownGrid()
		close(p.done)
		_ = paneEnd.Close()
		_ = agentEnd.Close()
	})
	return agentEnd
}

func TestDirectAutoFireApprovesCodexMcpPermissionMenu(t *testing.T) {
	t.Parallel()
	p := testPaneWithTail(t, "pane-codex-mcp", "codex", codexMcpPermissionObservedPrompt+"\n")
	p.AutoMode = "always"
	p.forceCodexForTest = true
	agentEnd := attachDirectAutoTestPipe(t, p)

	if got := p.computeState(); got != StateChoicePrompt {
		t.Fatalf("computeState = %s, want %s", got, StateChoicePrompt)
	}

	type frameRead struct {
		type_   FrameType
		payload []byte
		err     error
	}
	reads := make(chan frameRead, 2)
	go func() {
		for i := 0; i < 2; i++ {
			_ = agentEnd.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
			type_, payload, err := ReadFrame(agentEnd)
			reads <- frameRead{type_: type_, payload: payload, err: err}
			if err != nil {
				return
			}
		}
	}()

	a := &Aria{
		mgr:          testManagerWithPane(p),
		uiBus:        newUIBus(),
		confirms:     map[string]*ariaPendingConfirm{},
		sweepStreaks: map[string]*sweepStreak{},
		evalLog:      newAriaEvalLog(),
	}
	a.directAutoFire(context.Background(), p.ID, p.Title)

	first := <-reads
	if first.err != nil {
		t.Fatalf("first frame read: %v", first.err)
	}
	if first.type_ != FrameData {
		t.Fatalf("first frame type = %v, want %v", first.type_, FrameData)
	}
	if got := string(first.payload); got != "\r" {
		t.Fatalf("first frame payload = %q, want bare Enter", got)
	}
	second := <-reads
	if second.err == nil {
		t.Fatalf("unexpected second frame: type=%v payload=%q", second.type_, second.payload)
	}
	if timeoutErr, ok := second.err.(net.Error); !ok || !timeoutErr.Timeout() {
		t.Fatalf("second read error = %v, want timeout proving no second write", second.err)
	}

	outcomes := a.recentEvalOutcomes(p.ID)
	if len(outcomes) != 1 {
		t.Fatalf("eval outcomes = %d, want exactly 1", len(outcomes))
	}
	if got := outcomes[0]; got.Kind != "fired" || got.Model != "direct" {
		t.Fatalf("eval outcome = %+v, want fired/direct", got)
	}
}

func TestDirectAutoFireDeclinesGenericMenuOnCodexPane(t *testing.T) {
	t.Parallel()
	p := testPaneWithTail(t, "pane-codex-generic", "codex", strings.Join([]string{
		"How should I proceed?",
		"❯ 1. Option A",
		"  2. Option B",
	}, "\n")+"\n")
	p.AutoMode = "always"
	p.forceCodexForTest = true
	agentEnd := attachDirectAutoTestPipe(t, p)

	if got := p.computeState(); got != StateChoicePrompt {
		t.Fatalf("computeState = %s, want %s", got, StateChoicePrompt)
	}

	a := &Aria{
		mgr:          testManagerWithPane(p),
		uiBus:        newUIBus(),
		confirms:     map[string]*ariaPendingConfirm{},
		sweepStreaks: map[string]*sweepStreak{},
		evalLog:      newAriaEvalLog(),
	}
	a.directAutoFire(context.Background(), p.ID, p.Title)

	_ = agentEnd.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
	if type_, payload, err := ReadFrame(agentEnd); err == nil {
		t.Fatalf("generic menu received frame type=%v payload=%q, want no write", type_, payload)
	} else if timeoutErr, ok := err.(net.Error); !ok || !timeoutErr.Timeout() {
		t.Fatalf("generic-menu read error = %v, want timeout proving no write", err)
	}

	outcomes := a.recentEvalOutcomes(p.ID)
	if len(outcomes) != 1 {
		t.Fatalf("eval outcomes = %d, want exactly 1", len(outcomes))
	}
	if got := outcomes[0]; got.Kind != "declined" || got.Reason != "choice menu, not a permission prompt" {
		t.Fatalf("eval outcome = %+v, want declined/choice menu, not a permission prompt", got)
	}
	if got := a.BackoffStreak(p.ID); got != 1 {
		t.Fatalf("BackoffStreak = %d, want 1", got)
	}
}

// TestRecordDirectDeclineSurfacedVsBuried covers the escape hatch that keeps
// a multi-subagent burst from parking an always-mode pane. directAutoFire's
// "not an approval prompt" recheck must bump the sweeper-suspend streak ONLY
// when a menu is genuinely present (a real generic-menu decline); when the
// menu vanished/was buried between admit and recheck it must record neutrally,
// or 3 transient misses would suspend the sweeper on a pane that still has a
// live, unanswered menu.
func TestRecordDirectDeclineSurfacedVsBuried(t *testing.T) {
	t.Parallel()
	// A menu IS present → genuine decline → bump the streak.
	menuPane := testPaneWithTail(t, "pane-menu", "menu", strings.Join([]string{
		"How should I proceed?",
		"❯ 1. Option A",
		"  2. Option B",
	}, "\n")+"\n")
	aMenu := &Aria{
		mgr:          testManagerWithPane(menuPane),
		uiBus:        newUIBus(),
		sweepStreaks: map[string]*sweepStreak{},
	}
	aMenu.recordDirectDecline(menuPane.ID, menuPane)
	if got := aMenu.BackoffStreak(menuPane.ID); got != 1 {
		t.Fatalf("menu present: BackoffStreak = %d, want 1 (genuine decline should bump streak)", got)
	}

	// No menu present (pane returned to thinking between admit and recheck)
	// → surfacing miss → must NOT bump the streak even repeated past the
	// suspend threshold.
	thinkPane := testPaneWithTail(t, "pane-think", "think",
		"✶ Wibbling… still thinking with xhigh effort\n")
	aThink := &Aria{
		mgr:          testManagerWithPane(thinkPane),
		uiBus:        newUIBus(),
		sweepStreaks: map[string]*sweepStreak{},
	}
	for i := 0; i < ariaSweeperSuspendThreshold+1; i++ {
		aThink.recordDirectDecline(thinkPane.ID, thinkPane)
	}
	if got := aThink.BackoffStreak(thinkPane.ID); got != 0 {
		t.Fatalf("no menu present: BackoffStreak = %d, want 0 (surfacing miss must not park the pane)", got)
	}
}

func TestAriaAutonomousToolsOnlySendInput(t *testing.T) {
	t.Parallel()
	tools := ariaAutonomousTools()
	if len(tools) != 1 {
		t.Fatalf("len(ariaAutonomousTools()) = %d, want 1", len(tools))
	}
	if tools[0].Name != "send_input" {
		t.Fatalf("autonomous tool = %q, want send_input", tools[0].Name)
	}
}

func TestExecuteToolAutonomousRejectsNonSendInput(t *testing.T) {
	t.Parallel()
	a := &Aria{}
	names := []string{
		"list_panes",
		"read_pane_tail",
		"spawn_pane",
		"kill_pane",
		"set_standing_order",
		"remove_standing_order",
		"delete_workspace",
	}
	for _, name := range names {
		got := a.executeTool(context.Background(), "tu_1", name, json.RawMessage(`{}`),
			ariaQueryReq{Autonomous: true, AutonomousPaneID: "pane_ok"}, func(map[string]any) {})
		if !strings.Contains(got, "autonomous ARIA turns may only call send_input") {
			t.Fatalf("executeTool(%s) = %q, want autonomous rejection", name, got)
		}
	}
}

func TestExecuteToolAutonomousRejectsWrongPane(t *testing.T) {
	t.Parallel()
	a := &Aria{}
	got := a.executeTool(
		context.Background(),
		"tu_1",
		"send_input",
		json.RawMessage(`{"pane_id":"pane_other","input":"1\n"}`),
		ariaQueryReq{Autonomous: true, AutonomousPaneID: "pane_expected"},
		func(map[string]any) {},
	)
	if !strings.Contains(got, `autonomous send_input may only target pane "pane_expected"`) {
		t.Fatalf("executeTool wrong pane = %q", got)
	}
}

func TestAutoEmitterRefusesUnsafeConfirm(t *testing.T) {
	t.Parallel()
	bus := newUIBus()
	a := &Aria{
		uiBus:    bus,
		confirms: map[string]*ariaPendingConfirm{},
	}
	pc := &ariaPendingConfirm{ch: make(chan bool, 1)}
	a.confirms["cf_unsafe"] = pc

	emit := a.autoEmitter("pane_expected", "Expected", "order_1", 5000)
	emit(map[string]any{
		"type": "needs_confirm",
		"id":   "cf_unsafe",
		"tool": "kill_pane",
	})

	select {
	case approved := <-pc.ch:
		if approved {
			t.Fatal("unsafe confirm was approved")
		}
	default:
		t.Fatal("unsafe confirm was not resolved")
	}

	foundErr := false
	for _, e := range bus.Recent() {
		if e.Type == "aria-auto-error" && strings.Contains(e.Text, "refused unsafe autonomous tool request") {
			foundErr = true
		}
	}
	if !foundErr {
		t.Fatalf("aria-auto-error event not published; got %#v", bus.Recent())
	}
}

func TestToolSendInputAutonomousSkipsPromptMismatch(t *testing.T) {
	t.Parallel()
	p := testPaneWithTail(t, "pane_1", "Pane 1", strings.Join([]string{
		"Would you like to proceed?",
		"› 1. Yes",
		"  2. No",
	}, "\n"))
	if got := p.computeState(); got != StateChoicePrompt {
		t.Fatalf("computeState = %s, want choice-prompt", got)
	}
	currentPos := atomic.LoadInt64(&p.LastAckedChoicePromptPos)
	if currentPos == 0 {
		t.Fatal("LastAckedChoicePromptPos was not set")
	}

	a := &Aria{mgr: testManagerWithPane(p), uiBus: newUIBus(), confirms: map[string]*ariaPendingConfirm{}}
	events := make(chan map[string]any, 4)
	done := make(chan string, 1)
	go func() {
		done <- a.toolSendInput(
			context.Background(),
			"tu_1",
			p.ID,
			"1\n",
			currentPos-1,
			choicePromptFingerprint(tailOfPane(p, 30)),
			false,
			func(evt map[string]any) { events <- evt },
		)
	}()
	confirmID := waitNeedsConfirm(t, events, "send_input")
	if !a.signalConfirm(confirmID, true, false) {
		t.Fatal("signalConfirm returned false")
	}
	got := <-done
	if !strings.Contains(got, "pane prompt changed") {
		t.Fatalf("toolSendInput = %q, want prompt changed skip", got)
	}
	evt := <-events
	if errText, _ := evt["error"].(string); !strings.Contains(errText, "pane prompt changed") {
		t.Fatalf("event = %#v, want prompt changed error", evt)
	}
}

func TestToolSendInputCautiousDeniesRiskyPromptBeforeConfirm(t *testing.T) {
	t.Parallel()
	p := testPaneWithTail(t, "pane_1", "Pane 1", strings.Join([]string{
		"Would you like to run the following command?",
		"$ git push origin main",
		"› 1. Yes, proceed (y)",
		"  2. No, and tell Codex what to do differently (esc)",
	}, "\n"))
	p.SetAutoMode("cautious")
	if got := p.computeState(); got != StateChoicePrompt {
		t.Fatalf("computeState = %s, want choice-prompt", got)
	}
	currentPos := atomic.LoadInt64(&p.LastAckedChoicePromptPos)

	a := &Aria{mgr: testManagerWithPane(p), uiBus: newUIBus()}
	var events []map[string]any
	got := a.toolSendInput(
		context.Background(),
		"tu_1",
		p.ID,
		"1\n",
		currentPos,
		choicePromptFingerprint(tailOfPane(p, 30)),
		true,
		func(evt map[string]any) { events = append(events, evt) },
	)
	if !strings.Contains(got, "cautious auto-approval denied") {
		t.Fatalf("toolSendInput = %q, want cautious denial", got)
	}
	if len(events) != 0 {
		t.Fatalf("events = %#v, want no confirm armed", events)
	}
}
