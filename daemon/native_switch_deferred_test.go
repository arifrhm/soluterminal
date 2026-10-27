package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// deferredTestPane builds a live terminal/native pane with a ring buffer for the
// deferred-switch unit tests. Like switchTestPane but with a real buffer so the
// terminal settle predicate (computeStateCached) has something to scan.
func deferredTestPane(id, kind string) *Pane {
	return &Pane{
		ID:        id,
		Cwd:       "/tmp",
		Cmd:       []string{"bash"},
		Title:     "agent",
		Status:    PaneRunning,
		CreatedAt: time.Now().UTC(),
		Cols:      80,
		Rows:      24,
		Kind:      kind,
		done:      make(chan struct{}),
		buffer:    NewRingBuffer(1 << 20),
		subs:      map[int64]*subEntry{},
	}
}

// awaitingInputContent is a real Claude idle prompt (a ❯ line + a context
// footer), the positive "agent handed the turn back" signal computeState reads
// as StateAwaitingInput. Lifted from state_test.go's awaiting-input fixture.
const awaitingInputContent = "❯                                         \n" +
	"\n" +
	"  ~/wsl-projects/unified-inbox [Opus 4.7 (1M context) | ctx: 3% | unified-…]\n"

// --- turnSettledForSwitch: native ---

// TestTurnSettledNativeFlagClear: a native pane settles ONLY when nativeInTurn
// clears, and NOT while an approval is parked (hasNativePending).
func TestTurnSettledNativeFlagClear(t *testing.T) {
	p := deferredTestPane("pane_native_settle", "native")

	// Mid-turn: in-turn flag set → not settled.
	p.nativeInTurn.Store(true)
	if p.turnSettledForSwitch() {
		t.Fatal("native pane mid-turn (nativeInTurn=true) must NOT be settled")
	}

	// Flag cleared (turn_end/result) → settled.
	p.nativeInTurn.Store(false)
	if !p.turnSettledForSwitch() {
		t.Fatal("native pane with nativeInTurn cleared must be settled")
	}

	// A parked approval keeps it unsettled even though nativeInTurn is clear:
	// the agent is waiting for the user, i.e. still mid-turn.
	p.nativePendingCount.Store(1)
	if p.turnSettledForSwitch() {
		t.Fatal("native pane with a parked approval (hasNativePending) must NOT be settled")
	}
	p.nativePendingCount.Store(0)
	if !p.turnSettledForSwitch() {
		t.Fatal("native pane settles again once the approval is drained")
	}
}

// --- turnSettledForSwitch: terminal ---

// TestTurnSettledTerminalOnlyOnAwaitingInput: a terminal pane settles ONLY on a
// positive prompt signal (StateAwaitingInput / StateChoicePrompt), never on bare
// Idle/Recent/Busy — firing on bare idle would abort an in-flight turn.
func TestTurnSettledTerminalOnlyOnAwaitingInput(t *testing.T) {
	// Bare Busy: fresh output, no prompt → NOT settled.
	busy := deferredTestPane("pane_busy", "")
	writeBuf(busy, "streaming tokens still flowing...\n")
	atomic.StoreInt64(&busy.LastActivityMs, time.Now().UnixMilli()) // age < 1500 → Busy
	if got := busy.computeStateCached(); got != StateBusy {
		t.Fatalf("precondition: state = %q, want %q", got, StateBusy)
	}
	if busy.turnSettledForSwitch() {
		t.Fatal("terminal pane in StateBusy must NOT be settled")
	}

	// Bare Idle: old activity, no prompt content → Idle, NOT settled. This is
	// the CRITICAL case: a mid-turn TUI pause that reads Idle must not fire.
	idle := deferredTestPane("pane_idle", "")
	writeBuf(idle, "some output with no prompt box at all\n")
	atomic.StoreInt64(&idle.LastActivityMs, time.Now().UnixMilli()-5000) // age > 3000 → Idle
	if got := idle.computeStateCached(); got != StateIdle {
		t.Fatalf("precondition: state = %q, want %q", got, StateIdle)
	}
	if idle.turnSettledForSwitch() {
		t.Fatal("terminal pane in bare StateIdle must NOT be settled (would abort an in-flight turn)")
	}

	// Bare Recent: activity 1.5-3s ago, no prompt → Recent, NOT settled.
	recent := deferredTestPane("pane_recent", "")
	writeBuf(recent, "trailing output without a prompt\n")
	atomic.StoreInt64(&recent.LastActivityMs, time.Now().UnixMilli()-2000) // 1500<age<3000 → Recent
	if got := recent.computeStateCached(); got != StateRecent {
		t.Fatalf("precondition: state = %q, want %q", got, StateRecent)
	}
	if recent.turnSettledForSwitch() {
		t.Fatal("terminal pane in StateRecent must NOT be settled")
	}

	// AwaitingInput: a real Claude prompt box → settled.
	await := deferredTestPane("pane_await", "")
	writeBuf(await, awaitingInputContent)
	atomic.StoreInt64(&await.LastActivityMs, time.Now().UnixMilli()-2000) // age>=1500 unlocks the prompt scan
	if got := await.computeStateCached(); got != StateAwaitingInput {
		t.Fatalf("precondition: state = %q, want %q", got, StateAwaitingInput)
	}
	if !await.turnSettledForSwitch() {
		t.Fatal("terminal pane in StateAwaitingInput must be settled (agent handed the turn back)")
	}
}

