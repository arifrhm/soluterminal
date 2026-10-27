package main

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"soluterminal-shared/wire"
)

// fakeComputeWorker is only a deterministic control-plane fixture. It models
// D12 readiness and receipts without enrollment cryptography, a transport,
// filesystem access, subprocesses, or real verification execution.
type fakeComputeWorker struct {
	candidate computeCandidateState

	offers       map[string]wire.AssignmentOffer
	claims       map[string]wire.AssignmentClaim
	grants       map[string]wire.AssignmentGrant
	renewals     map[string]wire.LeaseRenewal
	starts       map[string]int
	startedAt    map[string]time.Time
	selfFenced   map[string]bool
	bufferedLogs []wire.LogChunk
	bufferedEnds []wire.TerminalResult
}

func newFakeComputeWorker(eligible computeCandidateState) *fakeComputeWorker {
	eligible.Node.State = "pending"
	eligible.Node.ApprovedAt = nil
	eligible.SessionAuthenticated = false
	eligible.LocalEnabled = false
	return &fakeComputeWorker{
		candidate:  eligible,
		offers:     make(map[string]wire.AssignmentOffer),
		claims:     make(map[string]wire.AssignmentClaim),
		grants:     make(map[string]wire.AssignmentGrant),
		renewals:   make(map[string]wire.LeaseRenewal),
		starts:     make(map[string]int),
		startedAt:  make(map[string]time.Time),
		selfFenced: make(map[string]bool),
	}
}

func (w *fakeComputeWorker) approveByController(at time.Time) {
	approved := computeControllerTime(at)
	w.candidate.Node.State = "enabled"
	w.candidate.Node.ApprovedAt = &approved
}

func (w *fakeComputeWorker) enableLocalAuthenticatedSession() {
	w.candidate.LocalEnabled = true
	w.candidate.SessionAuthenticated = true
}

func (w *fakeComputeWorker) schedulingCandidate() computeCandidateState {
	return w.candidate
}

