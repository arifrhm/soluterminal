package main

import (
	"encoding/json"
	"net"
	"strings"
	"testing"
	"time"
)

func scheduledNativePane(t *testing.T, id, agent string) (*Pane, <-chan []byte) {
	t.Helper()
	daemonConn, agentConn := net.Pipe()
	p := &Pane{
		ID: id, Kind: "native", Agent: agent, Status: PaneRunning,
		Cwd: "/work", Cmd: []string{agent, "focus"}, CreatedAt: time.Now(),
		agentConn: daemonConn, done: make(chan struct{}), buffer: NewRingBuffer(1024),
		subs: map[int64]*subEntry{},
	}
	if effectiveAgentKind(agent) == agentCodex {
		driver := NewCodexAdapter()
		driver.RestoreThread("thread-"+id, p.Cwd, "gpt-test")
		p.nativeAdapter = driver
	} else {
		p.nativeAdapter = NewClaudeAdapter()
	}
	p.nativeSubs = map[int64]chan NativeEvent{}
	p.nativeStop = make(chan struct{})

	frames := make(chan []byte, 1)
	go func() {
		frameType, payload, err := ReadFrame(agentConn)
		if err == nil && frameType == FrameData {
			frames <- payload
		}
	}()
	t.Cleanup(func() {
		p.shutdownNative()
		_ = daemonConn.Close()
		_ = agentConn.Close()
	})
	return p, frames
}

func nativePromptSchedule(t *testing.T, pane *Pane) (*ScheduleStore, *Schedule) {
	t.Helper()
	mgr := NewManager(t.TempDir())
	mgr.panes[pane.ID] = pane
	store, err := NewScheduleStore(t.TempDir(), mgr, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Stop)
	enabled := false
	sched, err := store.Create(ScheduleInput{
		Name: "Focus prompt", Kind: "prompt", Cron: "*/5 * * * *",
		PaneID: pane.ID, Prompt: "inspect the failing test", Enabled: &enabled,
	})
	if err != nil {
		t.Fatal(err)
	}
	return store, sched
}

func latestNativeScheduleRun(t *testing.T, store *ScheduleStore) ScheduleRun {
	t.Helper()
	listed := store.List()
	if len(listed) != 1 || len(listed[0].Runs) != 1 {
		t.Fatalf("schedule runs=%+v, want exactly one", listed)
	}
	return listed[0].Runs[0]
}

func TestSchedulePromptUsesSemanticFocusSubmissionForClaudeAndCodex(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, agent, wantMethod string
	}{
		{name: "claude", agent: agentClaude, wantMethod: "user"},
		{name: "codex", agent: agentCodex, wantMethod: "turn/start"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pane, frames := scheduledNativePane(t, "scheduled-"+tc.name, tc.agent)
			mgr := NewManager(t.TempDir())
			mgr.panes[pane.ID] = pane
			store, err := NewScheduleStore(t.TempDir(), mgr, nil)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(store.Stop)

			enabled := false
			sched, err := store.Create(ScheduleInput{
				Name: "Focus prompt", Kind: "prompt", Cron: "*/5 * * * *",
				PaneID: pane.ID, Prompt: "inspect the failing test", Enabled: &enabled,
			})
			if err != nil {
				t.Fatal(err)
			}
			store.fire(sched.ID)

			var payload []byte
			select {
			case payload = <-frames:
			case <-time.After(time.Second):
				t.Fatal("scheduled Focus submission wrote no protocol frame")
			}
			var wire map[string]any
			if err := json.Unmarshal(payload, &wire); err != nil {
				t.Fatalf("scheduled payload is not provider JSON: %v; payload=%q", err, payload)
			}
			if tc.agent == agentCodex {
				if wire["method"] != tc.wantMethod {
					t.Fatalf("Codex method=%v, want %s; wire=%v", wire["method"], tc.wantMethod, wire)
				}
				params := wire["params"].(map[string]any)
				input := params["input"].([]any)[0].(map[string]any)
				if input["text"] != "inspect the failing test" {
					t.Fatalf("Codex scheduled text=%v", input["text"])
				}
			} else {
				if wire["type"] != tc.wantMethod {
					t.Fatalf("Claude type=%v, want %s; wire=%v", wire["type"], tc.wantMethod, wire)
				}
				message := wire["message"].(map[string]any)
				if message["content"] != "inspect the failing test" {
					t.Fatalf("Claude scheduled text=%v", message["content"])
				}
			}

			listed := store.List()
			if len(listed) != 1 || len(listed[0].Runs) != 1 || listed[0].Runs[0].Status != "fired" {
				t.Fatalf("schedule run=%+v", listed)
			}
			if got := pane.Snapshot().LastPrompts; len(got) != 1 || got[0] != "inspect the failing test" {
				t.Fatalf("Focus prompt history=%q", got)
			}
			pane.nativeMu.Lock()
			defer pane.nativeMu.Unlock()
			if len(pane.nativeRing) != 1 || pane.nativeRing[0].Type != EvUserText || pane.nativeRing[0].Text != "inspect the failing test" {
				t.Fatalf("Focus input event=%+v", pane.nativeRing)
			}
		})
	}
}

