package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// codexTurnTestPane builds a Focus pane wired to a real CodexAdapter, with a
// recording persist hook standing in for paneStore.Put.
func codexTurnTestPane(t *testing.T) (*Pane, *int) {
	t.Helper()
	done := make(chan struct{})
	p := &Pane{
		ID:            "pane_codex_turn",
		Cwd:           "/tmp",
		Title:         "codex",
		Status:        PaneRunning,
		Kind:          "native",
		Agent:         agentCodex,
		CodexThreadID: "019f5443-20e7-7e62-a771-5e2c0560171b",
		Cols:          80,
		Rows:          24,
		done:          done,
		buffer:        NewRingBuffer(1024),
	}
	p.ensureNative()
	persists := 0
	p.onCodexTurnFlagCommit = func(*Pane) { persists++ }
	return p, &persists
}

// The durable flag must arm when a turn starts and disarm when Codex reports an
// end — and each transition must persist, since a flag that only lives in
// memory tells a post-reboot daemon nothing.
func TestCodexTurnInFlightArmsAndDisarmsOnAuthoritativeEdges(t *testing.T) {
	t.Parallel()
	p, persists := codexTurnTestPane(t)

	if p.Snapshot().CodexTurnInFlight {
		t.Fatal("a fresh pane must not claim a turn in flight")
	}

	// A user submit arms it BEFORE any byte streams back: a turn that dies while
	// the model is still thinking is the widest crash window, and it must not be
	// invisible.
	p.markNativeUserTurn()
	if !p.Snapshot().CodexTurnInFlight {
		t.Fatal("user submit must arm the durable turn flag")
	}
	if *persists != 1 {
		t.Fatalf("arming must persist exactly once, got %d", *persists)
	}

	// Streaming events arrive while it is already armed: idempotent, no re-persist.
	p.emitNativeEvent(NativeEvent{Type: EvTextDelta, Text: "thinking..."})
	if got := *persists; got != 1 {
		t.Fatalf("streaming must not re-persist, got %d writes", got)
	}

	// Codex reports the end → disarm + persist.
	p.emitNativeEvent(NativeEvent{Type: EvTurnEnd, Subtype: "success"})
	if p.Snapshot().CodexTurnInFlight {
		t.Fatal("turn_end must disarm the durable turn flag")
	}
	if *persists != 2 {
		t.Fatalf("disarming must persist, got %d writes", *persists)
	}
}

// THE correctness case. Codex reports a user interrupt and a crash-orphaned
// turn IDENTICALLY on resume (both status:"interrupted", both with the same
// turn_aborted rollout record — verified against codex-cli 0.144.4). The flag
// is what separates them: an interrupt REPORTS an end, so it disarms; a crash
// never gets to. If this test ever fails, the notice starts lying to users who
// pressed ESC.
func TestCodexTurnInFlightDisarmsOnUserInterrupt(t *testing.T) {
	t.Parallel()
	p, _ := codexTurnTestPane(t)
	p.markNativeUserTurn()
	if !p.Snapshot().CodexTurnInFlight {
		t.Fatal("precondition: turn should be armed")
	}

	// The user hits ESC: Codex answers turn/completed{status:"interrupted"},
	// which the adapter maps to a turn_end with the interrupted subtype.
	p.emitNativeEvent(NativeEvent{Type: EvTurnEnd, Subtype: codexTurnSubtype("interrupted")})

	if p.Snapshot().CodexTurnInFlight {
		t.Fatal("a REPORTED interrupt must disarm the flag — otherwise a later resume " +
			"tells the user a restart cut off a response they interrupted themselves")
	}
}

// Replayed history (thread/read after a resume) re-renders old turns, including
// the interrupted one. It must never touch the durable flag: seeding it would
// re-arm a turn that is long dead and re-fire the notice forever.
func TestCodexTurnInFlightIgnoresReplayedHistory(t *testing.T) {
	t.Parallel()
	p, persists := codexTurnTestPane(t)

	p.emitNativeEvent(NativeEvent{Type: EvTextDelta, Text: "old", FromReplay: true})
	p.emitNativeEvent(NativeEvent{Type: EvTurnEnd, Subtype: "error_interrupted", FromReplay: true})

	if p.Snapshot().CodexTurnInFlight {
		t.Fatal("replayed history must not arm the durable turn flag")
	}
	if *persists != 0 {
		t.Fatalf("replay must not persist, got %d writes", *persists)
	}
}