func (w *fakeComputeWorker) receiveOffer(offer wire.AssignmentOffer, now time.Time) (wire.AssignmentClaim, error) {
	if prior, ok := w.offers[offer.AssignmentID]; ok {
		if !reflect.DeepEqual(prior, offer) {
			return wire.AssignmentClaim{}, ErrComputeConflictingDuplicate
		}
		return w.claims[offer.AssignmentID], nil
	}
	c := w.candidate
	if !c.SessionAuthenticated || !c.LocalEnabled || c.Node.State != "enabled" || c.Node.ApprovedAt == nil {
		return wire.AssignmentClaim{}, fmt.Errorf("fake worker is not registration-ready")
	}
	if offer.AssignmentID == "" || offer.JobID == "" || offer.ExecutionID == "" || offer.Epoch == 0 {
		return wire.AssignmentClaim{}, fmt.Errorf("fake worker rejected incomplete offer identity")
	}
	if offer.Authz.TenantID != c.Node.TenantID || offer.Authz.TenantID != c.Policy.TenantID ||
		offer.Authz.Principal == "" || !computeContainsString(c.Policy.AllowedPrincipals, offer.Authz.Principal) {
		return wire.AssignmentClaim{}, fmt.Errorf("fake worker rejected tenant/principal authority")
	}
	hash, err := computeSpecHash(offer.Spec)
	if err != nil || hash != offer.SpecHash || offer.Authz.SpecHash != offer.SpecHash ||
		offer.Authz.AssignmentID != offer.AssignmentID || offer.Authz.ExpiresAt != offer.ExpiresAt {
		return wire.AssignmentClaim{}, fmt.Errorf("fake worker rejected assignment/spec binding")
	}
	if !computeSupportedJob(offer.Spec) || offer.Spec.Handler != (wire.HandlerVersion{HandlerID: computeHandlerSharedTests, Version: computeHandlerVersionV1}) {
		return wire.AssignmentClaim{}, fmt.Errorf("fake worker rejected non-D11 handler tuple")
	}
	if !computeContainsString(c.Policy.AllowedJobKinds, offer.Spec.JobKind) ||
		!computeContainsHandler(c.Policy.AllowedHandlers, offer.Spec.Handler) ||
		!computeContainsHandler(c.Node.Admin.ApprovedHandlers, offer.Spec.Handler) ||
		!computeContainsString(c.Policy.WorkspaceIDs, offer.Spec.WorkspaceID) ||
		!computeContainsString(c.Node.Admin.ApprovedWorkspaceIDs, offer.Spec.WorkspaceID) {
		return wire.AssignmentClaim{}, fmt.Errorf("fake worker rejected handler/workspace policy")
	}
	jobClass, jobClassKnown := computeDataClassRank(offer.Spec.DataClass)
	maxClass, maxClassKnown := computeDataClassRank(c.Policy.MaximumDataClass)
	locality := computeCandidateLocality(offer.Spec, c.Node)
	if !jobClassKnown || !maxClassKnown || jobClass > maxClass ||
		((offer.Spec.Requirements.RequireDataOwner || jobClass >= computeDataClassConfidential) && locality != computeLocalityOwner) ||
		(locality == computeLocalityTransfer && !c.Policy.AllowRemoteTransfer) {
		return wire.AssignmentClaim{}, fmt.Errorf("fake worker rejected data policy/locality")
	}
	if (len(offer.Spec.Requirements.OS) > 0 && !computeContainsString(offer.Spec.Requirements.OS, c.Node.Observed.OS)) ||
		(len(offer.Spec.Requirements.Architectures) > 0 && !computeContainsString(offer.Spec.Requirements.Architectures, c.Node.Observed.Architecture)) ||
		!computeContainsHandler(c.Node.Observed.DetectedHandlers, offer.Spec.Handler) {
		return wire.AssignmentClaim{}, fmt.Errorf("fake worker rejected observed capabilities")
	}
	issued, issuedErr := time.Parse(time.RFC3339, offer.Authz.IssuedAt)
	deadline, deadlineErr := time.Parse(time.RFC3339, offer.ExpiresAt)
	if issuedErr != nil || deadlineErr != nil || !issued.Before(deadline) || !now.UTC().Before(deadline) {
		return wire.AssignmentClaim{}, ErrComputeExpiredOffer
	}
	required := offer.Spec.RequiredFeatures | offer.Spec.Requirements.RequiredFeatures
	if wire.CheckRequiredFeatures(c.Protocol.Features, required) != nil {
		return wire.AssignmentClaim{}, fmt.Errorf("fake worker rejected required features")
	}
	r := offer.Spec.Requirements
	if r.MinimumCPU < 0 || r.MinimumRAMBytes < 0 || r.MinimumDiskBytes < 0 || r.MinimumGPU < 0 ||
		r.MinimumCPU > c.Policy.MaxCPU || r.MinimumCPU > c.Node.Observed.LogicalCPU ||
		r.MinimumRAMBytes > c.Policy.MaxRAMBytes || r.MinimumRAMBytes > c.Telemetry.AvailableRAMBytes ||
		r.MinimumDiskBytes > c.Policy.MaxDiskBytes || r.MinimumDiskBytes > c.Telemetry.AvailableDiskBytes ||
		r.MinimumGPU > c.Policy.MaxGPU || r.MinimumGPU > len(c.Node.Observed.GPUs) {
		return wire.AssignmentClaim{}, fmt.Errorf("fake worker rejected resource policy")
	}
	if c.Protocol.Major != wire.ComputeProtocolMajorV1 || c.Telemetry.Health != "healthy" ||
		!c.HeartbeatFresh || !c.SessionLeaseValid || !c.TelemetryFresh ||
		c.Policy.MaxConcurrency <= 0 || c.Telemetry.ActiveJobs >= c.Policy.MaxConcurrency ||
		c.Telemetry.InteractiveIdleSec < c.Policy.MinimumIdleSec ||
		(c.Policy.RequireAC && (c.Telemetry.OnACPower == nil || !*c.Telemetry.OnACPower)) ||
		(c.Policy.MinimumBatteryPct != nil && (c.Telemetry.BatteryPercent == nil || *c.Telemetry.BatteryPercent < *c.Policy.MinimumBatteryPct)) {
		return wire.AssignmentClaim{}, fmt.Errorf("fake worker rejected session/health/concurrency policy")
	}
	for _, input := range offer.Spec.Inputs {
		if !computeContainsString(c.Policy.AllowedArtifacts, input.StoreID) {
			return wire.AssignmentClaim{}, fmt.Errorf("fake worker rejected artifact policy")
		}
	}
	claim := wire.AssignmentClaim{
		AssignmentID: offer.AssignmentID,
		JobID:        offer.JobID,
		ExecutionID:  offer.ExecutionID,
		Epoch:        offer.Epoch,
		SpecHash:     offer.SpecHash,
		ReceiptID:    "receipt-" + offer.AssignmentID,
	}
	w.offers[offer.AssignmentID] = offer
	w.claims[offer.AssignmentID] = claim
	return claim, nil
}