// TestTurnSettledTerminalPromptVisibleAfterAck: the steady-state prompt-box
// signal settles a terminal switch even AFTER StateAwaitingInput's one-shot has
// been consumed (acked) and computeState has fallen back to StateIdle. This is
// the stall the fix removes: the 200ms watcher could miss the single
// awaiting-input transition, then — with the old ack-gated predicate — wait out
// the 10-minute max-wait cap because awaiting-input never re-fires without new
// user input. A visible prompt box stays a positive settle signal.
func TestTurnSettledTerminalPromptVisibleAfterAck(t *testing.T) {
	p := deferredTestPane("pane_prompt_acked", "")
	writeBuf(p, awaitingInputContent)
	atomic.StoreInt64(&p.LastActivityMs, time.Now().UnixMilli()-5000) // age>3000

	// Simulate the awaiting-input one-shot already consumed: a non-zero ack with
	// no newer user input makes computeState fall through OFF StateAwaitingInput.
	atomic.StoreInt64(&p.LastAckedAwaitingInputPos, 1<<40)
	atomic.StoreInt64(&p.LastInputStrippedPos, 0)

	if got := p.computeStateCached(); got == StateAwaitingInput {
		t.Fatalf("precondition: state should have fallen off awaiting-input (acked one-shot), got %q", got)
	}
	if !p.turnSettledForSwitch() {
		t.Fatal("a terminal pane showing the prompt box must settle even after the awaiting-input one-shot was acked (else the deferred switch stalls to the max-wait cap)")
	}
}

// TestAgentMidTurnPromptOverridesRecencyTail: the prompt-aware "is the agent
// mid-turn?" signal does NOT misread a terminal pane's post-turn recency tail as
// working. A pane with fresh activity (StateBusy) but Claude's prompt box on
// screen is NOT mid-turn (the redraws are the idle prompt), so the client offers
// a one-click switch instead of the "switch when done" confirm, and a one-click
// switch stamps no bogus "interrupted" notice. The inverse — busy with no prompt
// box (real generation) — IS mid-turn.
func TestAgentMidTurnPromptOverridesRecencyTail(t *testing.T) {
	// Busy recency tail WITH a visible prompt box → NOT mid-turn.
	settled := deferredTestPane("pane_tail_settled", "")
	writeBuf(settled, awaitingInputContent)
	atomic.StoreInt64(&settled.LastActivityMs, time.Now().UnixMilli()-500) // age<1500 → Busy
	if got := settled.computeState(); got != StateBusy {
		t.Fatalf("precondition: state = %q, want %q (fresh redraw)", got, StateBusy)
	}
	if settled.agentMidTurnForState(StateBusy) {
		t.Fatal("a busy pane showing the prompt box must NOT be mid-turn (post-turn redraw tail)")
	}
	if settled.IsAgentWorking() {
		t.Fatal("IsAgentWorking must be false when Claude's prompt box is visible")
	}

	// Busy WITHOUT a prompt box (real generation) → mid-turn.
	working := deferredTestPane("pane_tail_working", "")
	writeBuf(working, "✶ Forging… (esc to interrupt)\nstreaming output keeps coming\n")
	atomic.StoreInt64(&working.LastActivityMs, time.Now().UnixMilli()-500)
	if got := working.computeState(); got != StateBusy {
		t.Fatalf("precondition: state = %q, want %q", got, StateBusy)
	}
	if !working.agentMidTurnForState(StateBusy) {
		t.Fatal("a busy pane with no prompt box (real generation) must be mid-turn")
	}
	if !working.IsAgentWorking() {
		t.Fatal("IsAgentWorking must be true while Claude is generating")
	}
}

