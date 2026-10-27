package main

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func testWarden(t *testing.T, enabled bool) (*Warden, *wardenSettingsStore) {
	t.Helper()
	st := &wardenSettingsStore{
		path: t.TempDir() + "/warden_settings.json",
		s:    normalizeWardenSettings(wardenSettings{Enabled: enabled}),
	}
	w := NewWarden(st, NewManager(""), newMcpScopeStore(), newUIBus(), t.TempDir(), ":8787")
	w.resolveBin = func() (string, error) { return "claude-stub", nil } // hermetic: no real install needed
	return w, st
}

func TestWardenMCPConfigAndArgs(t *testing.T) {
	t.Parallel()
	cfg, err := wardenMCPConfigJSON("http://127.0.0.1:8787/mcp", "tok123")
	if err != nil {
		t.Fatal(err)
	}
	var parsed struct {
		MCPServers map[string]struct {
			Type    string            `json:"type"`
			URL     string            `json:"url"`
			Headers map[string]string `json:"headers"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal(cfg, &parsed); err != nil {
		t.Fatal(err)
	}
	srv, ok := parsed.MCPServers["soluterminal"]
	if !ok || srv.Type != "http" || srv.URL != "http://127.0.0.1:8787/mcp" {
		t.Fatalf("config server = %+v", srv)
	}
	if srv.Headers["Authorization"] != "Bearer tok123" {
		t.Fatalf("auth header = %q, want scoped bearer", srv.Headers["Authorization"])
	}

	args := wardenClaudeArgs("/tmp/x.json", "claude-haiku-4-5-20251001", wardenReadAnnotateTools())
	joined := strings.Join(args, " ")
	for _, want := range []string{"-p", "--strict-mcp-config", "--mcp-config /tmp/x.json", "--allowedTools", "--model claude-haiku-4-5-20251001"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("args missing %q: %v", want, args)
		}
	}
	// allowedTools must be MCP-qualified and cover exactly the scoped set,
	// and must NOT include any write/exec tool.
	if !strings.Contains(joined, "mcp__soluterminal__set_pane_flag") {
		t.Fatalf("allowedTools missing the qualified flag tool: %v", args)
	}
	for _, forbidden := range []string{"send_input", "spawn_pane", "kill_pane", "write_file_line", "create_schedule", "answer_prompt", "Bash", "Write"} {
		if strings.Contains(joined, forbidden) {
			t.Fatalf("warden argv leaked a non-read/annotate tool %q: %v", forbidden, args)
		}
	}
}

func TestWardenMCPURL(t *testing.T) {
	t.Parallel()
	// Wildcard/unspecified hosts MUST normalize to loopback — the child dials
	// this, and 0.0.0.0 (the desktop app's --addr) is not a connect address.
	cases := map[string]string{
		":8787":            "http://127.0.0.1:8787/mcp",
		"0.0.0.0:8787":     "http://127.0.0.1:8787/mcp",
		"[::]:8787":        "http://127.0.0.1:8787/mcp",
		"127.0.0.1:9000":   "http://127.0.0.1:9000/mcp",
		"localhost:8787":   "http://localhost:8787/mcp",
		"192.168.1.5:8787": "http://192.168.1.5:8787/mcp",
	}
	for in, want := range cases {
		if got := wardenMCPURL(in); got != want {
			t.Fatalf("wardenMCPURL(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestWardenDisabledCarriesNoState(t *testing.T) {
	t.Parallel()
	w, _ := testWarden(t, false)
	p := testPaneWithTail(t, "pane_dis", "worker", "$ ")
	p.ClaudeSessionID = "sess-1"
	w.mgr.panes[p.ID] = p

	w.tickOnce(context.Background())
	w.mu.Lock()
	n := len(w.tracks)
	w.mu.Unlock()
	if n != 0 {
		t.Fatalf("disabled warden accrued %d tracks, want 0", n)
	}
}

func TestWardenDetectsDebriefOnExit(t *testing.T) {
	t.Parallel()
	w, _ := testWarden(t, true)
	p := testPaneWithTail(t, "pane_db", "builder", strings.Repeat("work output line\n", 300))
	p.ClaudeSessionID = "sess-db"
	atomic.StoreInt64(&p.TotalStrippedChars, 5000)
	w.mgr.panes[p.ID] = p

	// First detect: pane running, tracked as working Claude, no event.
	if evs := w.detect(time.Now()); len(evs) != 0 {
		t.Fatalf("running pane produced events: %+v", evs)
	}
	// Pane exits → exactly one debrief.
	p.Status = PaneExited
	evs := w.detect(time.Now())
	if len(evs) != 1 || evs[0].Kind != wardenDebrief || evs[0].PaneID != p.ID {
		t.Fatalf("exit detect = %+v, want one debrief for %s", evs, p.ID)
	}
	if pending := w.detect(time.Now()); len(pending) != 1 || pending[0].Kind != wardenDebrief {
		t.Fatalf("unreserved debrief was lost: %+v", pending)
	}
	if !w.ackEvent(evs[0]) {
		t.Fatal("valid debrief not acknowledged")
	}
	// Idempotent: a second detect on the still-exited pane fires nothing.
	if evs := w.detect(time.Now()); len(evs) != 0 {
		t.Fatalf("second detect re-fired debrief: %+v", evs)
	}
}

func TestWardenNoDebriefForTrivialOrNonClaude(t *testing.T) {
	t.Parallel()
	w, _ := testWarden(t, true)

	// Non-Claude shell that exits — no debrief.
	sh := testPaneWithTail(t, "pane_sh", "shell", "$ ls\n")
	w.mgr.panes[sh.ID] = sh
	w.detect(time.Now())
	sh.Status = PaneExited
	if evs := w.detect(time.Now()); len(evs) != 0 {
		t.Fatalf("non-Claude shell exit produced a debrief: %+v", evs)
	}

	// Claude pane that produced almost no output — below the floor, no debrief.
	c := testPaneWithTail(t, "pane_trivial", "claude", "hi")
	c.ClaudeSessionID = "sess-x"
	atomic.StoreInt64(&c.TotalStrippedChars, 10)
	w.mgr.panes[c.ID] = c
	w.detect(time.Now())
	c.Status = PaneExited
	if evs := w.detect(time.Now()); len(evs) != 0 {
		t.Fatalf("trivial-output Claude exit produced a debrief: %+v", evs)
	}
}

func TestWardenTriageAfterSustainedNeedsUser(t *testing.T) {
	t.Parallel()
	w, st := testWarden(t, true)
	st.s.TriageAfterMin = 10
	p := testPaneWithTail(t, "pane_tri", "blocked", "❯ 1. Yes")
	p.setAttention(AttentionNeedsUser, "needs a product call")
	w.mgr.panes[p.ID] = p

	base := time.Now()
	// Just entered needs-user — not yet.
	if evs := w.detect(base); len(evs) != 0 {
		t.Fatalf("immediate triage fired: %+v", evs)
	}
	// 5 min in — still not.
	if evs := w.detect(base.Add(5 * time.Minute)); len(evs) != 0 {
		t.Fatalf("early triage fired: %+v", evs)
	}
	// 11 min — triage.
	evs := w.detect(base.Add(11 * time.Minute))
	if len(evs) != 1 || evs[0].Kind != wardenTriage {
		t.Fatalf("sustained triage = %+v, want one triage", evs)
	}
	if !w.ackEvent(evs[0]) {
		t.Fatal("valid triage not acknowledged")
	}
	// Same episode does not re-fire.
	if evs := w.detect(base.Add(12 * time.Minute)); len(evs) != 0 {
		t.Fatalf("triage re-fired same episode: %+v", evs)
	}
	// Attention clears, then a NEW episode re-arms triage.
	p.setAttention(AttentionNone, "")
	w.detect(base.Add(13 * time.Minute))
	p.setAttention(AttentionNeedsUser, "blocked again")
	newBase := base.Add(20 * time.Minute)
	w.detect(newBase)
	if evs := w.detect(newBase.Add(11 * time.Minute)); len(evs) != 1 {
		t.Fatalf("second episode did not re-trigger triage: %+v", evs)
	}
}

func TestWardenSingleFlightAndHourlyCap(t *testing.T) {
	t.Parallel()
	w, st := testWarden(t, true)
	st.s.MaxRunsPerHour = 2
	fixed := time.Now()
	w.clock = func() time.Time { return fixed }

	if !w.tryReserveSlot() {
		t.Fatal("first reservation refused")
	}
	// Single-flight: second reservation refused while one is running.
	if w.tryReserveSlot() {
		t.Fatal("single-flight breached — second reservation allowed while running")
	}
	w.releaseSlot()
	// Second run within the hour is allowed (cap is 2).
	if !w.tryReserveSlot() {
		t.Fatal("second run within cap refused")
	}
	w.releaseSlot()
	// Third within the same hour hits the cap.
	if w.tryReserveSlot() {
		t.Fatal("hourly cap breached — third run allowed")
	}
	// An hour later the window clears.
	w.clock = func() time.Time { return fixed.Add(61 * time.Minute) }
	if !w.tryReserveSlot() {
		t.Fatal("run refused after the hourly window rolled")
	}
}

func TestWardenTriageRetriesBusyAndCapThenStopsAfterResolution(t *testing.T) {
	for _, mode := range []string{"busy", "cap"} {
		t.Run(mode, func(t *testing.T) {
			w, st := testWarden(t, true)
			st.s.TriageAfterMin = 2
			st.s.MaxRunsPerHour = 1
			p := testPaneWithTail(t, "pane-retry", "worker", "$ ")
			p.setAttention(AttentionNeedsUser, "decision")
			w.mgr.panes[p.ID] = p
			base := time.Now()
			var nanos atomic.Int64
			nanos.Store(base.UnixNano())
			w.clock = func() time.Time { return time.Unix(0, nanos.Load()) }
			fired := make(chan struct{}, 2)
			w.exec = func(context.Context, string, []string, string, string) execResult {
				fired <- struct{}{}
				return execResult{}
			}
			w.tickOnce(context.Background())
			nanos.Store(base.Add(3 * time.Minute).UnixNano())
			if !w.tryReserveSlot() {
				t.Fatal("fixture reservation refused")
			}
			if mode == "cap" {
				w.releaseSlot()
			}
			w.tickOnce(context.Background())
			select {
			case <-fired:
				t.Fatal("run fired while blocked")
			default:
			}
			if mode == "busy" {
				w.unreserveSlot()
			} else {
				nanos.Store(base.Add(64 * time.Minute).UnixNano())
			}
			w.tickOnce(context.Background())
			select {
			case <-fired:
			case <-time.After(2 * time.Second):
				t.Fatal("pending triage was not retried")
			}
			deadline := time.Now().Add(2 * time.Second)
			for time.Now().Before(deadline) {
				w.mu.Lock()
				running := w.running
				w.mu.Unlock()
				if !running {
					break
				}
				time.Sleep(time.Millisecond)
			}
			w.tickOnce(context.Background())
			select {
			case <-fired:
				t.Fatal("same episode ran twice")
			default:
			}
			p.setAttention(AttentionNone, "")
			w.tickOnce(context.Background())
			select {
			case <-fired:
				t.Fatal("resolved episode retried")
			default:
			}
		})
	}
}

func TestWardenBatchKeepsSecondEventPending(t *testing.T) {
	w, st := testWarden(t, true)
	st.s.TriageAfterMin = 2
	base := time.Now()
	var nanos atomic.Int64
	nanos.Store(base.UnixNano())
	w.clock = func() time.Time { return time.Unix(0, nanos.Load()) }
	for _, id := range []string{"pane-batch-a", "pane-batch-b"} {
		p := testPaneWithTail(t, id, "worker", "$ ")
		p.setAttention(AttentionNeedsUser, "decision")
		w.mgr.panes[id] = p
	}
	started := make(chan struct{}, 2)
	resume := make(chan struct{})
	w.exec = func(context.Context, string, []string, string, string) execResult {
		started <- struct{}{}
		<-resume
		return execResult{}
	}
	w.tickOnce(context.Background())
	nanos.Store(base.Add(3 * time.Minute).UnixNano())
	w.tickOnce(context.Background())
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("first batch event did not start")
	}
	if evs := w.detect(w.now()); len(evs) != 1 {
		t.Fatalf("pending batch events = %+v, want one", evs)
	}
	resume <- struct{}{}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		w.mu.Lock()
		running := w.running
		w.mu.Unlock()
		if !running {
			break
		}
		time.Sleep(time.Millisecond)
	}
	w.tickOnce(context.Background())
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("second batch event was lost")
	}
	resume <- struct{}{}
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		w.mu.Lock()
		running := w.running
		w.mu.Unlock()
		if !running {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("second batch event did not finish")
}

func TestWardenResolvedBeforeRetryDoesNotRun(t *testing.T) {
	w, st := testWarden(t, true)
	st.s.TriageAfterMin = 2
	p := testPaneWithTail(t, "pane-resolved", "worker", "$ ")
	p.setAttention(AttentionNeedsUser, "decision")
	w.mgr.panes[p.ID] = p
	base := time.Now()
	var nanos atomic.Int64
	nanos.Store(base.UnixNano())
	w.clock = func() time.Time { return time.Unix(0, nanos.Load()) }
	called := make(chan struct{}, 1)
	w.exec = func(context.Context, string, []string, string, string) execResult {
		called <- struct{}{}
		return execResult{}
	}
	w.tickOnce(context.Background())
	nanos.Store(base.Add(3 * time.Minute).UnixNano())
	if !w.tryReserveSlot() {
		t.Fatal("fixture busy reservation refused")
	}
	w.tickOnce(context.Background())
	p.setAttention(AttentionNone, "")
	w.unreserveSlot()
	w.tickOnce(context.Background())
	select {
	case <-called:
		t.Fatal("resolved episode ran")
	default:
	}
}

// End-to-end through tickOnce with a fake exec: an enabled warden with a
// freshly-exited Claude pane runs exactly one debrief and mints a correctly-
// scoped token that the real /mcp handler would honor.
func TestWardenTickSpawnsScopedRun(t *testing.T) {
	t.Parallel()
	w, _ := testWarden(t, true)
	var (
		mu       sync.Mutex
		calls    int
		gotArgs  []string
		gotStdin string
	)
	w.exec = func(ctx context.Context, bin string, args []string, cwd, prompt string) execResult {
		mu.Lock()
		calls++
		gotArgs = args
		gotStdin = prompt
		mu.Unlock()
		return execResult{output: []byte("OUTCOME: shipped X.\n")}
	}

	p := testPaneWithTail(t, "pane_e2e", "builder", strings.Repeat("line\n", 400))
	p.ClaudeSessionID = "sess-e2e"
	atomic.StoreInt64(&p.TotalStrippedChars, 6000)
	w.mgr.panes[p.ID] = p
	w.tickOnce(context.Background()) // sees it running
	p.Status = PaneExited

	done := make(chan struct{})
	w.onRun = func(ev wardenEvent, start time.Time, res execResult, logFile string) { close(done) }
	w.tickOnce(context.Background()) // detects exit, spawns

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("warden run did not complete")
	}
	mu.Lock()
	defer mu.Unlock()
	if calls != 1 {
		t.Fatalf("exec called %d times, want 1", calls)
	}
	if !strings.Contains(gotStdin, "DEBRIEF") {
		t.Fatalf("prompt was not a debrief: %q", gotStdin)
	}
	// The --mcp-config file the run was handed must carry a token that
	// resolves to the read+annotate scope in the SAME store /mcp checks.
	joined := strings.Join(gotArgs, " ")
	if !strings.Contains(joined, "--strict-mcp-config") {
		t.Fatalf("run argv missing --strict-mcp-config: %v", gotArgs)
	}
}

func TestWardenPersistsRunHistory(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	st := &wardenSettingsStore{path: dir + "/warden_settings.json", s: normalizeWardenSettings(wardenSettings{})}
	w := NewWarden(st, NewManager(""), newMcpScopeStore(), newUIBus(), dir, ":8787")
	w.recordRun(wardenRunRecord{Kind: wardenBrief, At: time.Now(), Status: "ok", Summary: "did stuff", LogFile: "run-1.log"})
	w.recordRun(wardenRunRecord{Kind: wardenDebrief, At: time.Now(), Status: "ok", LogFile: "run-2.log"})
	if len(w.RecentRuns()) != 2 {
		t.Fatalf("recorded runs = %d, want 2", len(w.RecentRuns()))
	}

	// A fresh Warden on the SAME dataDir (simulating a daemon restart/update)
	// must restore the run history from disk — the whole point of persistence.
	w2 := NewWarden(st, NewManager(""), newMcpScopeStore(), newUIBus(), dir, ":8787")
	got := w2.RecentRuns()
	if len(got) != 2 {
		t.Fatalf("restored runs = %d, want 2 (history lost across restart)", len(got))
	}
	if got[0].Kind != wardenDebrief || got[1].Kind != wardenBrief {
		t.Fatalf("newest-first order lost after restore: %+v", got)
	}
	if got[1].Summary != "did stuff" || got[1].LogFile != "run-1.log" {
		t.Fatalf("run fields not restored: %+v", got[1])
	}
}

func TestWardenLoadRunsMissingOrCorruptIsEmpty(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	st := &wardenSettingsStore{path: dir + "/warden_settings.json", s: normalizeWardenSettings(wardenSettings{})}
	// No index yet — starts empty, no panic.
	w := NewWarden(st, NewManager(""), newMcpScopeStore(), newUIBus(), dir, ":8787")
	if len(w.RecentRuns()) != 0 {
		t.Fatalf("fresh warden has %d runs, want 0", len(w.RecentRuns()))
	}
	// Corrupt index — best-effort load leaves it empty rather than crashing.
	if err := os.MkdirAll(w.runDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(w.runsIndexPath(), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	w2 := NewWarden(st, NewManager(""), newMcpScopeStore(), newUIBus(), dir, ":8787")
	if len(w2.RecentRuns()) != 0 {
		t.Fatalf("corrupt index yielded %d runs, want 0", len(w2.RecentRuns()))
	}
}

// A pending event waiting on a busy/capped Warden is retried every tick; it
// must be logged and published once per episode, not every 15s.
func TestWardenSkipReportedOncePerEpisode(t *testing.T) {
	w, _ := testWarden(t, true)
	ev := wardenEvent{Kind: "triage", PaneID: "p1", Episode: "e1"}
	if !w.firstSkip(ev) || w.firstSkip(ev) || w.firstSkip(ev) {
		t.Fatal("a skipped event must be reported exactly once")
	}
	if !w.firstSkip(wardenEvent{Kind: "triage", PaneID: "p1", Episode: "e2"}) {
		t.Fatal("a new episode must be reported again")
	}
	w.pruneSkipReports([]wardenEvent{{Kind: "triage", PaneID: "p1", Episode: "e2"}})
	if !w.firstSkip(ev) {
		t.Fatal("pruned (no longer pending) events must be reportable again")
	}
	if len(w.skipReported) != 2 {
		t.Fatalf("skip reports not bounded to pending events: %d", len(w.skipReported))
	}
}