func fakeComputeFenceKey(jobID, executionID string, epoch uint64) string {
	return fmt.Sprintf("%s:%s:%d", jobID, executionID, epoch)
}

func (w *fakeComputeWorker) receiveGrant(grant wire.AssignmentGrant, now time.Time) error {
	if prior, ok := w.grants[grant.AssignmentID]; ok {
		if prior != grant {
			return ErrComputeConflictingDuplicate
		}
		return nil
	}
	claim, ok := w.claims[grant.AssignmentID]
	if !ok || claim.JobID != grant.JobID || claim.ExecutionID != grant.ExecutionID || claim.Epoch != grant.Epoch {
		return fmt.Errorf("fake worker rejected grant without matching claim")
	}
	deadline, err := time.Parse(time.RFC3339, grant.LeaseUntil)
	if err != nil || !now.UTC().Before(deadline) {
		return ErrComputeExpiredLease
	}
	key := fakeComputeFenceKey(grant.JobID, grant.ExecutionID, grant.Epoch)
	if w.starts[key] != 0 {
		return errors.New("fake worker attempted a second synthetic start")
	}
	w.grants[grant.AssignmentID] = grant
	w.starts[key] = 1
	w.startedAt[key] = now.UTC()
	return nil
}

func (w *fakeComputeWorker) receiveRenewal(renewal wire.LeaseRenewal) error {
	key := fakeComputeFenceKey(renewal.JobID, renewal.ExecutionID, renewal.Epoch)
	grant, ok := w.grantForFence(key)
	if !ok || w.selfFenced[key] {
		return ErrComputeStaleExecutionFence
	}
	current := grant.LeaseUntil
	if prior, ok := w.renewals[key]; ok {
		if prior == renewal {
			return nil
		}
		current = prior.LeaseUntil
	}
	currentDeadline, currentErr := time.Parse(time.RFC3339, current)
	deadline, err := time.Parse(time.RFC3339, renewal.LeaseUntil)
	if currentErr != nil || err != nil || !deadline.After(currentDeadline) {
		return ErrComputeInvalidLifecycleState
	}
	w.renewals[key] = renewal
	return nil
}

func (w *fakeComputeWorker) grantForFence(key string) (wire.AssignmentGrant, bool) {
	for _, grant := range w.grants {
		if fakeComputeFenceKey(grant.JobID, grant.ExecutionID, grant.Epoch) == key {
			return grant, true
		}
	}
	return wire.AssignmentGrant{}, false
}

func (w *fakeComputeWorker) leaseDeadline(key string) (time.Time, bool) {
	if renewal, ok := w.renewals[key]; ok {
		deadline, err := time.Parse(time.RFC3339, renewal.LeaseUntil)
		return deadline, err == nil
	}
	grant, ok := w.grantForFence(key)
	if !ok {
		return time.Time{}, false
	}
	deadline, err := time.Parse(time.RFC3339, grant.LeaseUntil)
	return deadline, err == nil
}

func (w *fakeComputeWorker) applySelfFence(now time.Time) bool {
	changed := false
	for key := range w.starts {
		deadline, ok := w.leaseDeadline(key)
		if ok && !now.UTC().Before(deadline.Add(-computeWorkerFenceLead)) {
			if !w.selfFenced[key] {
				w.selfFenced[key] = true
				changed = true
			}
		}
	}
	return changed
}