// --- claimPendingSwitch: single-fire CAS ---

// TestClaimPendingSwitchSingleFire: exactly one caller wins the claim; a second
// claim of the same armed entry returns ok=false. Run concurrently to exercise
// the CAS.
func TestClaimPendingSwitchSingleFire(t *testing.T) {
	p := deferredTestPane("pane_claim", "")
	p.setPendingSwitch("native")

	const N = 50
	var wins int64
	var wg sync.WaitGroup
	wg.Add(N)
	for i := 0; i < N; i++ {
		go func() {
			defer wg.Done()
			if target, ok := p.claimPendingSwitch(); ok {
				if target != "native" {
					t.Errorf("claimed target = %q, want native", target)
				}
				atomic.AddInt64(&wins, 1)
			}
		}()
	}
	wg.Wait()

	if wins != 1 {
		t.Fatalf("claimPendingSwitch winners = %d, want exactly 1 (single-fire CAS)", wins)
	}

	// No entry → never ok.
	p.clearPendingSwitch()
	if _, ok := p.claimPendingSwitch(); ok {
		t.Fatal("claimPendingSwitch on a cleared pane must return ok=false")
	}
}

// TestSetPendingSwitchResetsFiredFlag: re-arming after a fire (new pending entry)
// is independently claimable — the fired flag lives on the entry, not the pane.
func TestSetPendingSwitchResetsFiredFlag(t *testing.T) {
	p := deferredTestPane("pane_rearm", "")
	p.setPendingSwitch("native")
	if _, ok := p.claimPendingSwitch(); !ok {
		t.Fatal("first claim should win")
	}
	if _, ok := p.claimPendingSwitch(); ok {
		t.Fatal("second claim on the SAME entry must lose")
	}
	// Re-arm: a fresh entry with its own (false) fired flag.
	p.setPendingSwitch("")
	if target, ok := p.claimPendingSwitch(); !ok || target != "" {
		t.Fatalf("claim after re-arm = (%q,%v), want (\"\",true)", target, ok)
	}
}

// TestPendingSwitchSnapshotCopy: the snapshot is a detached copy (not the live
// pointer), so a concurrent clear can't mutate a snapshot a DTO is reading.
func TestPendingSwitchSnapshotCopy(t *testing.T) {
	p := deferredTestPane("pane_snap", "")
	if p.pendingSwitchSnapshot() != nil {
		t.Fatal("no pending switch armed → snapshot must be nil")
	}
	p.setPendingSwitch("native")
	snap := p.pendingSwitchSnapshot()
	if snap == nil || snap.Target != "native" || snap.RequestedAt == 0 {
		t.Fatalf("snapshot = %#v, want {Target:native, RequestedAt:>0}", snap)
	}
	p.clearPendingSwitch()
	if snap.Target != "native" {
		t.Fatal("snapshot must be a detached copy unaffected by clearPendingSwitch")
	}
	if p.pendingSwitchSnapshot() != nil {
		t.Fatal("snapshot after clear must be nil")
	}
}

// --- API dispatch: arm / cancel / now-supersede ---

