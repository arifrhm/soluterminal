package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type providerTestPane struct {
	*fakeTurnPane
	observation turnProviderObservation
}

func (p *providerTestPane) providerTurnObservation(*turnProviderReader, int64) turnProviderObservation {
	out := p.observation
	p.observation.completed = nil
	p.observation.initial = false
	return out
}
func TestTurnProviderTrackerNoPTYPhantomCompletion(t *testing.T) {
	p := &providerTestPane{fakeTurnPane: &fakeTurnPane{paneID: "p", agentKnd: agentCodex, st: StateBusy}, observation: turnProviderObservation{available: true, source: "codex:" + turnProviderSession}}
	tr, clock := newTestTracker(t, p)
	tr.providerLifecycle = true
	for i := 0; i < 13; i++ {
		p.activity = clock.now().UnixMilli()
		step(tr, clock, 1)
	}
	p.set(StateIdle)
	step(tr, clock, 8)
	if got := tr.summary("p"); got == nil || got.Active || got.Count != 0 || got.Last != nil {
		t.Fatalf("redraw fabricated completion: %+v", got)
	}
	// An old stored heuristic record cannot reappear as authoritative 'done'.
	tr.record("p", turnRecord{StartedMs: 1, EndedMs: 2})
	if got := tr.summary("p"); got.Last != nil {
		t.Fatal("legacy heuristic treated as provider completion")
	}
}
func TestTurnProviderTrackerCanonicalTimeAndRestartDedup(t *testing.T) {
	at := providerTurn{Agent: agentCodex, Session: turnProviderSession, ID: "a", StartedMs: turnProviderStart}
	p := &providerTestPane{fakeTurnPane: &fakeTurnPane{paneID: "p", agentKnd: agentCodex, st: StateBusy}, observation: turnProviderObservation{available: true, source: "codex:" + turnProviderSession, active: &at}}
	tr, clock := newTestTracker(t, p)
	tr.providerLifecycle = true
	clock.t = time.UnixMilli(turnProviderStart + 1000)
	step(tr, clock, 1)
	// Silence and approval menus cannot close a provider-active turn.
	p.set(StateIdle)
	step(tr, clock, 8)
	p.set(StateChoicePrompt)
	step(tr, clock, 3)
	if !tr.summary("p").Active {
		t.Fatal("silence/menu prematurely finished turn")
	}
	ended := at
	ended.EndedMs = turnProviderStart + 13955
	ended.Outcome = "completed"
	p.activity = turnProviderStart + 40*60*1000
	p.observation.active = nil
	p.observation.completed = []providerTurn{ended}
	step(tr, clock, 1)
	got := tr.summary("p")
	if got.Active || got.Count != 1 || got.Last == nil || got.Last.EndedMs != ended.EndedMs || got.Last.Historical {
		t.Fatalf("canonical completion mismatch: %+v", got)
	}
	original, err := os.ReadFile(tr.file("p"))
	if err != nil {
		t.Fatal(err)
	}
	fresh, _ := newTestTracker(t, p)
	fresh.dir = tr.dir
	fresh.providerLifecycle = true
	fresh.lifecycleCutoffMs = ended.EndedMs + 10000
	p.observation.completed = []providerTurn{ended}
	p.observation.initial = true
	fresh.observe(p)
	again := fresh.summary("p")
	if again.Count != 1 || again.Last.EndedMs != ended.EndedMs || again.Last.Historical {
		t.Fatal("restart changed completion/unread")
	}
	after, _ := os.ReadFile(tr.file("p"))
	if string(original) != string(after) {
		t.Fatal("replay rewrote stored completion")
	}
}

type blockedTurnSnapshotter struct {
	started chan struct{}
	release chan struct{}
}