func (w *fakeComputeWorker) syntheticLog(grant wire.AssignmentGrant, stream string, data []byte) (wire.LogChunk, error) {
	key := fakeComputeFenceKey(grant.JobID, grant.ExecutionID, grant.Epoch)
	if w.starts[key] != 1 || w.selfFenced[key] {
		return wire.LogChunk{}, ErrComputeStaleExecutionFence
	}
	sum := sha256.Sum256(data)
	chunk := wire.LogChunk{
		JobID: grant.JobID, ExecutionID: grant.ExecutionID, Epoch: grant.Epoch,
		Stream: stream, Sequence: 1, ByteOffset: 0, Data: base64.StdEncoding.EncodeToString(data),
		SHA256: "sha256:" + hex.EncodeToString(sum[:]), EOF: true,
	}
	w.bufferedLogs = append(w.bufferedLogs, chunk)
	return chunk, nil
}

func (w *fakeComputeWorker) syntheticTerminal(grant wire.AssignmentGrant, job wire.ComputeJobRecord, ended time.Time, leaseLost bool) (wire.TerminalResult, error) {
	key := fakeComputeFenceKey(grant.JobID, grant.ExecutionID, grant.Epoch)
	if w.starts[key] != 1 {
		return wire.TerminalResult{}, ErrComputeInvalidLifecycleState
	}
	if w.selfFenced[key] != leaseLost {
		return wire.TerminalResult{}, ErrComputeInvalidLifecycleState
	}
	zero := 0
	started := w.startedAt[key]
	result := wire.TerminalResult{
		SchemaVersion: 1, JobID: job.JobID, NodeID: w.candidate.Node.NodeID,
		ExecutionID: grant.ExecutionID, Epoch: grant.Epoch, Handler: job.Spec.Handler, SourceDigest: job.Spec.SourceDigest,
		StartedAt: computeControllerTime(started), EndedAt: computeControllerTime(ended),
		DurationMs: ended.UTC().Sub(started).Milliseconds(), ExitCode: &zero,
	}
	if leaseLost {
		result.ExitCode = nil
		result.ErrorKind = "lease_lost"
		result.Retryable = true
	}
	w.bufferedEnds = append(w.bufferedEnds, result)
	return result, nil
}

func TestFakeComputeWorkerOfferAndGrantDeduplication(t *testing.T) {
	h := newComputeControllerHarness(t, "job-fake-worker-dedupe")
	h.enableWorker()
	offer, _, err := h.controller.Offer(h.job.JobID, []computeCandidateState{h.worker.schedulingCandidate()})
	if err != nil {
		t.Fatal(err)
	}
	claim, err := h.worker.receiveOffer(offer, h.clock.Now())
	if err != nil {
		t.Fatal(err)
	}
	replayedClaim, err := h.worker.receiveOffer(offer, h.clock.Now())
	if err != nil || replayedClaim != claim {
		t.Fatalf("identical offer claim=%+v err=%v", replayedClaim, err)
	}
	conflictingOffer := offer
	conflictingOffer.Spec.TimeoutSec++
	if _, err := h.worker.receiveOffer(conflictingOffer, h.clock.Now()); !errors.Is(err, ErrComputeConflictingDuplicate) {
		t.Fatalf("conflicting offer error = %v", err)
	}
	key := fakeComputeFenceKey(offer.JobID, offer.ExecutionID, offer.Epoch)
	if h.worker.starts[key] != 0 {
		t.Fatal("fake handler started before a grant")
	}
	if _, err := h.controller.AcceptClaim(h.worker.candidate.Node.NodeID, claim); err != nil {
		t.Fatal(err)
	}
	grant, err := h.controller.Grant(h.job.JobID)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.worker.receiveGrant(grant, h.clock.Now()); err != nil {
		t.Fatal(err)
	}
	if err := h.worker.receiveGrant(grant, h.clock.Now()); err != nil {
		t.Fatalf("identical grant error = %v", err)
	}
	conflictingGrant := grant
	conflictingGrant.LeaseUntil = computeControllerTime(h.clock.Now().Add(2 * computeExecutionLease))
	if err := h.worker.receiveGrant(conflictingGrant, h.clock.Now()); !errors.Is(err, ErrComputeConflictingDuplicate) {
		t.Fatalf("conflicting grant error = %v", err)
	}
	if h.worker.starts[key] != 1 {
		t.Fatalf("fake starts = %d, want 1", h.worker.starts[key])
	}
}