// newDeferredSwitchAPI builds an apiServer whose spawn/kill are recorded, plus a
// helper to issue switch-mode requests. Spawn returns a fresh running pane so a
// "now" switch can complete synchronously.
func newDeferredSwitchAPI(t *testing.T) (*apiServer, *Manager, *failingPaneStore) {
	t.Helper()
	mgr := NewManager(t.TempDir())
	store := &failingPaneStore{}
	api := &apiServer{
		mgr:   mgr,
		panes: store,
		// Mutate mgr.panes under the manager lock: the deferred-switch watcher
		// runs spawn/kill on its OWN goroutine while the test polls mgr.Get
		// (which RLocks), so the closures must lock to match real spawn/kill.
		spawnPane: func(o SpawnOpts) (*Pane, error) {
			np := switchTestPane(o.ID, o.Kind, o.Cmd, o.ClaudeSessionID)
			mgr.mu.Lock()
			mgr.panes[np.ID] = np
			mgr.mu.Unlock()
			return np, nil
		},
		killPane: func(id string) error {
			mgr.mu.Lock()
			delete(mgr.panes, id)
			mgr.mu.Unlock()
			return nil
		},
	}
	return api, mgr, store
}

func doSwitch(t *testing.T, api *apiServer, id, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/panes/"+id+"/switch-mode", strings.NewReader(body))
	req.SetPathValue("id", id)
	rec := httptest.NewRecorder()
	api.handleSwitchMode(rec, req)
	return rec
}

