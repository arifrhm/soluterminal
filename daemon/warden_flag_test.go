package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type failingFirstFlagStore struct {
	entered chan struct{}
	release chan struct{}
	calls   atomic.Int32
}

type blockingFlagStore struct {
	*PaneStore
	entered chan struct{}
	release chan struct{}
	removed chan struct{}
	calls   atomic.Int32
}

func (s *blockingFlagStore) Put(p *Pane) error {
	if s.calls.Add(1) == 1 {
		close(s.entered)
		<-s.release
	}
	return s.PaneStore.Put(p)
}

func (s *blockingFlagStore) Remove(id string) error {
	close(s.removed)
	return s.PaneStore.Remove(id)
}

func TestFlagPersistenceSerializesReplacementAndRollback(t *testing.T) {
	for _, rollback := range []bool{false, true} {
		t.Run(fmt.Sprintf("rollback=%v", rollback), func(t *testing.T) {
			mgr := NewManager(t.TempDir())
			old := testPaneWithTail(t, "pane-transaction", "old", "$ ")
			mgr.panes[old.ID] = old
			persist, err := NewPaneStore(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			store := &blockingFlagStore{PaneStore: persist, entered: make(chan struct{}), release: make(chan struct{}), removed: make(chan struct{})}
			defer func() {
				select {
				case <-store.release:
				default:
					close(store.release)
				}
			}()
			flagDone := make(chan error, 1)
			go func() { flagDone <- mgr.changeCurrentFlag(old, store, nil, true, "old reminder", "user", "") }()
			select {
			case <-store.entered:
			case <-time.After(2 * time.Second):
				t.Fatal("flag did not enter persistence")
			}
			started, finished := make(chan struct{}), make(chan struct{})
			replacement := testPaneWithTail(t, old.ID, "replacement", "$ ")
			go func() {
				defer close(finished)
				if rollback {
					a := &apiServer{mgr: mgr, panes: store, killPane: func(id string) error {
						close(started)
						mgr.mu.Lock()
						delete(mgr.panes, id)
						mgr.mu.Unlock()
						return nil
					}}
					a.rollbackSpawnedPane(old)
				} else {
					close(started)
					mgr.mu.Lock()
					mgr.panes[old.ID] = replacement
					mgr.mu.Unlock()
					if err := store.Put(replacement); err != nil {
						t.Error(err)
					}
				}
			}()
			select {
			case <-started:
			case <-time.After(2 * time.Second):
				t.Fatal("lifecycle change did not start")
			}
			select {
			case <-finished:
				t.Fatal("lifecycle change crossed in-flight persistence")
			case <-store.removed:
				t.Fatal("rollback removed persistence before fencing old writes")
			case <-time.After(10 * time.Millisecond):
			}
			close(store.release)
			select {
			case err := <-flagDone:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("flag transaction stuck")
			}
			select {
			case <-finished:
			case <-time.After(2 * time.Second):
				t.Fatal("lifecycle change stuck")
			}
			saved := store.List()
			if rollback {
				if len(saved) != 0 {
					t.Fatalf("rollback resurrected pane: %+v", saved)
				}
			} else if len(saved) != 1 || saved[0].Title != "replacement" || saved[0].Flagged {
				t.Fatalf("old flag overwrote replacement: %+v", saved)
			}
		})
	}
}

func (s *failingFirstFlagStore) List() []PersistedPane { return nil }
func (s *failingFirstFlagStore) Remove(string) error   { return nil }
func (s *failingFirstFlagStore) Put(*Pane) error {
	if s.calls.Add(1) == 1 {
		close(s.entered)
		<-s.release
		return errors.New("disk full")
	}
	return nil
}

func testWardenFlagScope(p *Pane) *mcpScope {
	_, _, episode := p.attentionEpisodeNow()
	return &mcpScope{Identity: wardenScopeIdentity, Tools: map[string]bool{"set_pane_flag": true}, WardenPaneID: p.ID, WardenEpisode: episode, Expires: time.Now().Add(time.Minute)}
}

func TestWardenCapturedPaneCannotOverwriteReplacementOrResurrectDeleted(t *testing.T) {
	for _, replaced := range []bool{true, false} {
		t.Run(fmt.Sprintf("replaced=%v", replaced), func(t *testing.T) {
			w, _ := testWarden(t, false)
			old := testPaneWithTail(t, "pane-captured", "old worker", "$ ")
			old.setAttention(AttentionNeedsUser, "question")
			_, _, episode := old.attentionEpisodeNow()
			if err := old.changeFlag(nil, nil, true, "automatic", wardenScopeIdentity, episode); err != nil {
				t.Fatal(err)
			}
			old.setAttention(AttentionNone, "")
			w.mgr.panes[old.ID] = old
			captured := w.mgr.List()[0]
			store, err := NewPaneStore(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			w.panes = store
			if replaced {
				next := testPaneWithTail(t, old.ID, "replacement", "$ ")
				next.SetFlagAs(true, "keep my reminder", "user")
				w.mgr.panes[old.ID] = next
				if err := store.Put(next); err != nil {
					t.Fatal(err)
				}
			} else {
				delete(w.mgr.panes, old.ID)
			}
			w.cleanupWardenFlag(captured)
			if saved := store.List(); replaced {
				if len(saved) != 1 || saved[0].Title != "replacement" || saved[0].FlagOwner != "user" || saved[0].FlagMemo != "keep my reminder" {
					t.Fatalf("stale cleanup overwrote replacement: %+v", saved)
				}
			} else if len(saved) != 0 {
				t.Fatalf("stale cleanup resurrected a deleted pane: %+v", saved)
			}
		})
	}
}

func TestWardenFlagBoundaryAndEpisodeCleanup(t *testing.T) {
	t.Parallel()
	w, st := testWarden(t, false)
	p := testPaneWithTail(t, "pane-auto", "worker", "$ ")
	other := testPaneWithTail(t, "pane-other", "worker", "$ ")
	p.setAttention(AttentionNeedsUser, "decision")
	other.setAttention(AttentionNeedsUser, "another decision")
	w.mgr.panes[p.ID], w.mgr.panes[other.ID] = p, other
	store, err := NewPaneStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	w.panes = store
	a := &apiServer{mgr: w.mgr, panes: store, wardenSettings: st}
	scope := testWardenFlagScope(p)
	set := func(sc *mcpScope, target *Pane, flagged bool) (string, bool) {
		return mcpResultText(t, scopedCallForTest(t, a, sc, "set_pane_flag", map[string]any{"pane_id": target.ID, "flagged": flagged, "memo": "choose A"}))
	}
	if _, bad := set(scope, p, true); !bad {
		t.Fatal("default-off Warden flag succeeded")
	}
	if snap := p.Snapshot(); snap.Flagged {
		t.Fatalf("off setting created flag: %+v", snap)
	}
	if _, err := st.Patch(wardenSettingsPatch{AutoFlags: boolPtr(true)}); err != nil {
		t.Fatal(err)
	}
	if _, bad := set(scope, other, true); !bad {
		t.Fatal("wrong-pane Warden flag succeeded")
	}
	if _, bad := set(&mcpScope{Identity: wardenScopeIdentity, Tools: scope.Tools}, p, true); !bad {
		t.Fatal("brief/debrief Warden flag succeeded")
	}
	if msg, bad := set(scope, p, true); bad {
		t.Fatalf("opted-in triage flag refused: %s", msg)
	}
	snap := p.Snapshot()
	if !snap.Flagged || snap.FlagOwner != wardenScopeIdentity || snap.FlagEpisode != scope.WardenEpisode || snap.FlagRevision == "" {
		t.Fatalf("auto flag marker = %+v", snap)
	}
	if _, bad := set(scope, p, true); !bad {
		t.Fatal("same-episode replacement succeeded")
	}
	otherScope := testWardenFlagScope(other)
	if _, err := st.Patch(wardenSettingsPatch{AutoFlags: boolPtr(false)}); err != nil {
		t.Fatal(err)
	}
	if msg, bad := set(otherScope, other, true); !bad || !strings.Contains(msg, "disabled") {
		t.Fatalf("opted-out token on its active unflagged pane = %q error=%v", msg, bad)
	}
	p.setAttention(AttentionNone, "")
	w.tickOnce(context.Background()) // master off must still clean the prior flag
	if got := p.Snapshot(); got.Flagged {
		t.Fatalf("episode exit did not clean marked flag: %+v", got)
	}
	if _, err := st.Patch(wardenSettingsPatch{AutoFlags: boolPtr(true)}); err != nil {
		t.Fatal(err)
	}
	if msg, bad := set(scope, p, true); !bad || !strings.Contains(msg, "no longer active") {
		t.Fatalf("stale run with flags enabled = %q error=%v", msg, bad)
	}
	if _, err := st.Patch(wardenSettingsPatch{AutoFlags: boolPtr(false)}); err != nil {
		t.Fatal(err)
	}
	// Manual and other scoped callers remain usable while automatic flags are off.
	if msg, bad := set(nil, p, true); bad {
		t.Fatalf("manual MCP flag refused: %s", msg)
	}
	if got := p.Snapshot(); got.FlagOwner != "agent" {
		t.Fatalf("manual MCP owner = %q", got.FlagOwner)
	}
	p.SetFlag(false, "")
	reviewer := &mcpScope{Identity: "reviewer", Tools: scope.Tools, Expires: time.Now().Add(time.Minute)}
	if msg, bad := set(reviewer, p, true); bad {
		t.Fatalf("other scoped caller refused: %s", msg)
	}
	if got := p.Snapshot(); got.FlagOwner != "reviewer" {
		t.Fatalf("other owner = %q", got.FlagOwner)
	}
}

func TestWardenFlagCleanupPreservesReplacementsAndRestart(t *testing.T) {
	t.Parallel()
	w, st := testWarden(t, false)
	p := testPaneWithTail(t, "pane-restart", "worker", "$ ")
	p.setAttention(AttentionNeedsUser, "decision")
	w.mgr.panes[p.ID] = p
	store, err := NewPaneStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	w.panes = store
	a := &apiServer{mgr: w.mgr, panes: store, wardenSettings: st}
	_, _ = st.Patch(wardenSettingsPatch{AutoFlags: boolPtr(true)})
	scope := testWardenFlagScope(p)
	create := func() {
		t.Helper()
		if msg, bad := mcpResultText(t, scopedCallForTest(t, a, scope, "set_pane_flag", map[string]any{"pane_id": p.ID, "flagged": true, "memo": "choose A"})); bad {
			t.Fatal(msg)
		}
	}
	create()
	marked := p.Snapshot()
	if len(store.List()) != 1 || store.List()[0].FlagEpisode != marked.FlagEpisode || store.List()[0].FlagRevision != marked.FlagRevision {
		t.Fatal("episode marker was not persisted")
	}
	// Prompt position is not persisted, so even a first post-restart prompt
	// with the same visible reason cannot prove continuity. The old marked
	// flag is cleared; historical unmarked flags below remain durable.
	restarted := testPaneWithTail(t, p.ID, "worker", "$ ")
	saved := store.List()[0]
	restoreResumedState(restarted, PaneSnapshot{Flagged: saved.Flagged, FlagMemo: saved.FlagMemo, FlagOwner: saved.FlagOwner, FlagEpisode: saved.FlagEpisode, FlagRevision: saved.FlagRevision})
	restarted.setAttentionForPrompt(AttentionNeedsUser, "decision", attentionPromptKey{State: StateChoicePrompt, Pos: 202})
	w.mgr.panes[p.ID] = restarted
	w.cleanupWardenFlags()
	if got := restarted.Snapshot(); got.Flagged {
		t.Fatalf("restart adopted an unverified persisted episode: %+v", got)
	}
	if _, _, current := restarted.attentionEpisodeNow(); current == saved.FlagEpisode || current == "" {
		t.Fatal("restart reused the persisted episode")
	}
	restarted.setAttention(AttentionNone, "")
	// Historical flags have no marker and must survive both inactive attention
	// and a newly observed episode.
	restarted.SetFlagAs(true, "historical", wardenScopeIdentity)
	restarted.setAttention(AttentionNeedsUser, "new decision")
	restarted.setAttention(AttentionNone, "")
	w.cleanupWardenFlags()
	if got := restarted.Snapshot(); !got.Flagged || got.FlagMemo != "historical" {
		t.Fatalf("historical flag cleared: %+v", got)
	}
	// Manual replacement and a later same-owner replacement erase the exact
	// marker; cleanup must preserve both.
	for _, owner := range []string{"user", wardenScopeIdentity} {
		restarted.SetFlag(false, "")
		restarted.setAttention(AttentionNeedsUser, "another decision")
		scope = testWardenFlagScope(restarted)
		p = restarted
		create()
		restarted.SetFlagAs(true, "replacement", owner)
		restarted.setAttention(AttentionNone, "")
		w.cleanupWardenFlags()
		if got := restarted.Snapshot(); !got.Flagged || got.FlagOwner != owner || got.FlagMemo != "replacement" || got.FlagEpisode != "" {
			t.Fatalf("%s replacement cleared: %+v", owner, got)
		}
	}
	// A run from an older episode cannot write during a later needs-user one.
	restarted.SetFlag(false, "")
	restarted.setAttention(AttentionNeedsUser, "old")
	oldScope := testWardenFlagScope(restarted)
	restarted.setAttention(AttentionNone, "")
	restarted.setAttention(AttentionNeedsUser, "current")
	if msg, bad := mcpResultText(t, scopedCallForTest(t, a, oldScope, "set_pane_flag", map[string]any{"pane_id": p.ID, "flagged": true})); !bad || !strings.Contains(msg, "no longer active") {
		t.Fatalf("old run accepted in new episode: %q error=%v", msg, bad)
	}
}

func TestWardenRestartClearsMarkedFlagAfterFirstNoneEvaluation(t *testing.T) {
	t.Parallel()
	w, _ := testWarden(t, false)
	p := testPaneWithTail(t, "pane-restart-none", "worker", "$ ")
	restoreResumedState(p, PaneSnapshot{
		Flagged: true, FlagMemo: "old decision", FlagOwner: wardenScopeIdentity,
		FlagEpisode: "prior-episode", FlagRevision: "prior-revision",
	})
	w.mgr.panes[p.ID] = p
	if p.setAttentionForPrompt(AttentionNone, "", attentionPromptKey{State: StateIdle}) {
		t.Fatal("first none evaluation emitted a visible attention change")
	}
	w.cleanupWardenFlags()
	if got := p.Snapshot(); got.Flagged {
		t.Fatalf("first none evaluation left a restarted marked flag: %+v", got)
	}
}

func TestWardenBackToBackNeedsUserPromptsRotateEpisode(t *testing.T) {
	for _, reasonB := range []string{"", "new reason"} {
		t.Run("reason="+reasonB, func(t *testing.T) {
			w, st := testWarden(t, true)
			if _, err := st.Patch(wardenSettingsPatch{AutoFlags: boolPtr(true)}); err != nil {
				t.Fatal(err)
			}
			p := testPaneWithTail(t, "pane-back-to-back", "worker", "$ ")
			w.mgr.panes[p.ID] = p
			store, err := NewPaneStore(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			w.panes = store
			a := &apiServer{mgr: w.mgr, panes: store, wardenSettings: st}
			keyA := attentionPromptKey{State: StateChoicePrompt, Pos: 101}
			keyB := attentionPromptKey{State: StateChoicePrompt, Pos: 202}
			if !p.setAttentionForPrompt(AttentionNeedsUser, "", keyA) {
				t.Fatal("A was not observed")
			}
			base := time.Now()
			if evs := w.detect(base); len(evs) != 0 {
				t.Fatalf("early A triage: %+v", evs)
			}
			evs := w.detect(base.Add(11 * time.Minute))
			if len(evs) != 1 || !w.ackEvent(evs[0]) {
				t.Fatalf("A triage was not accepted: %+v", evs)
			}
			scopeA := testWardenFlagScope(p)
			if msg, bad := mcpResultText(t, scopedCallForTest(t, a, scopeA, "set_pane_flag", map[string]any{"pane_id": p.ID, "flagged": true, "memo": "answer A"})); bad {
				t.Fatal(msg)
			}
			_, _, episodeA := p.attentionEpisodeNow()
			if p.setAttentionForPrompt(AttentionNeedsUser, "", keyA) {
				t.Fatal("same A observation churned UI")
			}
			if _, _, same := p.attentionEpisodeNow(); same != episodeA {
				t.Fatal("same A observation churned episode")
			}
			if evs := w.detect(base.Add(12 * time.Minute)); len(evs) != 0 {
				t.Fatalf("A triaged twice: %+v", evs)
			}
			if reasonB == "" && p.setAttentionForPrompt(AttentionNeedsUser, reasonB, keyB) {
				t.Fatal("same-reason B emitted duplicate UI change")
			} else if reasonB != "" && !p.setAttentionForPrompt(AttentionNeedsUser, reasonB, keyB) {
				t.Fatal("different reason B did not update UI")
			}
			_, _, episodeB := p.attentionEpisodeNow()
			if episodeB == episodeA {
				t.Fatal("B retained A episode")
			}
			w.cleanupWardenFlags()
			if got := p.Snapshot(); got.Flagged {
				t.Fatalf("A flag survived prompt B: %+v", got)
			}
			if msg, bad := mcpResultText(t, scopedCallForTest(t, a, scopeA, "set_pane_flag", map[string]any{"pane_id": p.ID, "flagged": true, "memo": "stale A"})); !bad || !strings.Contains(msg, "no longer active") {
				t.Fatalf("A token during B = %q error=%v", msg, bad)
			}
			if evs := w.detect(base.Add(12 * time.Minute)); len(evs) != 0 {
				t.Fatalf("B triaged before delay: %+v", evs)
			}
			if evs := w.detect(base.Add(21 * time.Minute)); len(evs) != 0 {
				t.Fatalf("B triaged at 9m: %+v", evs)
			}
			evs = w.detect(base.Add(23 * time.Minute))
			if len(evs) != 1 || evs[0].Episode != episodeB || !w.ackEvent(evs[0]) {
				t.Fatalf("B did not receive its own triage: %+v", evs)
			}
			if evs := w.detect(base.Add(24 * time.Minute)); len(evs) != 0 {
				t.Fatalf("B triaged twice: %+v", evs)
			}
		})
	}
}

func TestWardenFailedCleanupCannotRollbackLaterManualFlag(t *testing.T) {
	t.Parallel()
	w, _ := testWarden(t, false)
	p := testPaneWithTail(t, "pane-rollback", "worker", "$ ")
	p.setAttention(AttentionNeedsUser, "decision")
	_, _, episode := p.attentionEpisodeNow()
	if err := p.changeFlag(nil, nil, true, "automatic", "warden", episode); err != nil {
		t.Fatal(err)
	}
	w.mgr.panes[p.ID] = p
	store := &failingFirstFlagStore{entered: make(chan struct{}), release: make(chan struct{})}
	w.panes = store
	p.setAttention(AttentionNone, "")
	cleanupDone := make(chan struct{})
	go func() { w.cleanupWardenFlags(); close(cleanupDone) }()
	select {
	case <-store.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("cleanup never reached persistence")
	}
	manualDone := make(chan error, 1)
	manualStarted := make(chan struct{})
	go func() {
		close(manualStarted)
		manualDone <- p.changeFlag(store, nil, true, "new manual reminder", "user", "")
	}()
	<-manualStarted
	select {
	case <-manualDone:
		t.Fatal("manual write committed while cleanup held the flag transaction")
	case <-time.After(10 * time.Millisecond):
	}
	close(store.release)
	select {
	case <-cleanupDone:
	case <-time.After(2 * time.Second):
		t.Fatal("cleanup blocked")
	}
	select {
	case err := <-manualDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("manual write blocked")
	}
	if got := p.Snapshot(); !got.Flagged || got.FlagMemo != "new manual reminder" || got.FlagOwner != "user" {
		t.Fatalf("failed cleanup rolled back manual replacement: %+v", got)
	}
}