func TestScheduleNativePromptBypassesTerminalTailState(t *testing.T) {
	pane, frames := scheduledNativePane(t, "scheduled-native-tail", agentClaude)
	_, _ = pane.buffer.Write([]byte("Do you want to proceed?\n❯ 1. Yes\n  2. No\n"))
	if state := pane.computeState(); isFireReadyState(state, pane.Status) {
		t.Fatalf("test setup did not create a terminal-only not-ready state: %s", state)
	}
	store, sched := nativePromptSchedule(t, pane)
	store.fire(sched.ID)

	select {
	case <-frames:
	case <-time.After(time.Second):
		t.Fatal("native schedule did not bypass terminal tail state")
	}
	if run := latestNativeScheduleRun(t, store); run.Status != "fired" {
		t.Fatalf("run=%+v, want fired", run)
	}
}

func TestScheduleNativePromptSkipsPendingApproval(t *testing.T) {
	pane, _ := scheduledNativePane(t, "scheduled-native-approval", agentClaude)
	pane.nativePendingCount.Store(1)
	store, sched := nativePromptSchedule(t, pane)
	store.fire(sched.ID)

	run := latestNativeScheduleRun(t, store)
	if run.Status != "skipped" || !strings.Contains(run.Detail, "waiting on approval") {
		t.Fatalf("run=%+v, want skipped waiting on approval", run)
	}
}

func TestScheduleNativePromptSkipsMidTurn(t *testing.T) {
	pane, _ := scheduledNativePane(t, "scheduled-native-turn", agentClaude)
	pane.markNativeUserTurn()
	store, sched := nativePromptSchedule(t, pane)
	store.fire(sched.ID)

	run := latestNativeScheduleRun(t, store)
	if run.Status != "skipped" || !strings.Contains(run.Detail, "mid-turn") {
		t.Fatalf("run=%+v, want skipped mid-turn", run)
	}
}

func TestScheduleNativePromptSkipsExitedPane(t *testing.T) {
	pane, _ := scheduledNativePane(t, "scheduled-native-exited", agentClaude)
	pane.Status = PaneExited
	store, sched := nativePromptSchedule(t, pane)
	store.fire(sched.ID)

	run := latestNativeScheduleRun(t, store)
	if run.Status != "skipped" || !strings.Contains(run.Detail, "exited") {
		t.Fatalf("run=%+v, want skipped exited pane", run)
	}
}

func TestSchedulePromptWithoutManagerRecordsError(t *testing.T) {
	store, err := NewScheduleStore(t.TempDir(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Stop)
	enabled := false
	sched, err := store.Create(ScheduleInput{
		Name: "Prompt without manager", Kind: "prompt", Cron: "* * * * *",
		PaneID: "missing-pane", Prompt: "inspect the failing test", Enabled: &enabled,
	})
	if err != nil {
		t.Fatal(err)
	}

	store.fire(sched.ID)

	run := latestNativeScheduleRun(t, store)
	if run.Status != "error" || !strings.Contains(run.Detail, "pane manager unavailable") {
		t.Fatalf("run=%+v, want pane manager unavailable error", run)
	}
}