// TestSwitchModeDeferredArmsPending: a when:"deferred" request returns 200
// IMMEDIATELY (no kill/respawn yet) and arms a pending switch on the pane.
func TestSwitchModeDeferredArmsPending(t *testing.T) {
	t.Setenv("SOLU_NATIVE_RENDERER", "1")
	api, mgr, _ := newDeferredSwitchAPI(t)
	orig := deferredTestPane("pane_arm", "")
	mgr.panes[orig.ID] = orig

	rec := doSwitch(t, api, "pane_arm", `{"kind":"native","when":"deferred"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%q", rec.Code, rec.Body.String())
	}

	// Pane unchanged (still terminal, same struct) — the switch hasn't fired.
	if got, ok := mgr.Get("pane_arm"); !ok || got.Kind != "" || got != orig {
		t.Fatalf("pane changed after deferred arm: ok=%v kind=%q sameStruct=%v", ok, got.Kind, got == orig)
	}
	// Pending is armed and present in the DTO.
	if snap := orig.pendingSwitchSnapshot(); snap == nil || snap.Target != "native" {
		t.Fatalf("pending switch not armed: %#v", snap)
	}
	var dto paneDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &dto); err != nil {
		t.Fatalf("decode DTO: %v", err)
	}
	if dto.PendingSwitch == nil || dto.PendingSwitch.Target != "native" || dto.PendingSwitch.At == 0 {
		t.Fatalf("DTO pendingSwitch = %#v, want {target:native, at:>0}", dto.PendingSwitch)
	}
	// Stop the watcher cleanly.
	close(orig.done)
}

// TestSwitchModeDeferredFiresOnSettle: arm a deferred switch on a SETTLED native
// pane (nativeInTurn already clear) and verify the watcher fires performSwitchMode
// — the pane flips to terminal — without any interrupt.
func TestSwitchModeDeferredFiresOnSettle(t *testing.T) {
	t.Setenv("SOLU_NATIVE_RENDERER", "1")
	api, mgr, _ := newDeferredSwitchAPI(t)
	// Native pane that is already settled (not in turn).
	orig := deferredTestPane("pane_fire", "native")
	orig.ClaudeSessionID = "sess-fire"
	orig.nativeInTurn.Store(false)
	mgr.panes[orig.ID] = orig

	rec := doSwitch(t, api, "pane_fire", `{"kind":"","when":"deferred"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("arm status = %d, want 200; body=%q", rec.Code, rec.Body.String())
	}

	// The watcher should claim + fire within a few ticks (200ms each).
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if got, ok := mgr.Get("pane_fire"); ok && got.Kind == "" {
			// Flipped to terminal. No switchNotice should be set (lossless).
			if got.switchNoticeSnapshot() != nil {
				t.Fatal("deferred fire on a settled pane must NOT set a switchNotice (lossless)")
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("deferred switch never fired: pane did not flip to terminal within 3s")
}

// TestSwitchModeDeferredNeverNotifiesWhenWorking is the regression guard for the
// bogus "Switched … mid-turn — interrupted" marker on a LOSSLESS deferred switch.
// A deferred fire must NEVER stamp a switchNotice even if IsAgentWorking() reads
// true at the moment performSwitchMode runs (e.g. the terminal-source 3s recency
// window right at the settle boundary). The `deferred` arg makes the skip
// explicit instead of relying on gracefullyInterruptForSwitch happening to return
// false. Contrast: a "now" switch on a working pane SHOULD still mark interrupted.
func TestSwitchModeDeferredNeverNotifiesWhenWorking(t *testing.T) {
	t.Setenv("SOLU_NATIVE_RENDERER", "1")
	api, mgr, _ := newDeferredSwitchAPI(t)

	// A pane that IsAgentWorking() reports TRUE for (stands in for the terminal
	// recency window that triggered the bug).
	working := deferredTestPane("pane_defworking", "native")
	working.ClaudeSessionID = "sess-w"
	working.nativeInTurn.Store(true) // IsAgentWorking() == true
	mgr.panes[working.ID] = working

	newPane, err := api.performSwitchMode(working, "", true /* deferred */, 0, 0)
	if err != nil {
		t.Fatalf("deferred performSwitchMode: %v", err)
	}
	if newPane.switchNoticeSnapshot() != nil {
		t.Fatalf("deferred switch set a switchNotice while IsAgentWorking()==true; "+
			"a lossless deferred fire must NEVER mark interrupted. got=%#v",
			newPane.switchNoticeSnapshot())
	}
}

// TestSwitchModeCancelClearsPending: cancel:true clears an armed deferred switch
// and returns 200 with no pendingSwitch in the DTO. Kind is not required.
func TestSwitchModeCancelClearsPending(t *testing.T) {
	t.Setenv("SOLU_NATIVE_RENDERER", "1")
	api, mgr, _ := newDeferredSwitchAPI(t)
	orig := deferredTestPane("pane_cancel", "")
	mgr.panes[orig.ID] = orig

	// Arm, then cancel.
	if rec := doSwitch(t, api, "pane_cancel", `{"kind":"native","when":"deferred"}`); rec.Code != http.StatusOK {
		t.Fatalf("arm status = %d, want 200", rec.Code)
	}
	if orig.pendingSwitchSnapshot() == nil {
		t.Fatal("precondition: pending must be armed")
	}

	rec := doSwitch(t, api, "pane_cancel", `{"cancel":true}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("cancel status = %d, want 200; body=%q", rec.Code, rec.Body.String())
	}
	if orig.pendingSwitchSnapshot() != nil {
		t.Fatal("cancel must clear the pending switch")
	}
	var dto paneDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &dto); err != nil {
		t.Fatalf("decode DTO: %v", err)
	}
	if dto.PendingSwitch != nil {
		t.Fatalf("DTO pendingSwitch after cancel = %#v, want nil", dto.PendingSwitch)
	}
	close(orig.done)
}

// TestSwitchModeNowSupersedesPending: a when:"now" (or absent) switch clears any
// armed deferred switch BEFORE flipping — the deferred arm can't fire onto the
// post-switch pane.
func TestSwitchModeNowSupersedesPending(t *testing.T) {
	t.Setenv("SOLU_NATIVE_RENDERER", "1")
	api, mgr, _ := newDeferredSwitchAPI(t)
	orig := deferredTestPane("pane_supersede", "")
	mgr.panes[orig.ID] = orig

	// Arm a deferred switch to native.
	if rec := doSwitch(t, api, "pane_supersede", `{"kind":"native","when":"deferred"}`); rec.Code != http.StatusOK {
		t.Fatalf("arm status = %d, want 200", rec.Code)
	}
	if orig.pendingSwitchSnapshot() == nil {
		t.Fatal("precondition: pending must be armed")
	}

	// Now a synchronous "now" switch (no `when`) supersedes it. The old struct's
	// pending must be cleared by handleSwitchMode before the flip.
	rec := doSwitch(t, api, "pane_supersede", `{"kind":"native"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("now status = %d, want 200; body=%q", rec.Code, rec.Body.String())
	}
	if orig.pendingSwitchSnapshot() != nil {
		t.Fatal("a \"now\" switch must supersede (clear) the armed deferred switch")
	}
	// The pane is now native (the synchronous switch completed).
	if got, ok := mgr.Get("pane_supersede"); !ok || got.Kind != "native" {
		t.Fatalf("pane after now-switch: ok=%v kind=%q, want native", ok, got.Kind)
	}
}

// TestSwitchModeDeferredToCurrentKindIsNoop: a deferred switch whose target is
// already the current kind clears any prior arm and does NOT arm a new one.
func TestSwitchModeDeferredToCurrentKindIsNoop(t *testing.T) {
	t.Setenv("SOLU_NATIVE_RENDERER", "1")
	api, mgr, _ := newDeferredSwitchAPI(t)
	orig := deferredTestPane("pane_noop_def", "") // already terminal
	mgr.panes[orig.ID] = orig

	rec := doSwitch(t, api, "pane_noop_def", `{"kind":"","when":"deferred"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%q", rec.Code, rec.Body.String())
	}
	if orig.pendingSwitchSnapshot() != nil {
		t.Fatal("deferred switch to the current kind must NOT arm a pending switch")
	}
}

// TestPendingSwitchDTONormalizesTerminal: the DTO normalizes the empty terminal
// target "" → "terminal" (mirrors switchNotice.fromKind).
func TestPendingSwitchDTONormalizesTerminal(t *testing.T) {
	p := deferredTestPane("pane_dto_norm", "native")
	p.setPendingSwitch("") // target = terminal
	dto := makePaneDTO(p, nil, nil)
	if dto.PendingSwitch == nil {
		t.Fatal("DTO pendingSwitch must be present")
	}
	if dto.PendingSwitch.Target != "terminal" {
		t.Fatalf("DTO pendingSwitch.target = %q, want \"terminal\" (normalized from \"\")", dto.PendingSwitch.Target)
	}
}

// --- pendingSwitch persistence (survives daemon restart) ---

// TestPendingSwitchPersistRoundTrip: an armed deferred switch rides
// panes.json (target + ORIGINAL arm time) and a clear removes it — including
// through the clone paths (List returns deep copies).
func TestPendingSwitchPersistRoundTrip(t *testing.T) {
	store, err := NewPaneStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	p := deferredTestPane("pane_persist_pend", "native")
	armedAt := time.Now().Add(-42 * time.Second).UnixMilli()
	p.setPendingSwitchAt("", armedAt)

	if err := store.Put(p); err != nil {
		t.Fatal(err)
	}
	list := store.List()
	if len(list) != 1 || list[0].PendingSwitch == nil {
		t.Fatalf("persisted = %+v, want one entry with PendingSwitch", list)
	}
	if got := list[0].PendingSwitch; got.Target != "" || got.RequestedAt != armedAt {
		t.Fatalf("PendingSwitch = %+v, want target terminal at the ORIGINAL arm time %d", got, armedAt)
	}

	// Restore path: AttachExisting's restore call re-creates the in-memory arm.
	restored := deferredTestPane("pane_persist_pend2", "native")
	restored.setPendingSwitchAt(list[0].PendingSwitch.Target, list[0].PendingSwitch.RequestedAt)
	pend := restored.pendingSwitchSnapshot()
	if pend == nil || pend.RequestedAt != armedAt {
		t.Fatalf("restored pending = %+v, want RequestedAt preserved (cap spans the restart)", pend)
	}

	// Clearing persists the removal.
	p.clearPendingSwitch()
	if err := store.Put(p); err != nil {
		t.Fatal(err)
	}
	if list := store.List(); list[0].PendingSwitch != nil {
		t.Fatalf("cleared switch still on disk: %+v", list[0].PendingSwitch)
	}
}

// TestDeferredSwitchArmAndCancelPersist: the HTTP arm and cancel paths both
// rewrite the pane's persisted entry (so an update-restart can't silently drop
// the user's "switch when done", and a cancel can't resurrect after restart).
func TestDeferredSwitchArmAndCancelPersist(t *testing.T) {
	t.Setenv("SOLU_NATIVE_RENDERER", "1")
	api, mgr, store := newDeferredSwitchAPI(t)
	p := deferredTestPane("pane_arm_persist", "native")
	p.nativeInTurn.Store(true) // keep the watcher from firing mid-test
	mgr.panes[p.ID] = p

	rec := doSwitch(t, api, p.ID, `{"kind":"","when":"deferred"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("arm status = %d body=%q", rec.Code, rec.Body.String())
	}
	if ids := store.putIDsSnapshot(); len(ids) == 0 || ids[len(ids)-1] != p.ID {
		t.Fatalf("arm did not persist the pane: putIDs=%v", ids)
	}

	arms := len(store.putIDsSnapshot())
	rec = doSwitch(t, api, p.ID, `{"cancel":true}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("cancel status = %d body=%q", rec.Code, rec.Body.String())
	}
	if ids := store.putIDsSnapshot(); len(ids) <= arms {
		t.Fatalf("cancel did not persist the clear: putIDs=%v", ids)
	}
	if p.pendingSwitchSnapshot() != nil {
		t.Fatal("cancel left the switch armed")
	}
}

// TestRearmedWatcherFiresAfterRestart: the boot path (restore + ensureSwitchWatcher)
// yields a watcher that fires the persisted switch once the pane settles — the
// user's pre-restart "switch when Claude finishes" survives the update.
func TestRearmedWatcherFiresAfterRestart(t *testing.T) {
	t.Setenv("SOLU_NATIVE_RENDERER", "1")
	api, mgr, _ := newDeferredSwitchAPI(t)

	// A native pane that is SETTLED (turn done) with a restored pending switch —
	// as AttachExisting leaves it after a restart.
	p := deferredTestPane("pane_rearm", "native")
	p.ClaudeSessionID = "sess-rearm"
	p.setPendingSwitchAt("", time.Now().Add(-30*time.Second).UnixMilli())
	mgr.panes[p.ID] = p

	api.ensureSwitchWatcher(p)

	if !waitFor(3*time.Second, 20*time.Millisecond, func() bool {
		np, ok := mgr.Get(p.ID)
		return ok && np != p && np.Kind == ""
	}) {
		t.Fatal("re-armed watcher never fired the persisted deferred switch")
	}
}

// TestRearmedWatcherHonorsCapAcrossRestart: a restored arm whose ORIGINAL
// RequestedAt already exceeds the max-wait cap is abandoned promptly (and the
// clear persisted) instead of waiting another full cap from boot.
func TestRearmedWatcherHonorsCapAcrossRestart(t *testing.T) {
	t.Setenv("SOLU_NATIVE_RENDERER", "1")
	api, mgr, store := newDeferredSwitchAPI(t)

	p := deferredTestPane("pane_rearm_cap", "native")
	p.nativeInTurn.Store(true) // never settles → only the cap can end it
	p.setPendingSwitchAt("", time.Now().Add(-deferredSwitchMaxWait-time.Minute).UnixMilli())
	mgr.panes[p.ID] = p

	api.ensureSwitchWatcher(p)

	if !waitFor(3*time.Second, 20*time.Millisecond, func() bool {
		return p.pendingSwitchSnapshot() == nil
	}) {
		t.Fatal("expired restored arm was not abandoned by the cap")
	}
	if !waitFor(time.Second, 20*time.Millisecond, func() bool {
		for _, id := range store.putIDsSnapshot() {
			if id == p.ID {
				return true
			}
		}
		return false
	}) {
		t.Fatal("cap give-up did not persist the cleared switch")
	}
}