func (s *blockedTurnSnapshotter) snapshot(ctx context.Context, _ string) (turnSnapshot, error) {
	select {
	case s.started <- struct{}{}:
	default:
	}
	select {
	case <-s.release:
		return turnSnapshot{}, nil
	case <-ctx.Done():
		return turnSnapshot{}, ctx.Err()
	}
}
func (*blockedTurnSnapshotter) stat(context.Context, string, string, string) (int, int, int, error) {
	return 0, 0, 0, nil
}
func TestTurnProviderCompletionDoesNotWaitForSnapshots(t *testing.T) {
	at := providerTurn{Agent: agentCodex, Session: turnProviderSession, ID: "a", StartedMs: turnProviderStart}
	p := &providerTestPane{fakeTurnPane: &fakeTurnPane{paneID: "p", agentKnd: agentCodex, st: StateBusy, dir: t.TempDir()}, observation: turnProviderObservation{available: true, source: "codex:" + turnProviderSession, active: &at}}
	tr, clock := newTestTracker(t, p)
	tr.providerLifecycle = true
	clock.t = time.UnixMilli(turnProviderStart + 1000)
	snap := &blockedTurnSnapshotter{started: make(chan struct{}, 1), release: make(chan struct{})}
	tr.snap = snap
	tr.observe(p)
	select {
	case <-snap.started:
	case <-time.After(time.Second):
		t.Fatal("base capture not started")
	}
	ended := at
	ended.EndedMs = turnProviderStart + 1000
	ended.Outcome = "completed"
	p.observation.active = nil
	p.observation.completed = []providerTurn{ended}
	tr.observe(p)
	immediate := tr.summary("p")
	if immediate.Last == nil || immediate.Last.EndedMs != ended.EndedMs || immediate.Last.Note != "Diff capture pending" {
		t.Fatalf("completion waited for capture: %+v", immediate)
	}
	close(snap.release)
	tr.pending.Wait()
	complete := tr.summary("p")
	if complete.Count != 1 || complete.Last.N != immediate.Last.N || complete.Last.EndedMs != immediate.Last.EndedMs || complete.HistoryRevision <= immediate.HistoryRevision {
		t.Fatal("capture renumbered or changed completion time")
	}
}
func TestTurnProviderInitialHistoryDoesNotInventUnreadOrDiff(t *testing.T) {
	ended := providerTurn{Agent: agentCodex, Session: turnProviderSession, ID: "a", StartedMs: turnProviderStart, EndedMs: turnProviderStart + 1000, Outcome: "completed"}
	p := &providerTestPane{fakeTurnPane: &fakeTurnPane{paneID: "p", agentKnd: agentCodex}, observation: turnProviderObservation{available: true, initial: true, source: "codex:" + turnProviderSession, completed: []providerTurn{ended}}}
	tr, clock := newTestTracker(t, p)
	tr.providerLifecycle = true
	tr.lifecycleCutoffMs = ended.EndedMs + 1000
	clock.t = time.UnixMilli(ended.EndedMs + 2000)
	tr.observe(p)
	got := tr.summary("p")
	if got.Last == nil || !got.Last.Historical || got.Last.EndedMs != ended.EndedMs || got.Last.BaseTree != "" || got.Last.Files != 0 {
		t.Fatalf("history reconstruction invented work: %+v", got)
	}
	// A new live conversation cannot inherit another conversation's done pill.
	p.observation.source = "codex:another"
	tr.observe(p)
	if got := tr.summary("p"); got.Last != nil {
		t.Fatal("new conversation inherited stale done")
	}
	if _, err := os.Stat(filepath.Join(tr.dir, "p.json")); err != nil {
		t.Fatal(err)
	}
}

func TestTurnProviderTrackerActiveCheckpointSurvivesHandoff(t *testing.T) {
	at := providerTurn{Agent: agentCodex, Session: turnProviderSession, ID: "active", StartedMs: turnProviderStart}
	p := &providerTestPane{fakeTurnPane: &fakeTurnPane{paneID: "p", agentKnd: agentCodex, st: StateBusy}, observation: turnProviderObservation{available: true, source: "codex:" + turnProviderSession, active: &at,
		checkpoint: &turnReaderCheckpoint{Key: "source", FileID: "inode", Offset: turnSourceReadLimit + 100, Agent: agentCodex, Session: turnProviderSession, Active: &at}}}
	tr, _ := newTestTracker(t, p)
	tr.providerLifecycle = true
	tr.observe(p)
	tr.pending.Wait()
	fresh, _ := newTestTracker(t, p)
	fresh.dir, fresh.providerLifecycle = tr.dir, true
	loaded := fresh.load("p")
	if loaded.active == nil || loaded.active.startedMs != at.StartedMs || loaded.providerReader.restore == nil || loaded.providerReader.restore.Offset != turnSourceReadLimit+100 {
		t.Fatal("handoff lost active boundary or incremental cursor")
	}
	ended := at
	ended.EndedMs, ended.Outcome = turnProviderStart+60000, "completed"
	p.observation.active, p.observation.completed = nil, []providerTurn{ended}
	fresh.observe(p)
	fresh.pending.Wait()
	got := fresh.summary("p")
	if got.Active || got.Count != 1 || got.Last == nil || got.Last.StartedMs != at.StartedMs || got.Last.EndedMs != ended.EndedMs {
		t.Fatalf("handoff changed canonical lifecycle: %+v", got)
	}
}
