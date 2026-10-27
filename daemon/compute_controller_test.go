package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"reflect"
	"testing"
	"time"

	"soluterminal-shared/wire"
)

type computeManualClock struct {
	value time.Time
}

func (c *computeManualClock) Now() time.Time { return c.value }
func (c *computeManualClock) Advance(d time.Duration) {
	c.value = c.value.Add(d)
}

type computeSequenceIDs struct {
	next int
}

func (s *computeSequenceIDs) New(prefix string) (string, error) {
	s.next++
	return fmt.Sprintf("%s-%d", prefix, s.next), nil
}

type computeControllerHarness struct {
	dataDir    string
	store      *computeStore
	controller *computeController
	clock      *computeManualClock
	ids        *computeSequenceIDs
	worker     *fakeComputeWorker
	job        wire.ComputeJobRecord
}

func newComputeControllerHarness(t *testing.T, id string) *computeControllerHarness {
	t.Helper()
	dataDir := t.TempDir()
	store, err := newComputeStore(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	job, candidate := eligibleComputeFixture()
	clock := &computeManualClock{value: time.Date(2026, 9, 2, 1, 2, 3, 0, time.UTC)}
	job.JobID = id
	job.IdempotencyKey = "submit-" + id
	job.Authz.SubmittedAt = computeControllerTime(clock.Now())
	job.State = "queued"
	job.Attempt = 0
	job.Spec.MaxAttempts = 0
	created, err := store.CreateJob(job)
	if err != nil {
		t.Fatal(err)
	}
	ids := &computeSequenceIDs{}
	return &computeControllerHarness{
		dataDir: dataDir, store: store,
		controller: newComputeControllerWithDependencies(store, clock.Now, ids.New),
		clock:      clock, ids: ids, worker: newFakeComputeWorker(candidate), job: created,
	}
}

func (h *computeControllerHarness) enableWorker() {
	h.worker.approveByController(h.clock.Now())
	h.worker.enableLocalAuthenticatedSession()
}

func (h *computeControllerHarness) startRunning(t *testing.T) (wire.AssignmentOffer, wire.AssignmentGrant) {
	t.Helper()
	h.enableWorker()
	offer, decision, err := h.controller.Offer(h.job.JobID, []computeCandidateState{h.worker.schedulingCandidate()})
	if err != nil || decision.SelectedNodeID != h.worker.candidate.Node.NodeID {
		t.Fatalf("offer decision=%+v err=%v", decision, err)
	}
	claim, err := h.worker.receiveOffer(offer, h.clock.Now())
	if err != nil {
		t.Fatal(err)
	}
	if grant, err := h.controller.AcceptClaim(h.worker.candidate.Node.NodeID, claim); err != nil || grant != nil {
		t.Fatalf("claim returned grant=%+v err=%v", grant, err)
	}
	grant, err := h.controller.Grant(h.job.JobID)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.worker.receiveGrant(grant, h.clock.Now()); err != nil {
		t.Fatal(err)
	}
	return offer, grant
}

func TestComputeControllerFakeWorkerEndToEnd(t *testing.T) {
	h := newComputeControllerHarness(t, "job-controller-e2e")

	// D12 readiness needs controller approval and a separate local,
	// authenticated session fact. Neither partial state may be selected.
	if offer, decision, err := h.controller.Offer(h.job.JobID, []computeCandidateState{h.worker.schedulingCandidate()}); err != nil ||
		offer.AssignmentID != "" || decision.SelectedNodeID != "" {
		t.Fatalf("pending worker scheduled: offer=%+v decision=%+v err=%v", offer, decision, err)
	}
	h.worker.approveByController(h.clock.Now())
	if offer, decision, err := h.controller.Offer(h.job.JobID, []computeCandidateState{h.worker.schedulingCandidate()}); err != nil ||
		offer.AssignmentID != "" || decision.SelectedNodeID != "" {
		t.Fatalf("locally disabled worker scheduled: offer=%+v decision=%+v err=%v", offer, decision, err)
	}
	h.worker.enableLocalAuthenticatedSession()
	offer, decision, err := h.controller.Offer(h.job.JobID, []computeCandidateState{h.worker.schedulingCandidate()})
	if err != nil || decision.SelectedNodeID != h.worker.candidate.Node.NodeID {
		t.Fatalf("ready worker decision=%+v err=%v", decision, err)
	}
	persistedOffer := computeMustLoad(t, h.store, h.job.JobID)
	if persistedOffer.Job.State != "offered" || persistedOffer.Offer == nil || !reflect.DeepEqual(*persistedOffer.Offer, offer) {
		t.Fatalf("offer was not persisted before delivery: %+v", persistedOffer)
	}
	claim, err := h.worker.receiveOffer(offer, h.clock.Now())
	if err != nil {
		t.Fatal(err)
	}
	if key := fakeComputeFenceKey(offer.JobID, offer.ExecutionID, offer.Epoch); h.worker.starts[key] != 0 {
		t.Fatal("fake handler started before grant")
	}
	if got, err := h.controller.AcceptClaim(h.worker.candidate.Node.NodeID, claim); err != nil || got != nil {
		t.Fatalf("claim grant=%+v err=%v", got, err)
	}
	persistedClaim := computeMustLoad(t, h.store, h.job.JobID)
	if persistedClaim.Job.State != "claimed" || persistedClaim.Claim == nil || !reflect.DeepEqual(*persistedClaim.Claim, claim) {
		t.Fatalf("claim projection = %+v", persistedClaim)
	}
	grant, err := h.controller.Grant(h.job.JobID)
	if err != nil {
		t.Fatal(err)
	}
	persistedGrant := computeMustLoad(t, h.store, h.job.JobID)
	if persistedGrant.Job.State != "running" || persistedGrant.Grant == nil || *persistedGrant.Grant != grant || persistedGrant.Job.Attempt != 1 {
		t.Fatalf("grant was not persisted before delivery: %+v", persistedGrant)
	}
	if err := h.worker.receiveGrant(grant, h.clock.Now()); err != nil {
		t.Fatal(err)
	}
	if err := h.worker.receiveGrant(grant, h.clock.Now()); err != nil {
		t.Fatalf("duplicate grant delivery = %v", err)
	}
	key := fakeComputeFenceKey(grant.JobID, grant.ExecutionID, grant.Epoch)
	if h.worker.starts[key] != 1 {
		t.Fatalf("synthetic starts = %d, want 1", h.worker.starts[key])
	}

	h.clock.Advance(computeRenewalCadence)
	renewal, err := h.controller.Renew(h.job.JobID)
	if err != nil {
		t.Fatal(err)
	}
	if state := computeMustLoad(t, h.store, h.job.JobID); state.Lease == nil || *state.Lease != renewal {
		t.Fatalf("renewal not durable before delivery: %+v", state)
	}
	if err := h.worker.receiveRenewal(renewal); err != nil {
		t.Fatal(err)
	}
	chunk, err := h.worker.syntheticLog(grant, "stdout", []byte("synthetic shared tests passed\n"))
	if err != nil {
		t.Fatal(err)
	}
	ack, err := h.controller.AcceptLog(h.worker.candidate.Node.NodeID, chunk)
	if err != nil || ack.ContiguousSequence != 1 {
		t.Fatalf("durable log ack=%+v err=%v", ack, err)
	}
	h.clock.Advance(time.Second)
	terminal, err := h.worker.syntheticTerminal(grant, h.job, h.clock.Now(), false)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.controller.AcceptTerminal(h.worker.candidate.Node.NodeID, terminal); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(h.store.snapPath(h.job.JobID)); err != nil {
		t.Fatal(err)
	}
	reopened, err := newComputeStore(h.dataDir)
	if err != nil {
		t.Fatal(err)
	}
	final := computeMustLoad(t, reopened, h.job.JobID)
	if final.Job.State != "succeeded" || final.Job.Attempt != 1 || final.Terminal == nil ||
		final.LastAppliedEventSequence != 7 || final.Fence != (computeExecutionFence{JobID: h.job.JobID, ExecutionID: grant.ExecutionID, AuthenticatedNodeID: h.worker.candidate.Node.NodeID, Epoch: 1}) {
		t.Fatalf("journal-only final projection = %+v", final)
	}
}

func TestComputeControllerOfferExpiryHigherEpochAndLateClaim(t *testing.T) {
	h := newComputeControllerHarness(t, "job-controller-offer-expiry")
	h.enableWorker()
	first, _, err := h.controller.Offer(h.job.JobID, []computeCandidateState{h.worker.schedulingCandidate()})
	if err != nil {
		t.Fatal(err)
	}
	lateClaim, err := h.worker.receiveOffer(first, h.clock.Now())
	if err != nil {
		t.Fatal(err)
	}
	h.clock.Advance(computeOfferTTL)
	second, _, err := h.controller.Offer(h.job.JobID, []computeCandidateState{h.worker.schedulingCandidate()})
	if err != nil {
		t.Fatal(err)
	}
	if second.Epoch != first.Epoch+1 || second.AssignmentID == first.AssignmentID {
		t.Fatalf("replacement offer = %+v after %+v", second, first)
	}
	if err := func() error {
		_, err := h.controller.AcceptClaim(h.worker.candidate.Node.NodeID, lateClaim)
		return err
	}(); !errors.Is(err, ErrComputeStaleExecutionFence) {
		t.Fatalf("late claim error = %v", err)
	}
	state := computeMustLoad(t, h.store, h.job.JobID)
	if state.Job.State != "offered" || state.Job.Attempt != 0 || state.Offer == nil || state.Offer.AssignmentID != second.AssignmentID ||
		state.Fence.Epoch != 2 || state.LatestAssignmentExpiry == nil || state.LastAppliedEventSequence != 5 {
		t.Fatalf("replacement projection after stale claim = %+v", state)
	}
}

func TestComputeControllerDuplicateGrantDeliveryStartsOnce(t *testing.T) {
	h := newComputeControllerHarness(t, "job-controller-duplicate-grant")
	_, grant := h.startRunning(t)
	journalBefore, _ := os.ReadFile(h.store.eventsPath(h.job.JobID))
	replayed, err := h.controller.Grant(h.job.JobID)
	if err != nil || replayed != grant {
		t.Fatalf("grant replay=%+v err=%v", replayed, err)
	}
	journalAfter, _ := os.ReadFile(h.store.eventsPath(h.job.JobID))
	if !bytes.Equal(journalBefore, journalAfter) {
		t.Fatal("repeated grant retrieval appended")
	}
	if err := h.worker.receiveGrant(replayed, h.clock.Now()); err != nil {
		t.Fatal(err)
	}
	key := fakeComputeFenceKey(grant.JobID, grant.ExecutionID, grant.Epoch)
	if h.worker.starts[key] != 1 {
		t.Fatalf("duplicate grant starts = %d", h.worker.starts[key])
	}
}

func TestComputeControllerWorkerSelfFenceLapseRetryAndStaleResult(t *testing.T) {
	h := newComputeControllerHarness(t, "job-controller-lapse")
	firstOffer, firstGrant := h.startRunning(t)
	h.clock.Advance(computeExecutionLease - computeWorkerFenceLead)
	if !h.worker.applySelfFence(h.clock.Now()) {
		t.Fatal("fake worker did not self-fence five seconds before deadline")
	}
	leaseLost, err := h.worker.syntheticTerminal(firstGrant, h.job, h.clock.Now(), true)
	if err != nil {
		t.Fatal(err)
	}
	if expired, err := h.controller.ExpireDue(h.job.JobID); err != nil || expired != "" {
		t.Fatalf("lease lapsed before deadline: expired=%q err=%v", expired, err)
	}
	h.clock.Advance(computeWorkerFenceLead)
	if expired, err := h.controller.ExpireDue(h.job.JobID); err != nil || expired != computeEventExecutionLapsed {
		t.Fatalf("deadline lapse=%q err=%v", expired, err)
	}
	retry := computeMustLoad(t, h.store, h.job.JobID)
	if retry.Job.State != "retry_wait" || retry.Job.Attempt != 1 || retry.LatestExecutionLapse == nil || !retry.LatestExecutionLapse.Retry {
		t.Fatalf("retry projection = %+v", retry)
	}
	secondOffer, _, err := h.controller.Offer(h.job.JobID, []computeCandidateState{h.worker.schedulingCandidate()})
	if err != nil || secondOffer.Epoch != firstOffer.Epoch+1 {
		t.Fatalf("retry offer=%+v err=%v", secondOffer, err)
	}
	if err := h.controller.AcceptTerminal(h.worker.candidate.Node.NodeID, leaseLost); !errors.Is(err, ErrComputeStaleExecutionFence) {
		t.Fatalf("old-epoch terminal error = %v", err)
	}
	current := computeMustLoad(t, h.store, h.job.JobID)
	if current.Job.State != "offered" || current.Job.Attempt != 1 || current.Offer == nil || current.Offer.AssignmentID != secondOffer.AssignmentID || current.Fence.Epoch != 2 {
		t.Fatalf("stale result changed retry offer = %+v", current)
	}
}

func TestComputeControllerReplayAfterClaimAndWhileRunning(t *testing.T) {
	h := newComputeControllerHarness(t, "job-controller-replay")
	h.enableWorker()
	offer, _, err := h.controller.Offer(h.job.JobID, []computeCandidateState{h.worker.schedulingCandidate()})
	if err != nil {
		t.Fatal(err)
	}
	claim, err := h.worker.receiveOffer(offer, h.clock.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.controller.AcceptClaim(h.worker.candidate.Node.NodeID, claim); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(h.store.snapPath(h.job.JobID)); err != nil {
		t.Fatal(err)
	}
	claimedStore, err := newComputeStore(h.dataDir)
	if err != nil {
		t.Fatal(err)
	}
	claimed := computeMustLoad(t, claimedStore, h.job.JobID)
	if claimed.Job.State != "claimed" || claimed.Claim == nil || claimed.LastAppliedEventSequence != 3 || claimed.Job.Attempt != 0 {
		t.Fatalf("claim replay = %+v", claimed)
	}
	restarted := newComputeControllerWithDependencies(claimedStore, h.clock.Now, h.ids.New)
	journalAtClaim, _ := os.ReadFile(claimedStore.eventsPath(h.job.JobID))
	if returned, err := restarted.AcceptClaim(h.worker.candidate.Node.NodeID, claim); err != nil || returned != nil {
		t.Fatalf("repeated claim before grant returned=%+v err=%v", returned, err)
	}
	journalAfterClaimRetry, _ := os.ReadFile(claimedStore.eventsPath(h.job.JobID))
	if !bytes.Equal(journalAtClaim, journalAfterClaimRetry) {
		t.Fatal("repeated claimed-state claim appended")
	}
	grant, err := restarted.Grant(h.job.JobID)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.worker.receiveGrant(grant, h.clock.Now()); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(claimedStore.snapPath(h.job.JobID)); err != nil {
		t.Fatal(err)
	}
	runningStore, err := newComputeStore(h.dataDir)
	if err != nil {
		t.Fatal(err)
	}
	running := computeMustLoad(t, runningStore, h.job.JobID)
	if running.Job.State != "running" || running.Job.Attempt != 1 || running.Grant == nil || *running.Grant != grant ||
		running.LastAppliedEventSequence != 4 || running.Fence.Epoch != grant.Epoch {
		t.Fatalf("running replay = %+v", running)
	}
	journalBefore, _ := os.ReadFile(runningStore.eventsPath(h.job.JobID))
	afterRestart := newComputeControllerWithDependencies(runningStore, h.clock.Now, h.ids.New)
	replayed, err := afterRestart.Grant(h.job.JobID)
	if err != nil || replayed != grant {
		t.Fatalf("running grant replay=%+v err=%v", replayed, err)
	}
	if returned, err := afterRestart.AcceptClaim(h.worker.candidate.Node.NodeID, claim); err != nil || returned == nil || *returned != grant {
		t.Fatalf("claim retry returned=%+v err=%v", returned, err)
	}
	journalAfter, _ := os.ReadFile(runningStore.eventsPath(h.job.JobID))
	if !bytes.Equal(journalBefore, journalAfter) {
		t.Fatal("replay grant or repeated claim appended")
	}
	if err := h.worker.receiveGrant(replayed, h.clock.Now()); err != nil {
		t.Fatal(err)
	}
	key := fakeComputeFenceKey(grant.JobID, grant.ExecutionID, grant.Epoch)
	if h.worker.starts[key] != 1 {
		t.Fatalf("replayed grant starts = %d", h.worker.starts[key])
	}
}