// Claude has its own mid-turn recovery (repairDanglingToolUse); the Codex flag
// must stay inert on Claude panes so the two never double-stamp a notice.
func TestCodexTurnInFlightIsInertForClaudePanes(t *testing.T) {
	t.Parallel()
	done := make(chan struct{})
	p := &Pane{
		ID: "pane_claude_native", Cwd: "/tmp", Status: PaneRunning,
		Kind: "native", Agent: "", ClaudeSessionID: "sess-1",
		Cols: 80, Rows: 24, done: done, buffer: NewRingBuffer(1024),
	}
	p.ensureNative()
	persisted := false
	p.onCodexTurnFlagCommit = func(*Pane) { persisted = true }

	p.markNativeUserTurn()
	p.emitNativeEvent(NativeEvent{Type: EvTextDelta, Text: "hi"})

	if p.Snapshot().CodexTurnInFlight {
		t.Fatal("Claude pane must never arm the Codex turn flag")
	}
	if persisted {
		t.Fatal("Claude turn edges must not trigger the Codex flag persist")
	}
}

// A terminal Codex pane has no native parser; the flag must stay inert.
func TestCodexTurnInFlightIsInertForTerminalPanes(t *testing.T) {
	t.Parallel()
	done := make(chan struct{})
	p := &Pane{
		ID: "pane_codex_term", Cwd: "/tmp", Status: PaneRunning,
		Kind: "", Agent: agentCodex, CodexThreadID: "019f5443-20e7-7e62-a771-5e2c0560171b",
		Cols: 80, Rows: 24, done: done, buffer: NewRingBuffer(1024),
	}
	// No ensureNative(): a terminal pane has no adapter.
	p.markNativeUserTurn()
	if p.Snapshot().CodexTurnInFlight {
		t.Fatal("terminal pane must never arm the Codex turn flag")
	}
}

// The flag has to survive the round trip to panes.json, and a suspended pane
// must carry it back so the resume it feeds still knows the turn died.
func TestCodexTurnInFlightPersistsAndRestoresThroughSuspend(t *testing.T) {
	t.Parallel()
	pp := persistedFromSnapshot(PaneSnapshot{
		ID: "p", Kind: "native", Agent: agentCodex,
		CodexThreadID: "019f5443-20e7-7e62-a771-5e2c0560171b", CodexTurnInFlight: true,
	})
	if !pp.CodexTurnInFlight {
		t.Fatal("snapshot → persisted lost the turn flag")
	}

	mgr := NewManager(t.TempDir())
	info := &PersistedPane{
		ID: "p_suspended", Cwd: "/tmp", Kind: "native", Agent: agentCodex,
		CodexThreadID: "019f5443-20e7-7e62-a771-5e2c0560171b", CodexTurnInFlight: true,
		Cols: 80, Rows: 24,
	}
	sp := mgr.SuspendExisting(info)
	if sp == nil {
		t.Fatal("codex pane with a thread id must suspend as resumable")
	}
	snap := sp.Snapshot()
	if !snap.CodexTurnInFlight {
		t.Fatal("SuspendExisting dropped the turn flag — the resume would show no notice")
	}
	// The suspended pane has NO process: it must not read as working.
	if sp.nativeInTurn.Load() {
		t.Fatal("a suspended pane must never render as mid-turn (stuck spinner)")
	}

	// resumeSpawnOpts must carry it into the spawn that stamps the notice.
	opts := resumeSpawnOpts(snap, 100, 30)
	if !opts.CodexTurnInFlight {
		t.Fatal("resumeSpawnOpts dropped the turn flag")
	}
}

// A legacy panes.json entry (written before this field existed) must stay
// byte-identical, and a pane with no in-flight turn must not add the key.
func TestCodexTurnInFlightOmittedWhenUnset(t *testing.T) {
	t.Parallel()
	b, err := json.Marshal(persistedFromSnapshot(PaneSnapshot{ID: "p", Kind: "native", Agent: agentCodex}))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "codexTurnInFlight") {
		t.Fatalf("an idle pane must omit the key: %s", b)
	}
}
