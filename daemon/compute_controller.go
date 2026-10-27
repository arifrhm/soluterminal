package main

// The Phase 1 Compute Fabric controller joins the pure D10 scheduler to the
// D8 event store. It has no transport, goroutines, daemon wiring, or handler
// execution; callers explicitly drive every persist-before-delivery step.

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"time"

	"soluterminal-shared/wire"
)

const (
	computeOfferTTL        = 15 * time.Second
	computeExecutionLease  = 60 * time.Second
	computeRenewalCadence  = 20 * time.Second
	computeWorkerFenceLead = 5 * time.Second
)

type computeIDSource func(prefix string) (string, error)

type computeController struct {
	store *computeStore
	now   func() time.Time
	newID computeIDSource
}

func newComputeController(store *computeStore) *computeController {
	return newComputeControllerWithDependencies(store, func() time.Time { return time.Now().UTC() }, randomComputeID)
}

func newComputeControllerWithDependencies(store *computeStore, now func() time.Time, newID computeIDSource) *computeController {
	return &computeController{store: store, now: now, newID: newID}
}

func randomComputeID(prefix string) (string, error) {
	var raw [18]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return prefix + "-" + base64.RawURLEncoding.EncodeToString(raw[:]), nil
}

func computeControllerTime(value time.Time) string {
	return value.UTC().Format(time.RFC3339Nano)
}

// Offer schedules one queued/retry-wait job and durably records its selected
// node and epoch before returning the value a transport may later deliver.
func (c *computeController) Offer(jobID string, candidates []computeCandidateState) (wire.AssignmentOffer, computeScheduleDecision, error) {
	if _, err := c.ExpireDue(jobID); err != nil {
		return wire.AssignmentOffer{}, computeScheduleDecision{}, err
	}
	state, err := c.store.Load(jobID)
	if err != nil {
		return wire.AssignmentOffer{}, computeScheduleDecision{}, err
	}
	if state.Job.State != "queued" && state.Job.State != "retry_wait" {
		return wire.AssignmentOffer{}, computeScheduleDecision{}, fmt.Errorf("%w: cannot schedule job in %s", ErrComputeInvalidLifecycleState, state.Job.State)
	}
	decision := computeSchedule(state.Job, candidates)
	if decision.SelectedNodeID == "" {
		return wire.AssignmentOffer{}, decision, nil
	}
	assignmentID, err := c.newID("assignment")
	if err != nil {
		return wire.AssignmentOffer{}, decision, err
	}
	executionID, err := c.newID("execution")
	if err != nil {
		return wire.AssignmentOffer{}, decision, err
	}
	now := c.now().UTC()
	deadline := computeControllerTime(now.Add(computeOfferTTL))
	offer := wire.AssignmentOffer{
		AssignmentID: assignmentID,
		JobID:        state.Job.JobID,
		ExecutionID:  executionID,
		Epoch:        state.Fence.Epoch + 1,
		SpecHash:     state.Job.SpecHash,
		Spec:         state.Job.Spec,
		Authz: wire.AssignmentAuthorization{
			TenantID:     state.Job.Authz.TenantID,
			Principal:    state.Job.Authz.Principal,
			AssignmentID: assignmentID,
			SpecHash:     state.Job.SpecHash,
			IssuedAt:     computeControllerTime(now),
			ExpiresAt:    deadline,
		},
		ExpiresAt: deadline,
	}
	if err := c.store.RecordOffer(state.LastAppliedEventSequence, decision.SelectedNodeID, offer); err != nil {
		return wire.AssignmentOffer{}, decision, err
	}
	return offer, decision, nil
}

// AcceptClaim expires due authority first, then durably accepts the selected
// authenticated node's claim. A repeated claim after restart returns the
// already-durable grant, if one exists, without appending another record.
func (c *computeController) AcceptClaim(authenticatedNodeID string, claim wire.AssignmentClaim) (*wire.AssignmentGrant, error) {
	expired, expireErr := c.ExpireDue(claim.JobID)
	if expireErr != nil {
		return nil, expireErr
	}
	if err := c.store.RecordClaim(authenticatedNodeID, claim); err != nil {
		if expired == computeEventAssignmentExpired && errors.Is(err, ErrComputeStaleExecutionFence) {
			return nil, errors.Join(ErrComputeExpiredOffer, err)
		}
		return nil, err
	}
	state, err := c.store.Load(claim.JobID)
	if err != nil {
		return nil, err
	}
	if state.Grant != nil {
		grant := *state.Grant
		return &grant, nil
	}
	return nil, nil
}

// Grant returns the one durable grant for a claim. Persistence completes
// before the authority is returned, and repeated retrieval is read-only.
func (c *computeController) Grant(jobID string) (wire.AssignmentGrant, error) {
	expired, err := c.ExpireDue(jobID)
	if err != nil {
		return wire.AssignmentGrant{}, err
	}
	if expired == computeEventExecutionLapsed {
		return wire.AssignmentGrant{}, ErrComputeExpiredLease
	}
	state, err := c.store.Load(jobID)
	if err != nil {
		return wire.AssignmentGrant{}, err
	}
	if state.Job.State == "running" && state.Grant != nil {
		return *state.Grant, nil
	}
	if state.Job.State != "claimed" || state.Claim == nil || state.Offer == nil {
		return wire.AssignmentGrant{}, fmt.Errorf("%w: cannot grant job in %s", ErrComputeInvalidLifecycleState, state.Job.State)
	}
	grant := wire.AssignmentGrant{
		AssignmentID: state.Claim.AssignmentID,
		JobID:        state.Job.JobID,
		ExecutionID:  state.Claim.ExecutionID,
		Epoch:        state.Claim.Epoch,
		LeaseUntil:   computeControllerTime(c.now().UTC().Add(computeExecutionLease)),
	}
	if err := c.store.RecordGrant(state.LastAppliedEventSequence, state.Fence.AuthenticatedNodeID, grant); err != nil {
		return wire.AssignmentGrant{}, err
	}
	return grant, nil
}

// Renew records the extended current lease before returning it for delivery.
func (c *computeController) Renew(jobID string) (wire.LeaseRenewal, error) {
	expired, err := c.ExpireDue(jobID)
	if err != nil {
		return wire.LeaseRenewal{}, err
	}
	if expired == computeEventExecutionLapsed {
		return wire.LeaseRenewal{}, ErrComputeExpiredLease
	}
	state, err := c.store.Load(jobID)
	if err != nil {
		return wire.LeaseRenewal{}, err
	}
	if state.Job.State != "running" || state.Grant == nil {
		return wire.LeaseRenewal{}, fmt.Errorf("%w: cannot renew job in %s", ErrComputeInvalidLifecycleState, state.Job.State)
	}
	renewal := wire.LeaseRenewal{
		JobID:       state.Job.JobID,
		ExecutionID: state.Fence.ExecutionID,
		Epoch:       state.Fence.Epoch,
		LeaseUntil:  computeControllerTime(c.now().UTC().Add(computeExecutionLease)),
	}
	if state.Lease != nil && *state.Lease == renewal {
		return renewal, nil
	}
	previous, err := time.Parse(time.RFC3339, effectiveComputeLeaseDeadline(state))
	if err != nil || !timeMustParseController(renewal.LeaseUntil).After(previous) {
		return wire.LeaseRenewal{}, fmt.Errorf("%w: renewal does not extend current authority", ErrComputeInvalidLifecycleState)
	}
	if err := c.store.RecordLeaseRenewal(state.LastAppliedEventSequence, state.Fence.AuthenticatedNodeID, renewal); err != nil {
		return wire.LeaseRenewal{}, err
	}
	return renewal, nil
}

func timeMustParseController(value string) time.Time {
	parsed, _ := time.Parse(time.RFC3339, value)
	return parsed
}

// AcceptLog returns only the acknowledgement reconstructed after the chunk is
// durable. Due leases are lapsed before the worker record is audited stale.
func (c *computeController) AcceptLog(authenticatedNodeID string, chunk wire.LogChunk) (wire.LogAck, error) {
	expired, err := c.ExpireDue(chunk.JobID)
	if err != nil {
		return wire.LogAck{}, err
	}
	ack, err := c.store.RecordLogChunk(authenticatedNodeID, chunk)
	if expired == computeEventExecutionLapsed && errors.Is(err, ErrComputeStaleExecutionFence) {
		return wire.LogAck{}, errors.Join(ErrComputeExpiredLease, err)
	}
	return ack, err
}

// AcceptTerminal accepts a result only while the execution lease is current.
func (c *computeController) AcceptTerminal(authenticatedNodeID string, terminal wire.TerminalResult) error {
	expired, err := c.ExpireDue(terminal.JobID)
	if err != nil {
		return err
	}
	err = c.store.RecordTerminal(authenticatedNodeID, terminal)
	if expired == computeEventExecutionLapsed && errors.Is(err, ErrComputeStaleExecutionFence) {
		return errors.Join(ErrComputeExpiredLease, err)
	}
	return err
}

// ExpireDue scans exactly one job. It persists an unclaimed offer expiry or a
// running lease lapse at the controller deadline, otherwise it is read-only.
func (c *computeController) ExpireDue(jobID string) (string, error) {
	state, err := c.store.Load(jobID)
	if err != nil {
		return "", err
	}
	now := c.now().UTC()
	switch state.Job.State {
	case "offered":
		if state.Offer == nil || state.Claim != nil {
			return "", fmt.Errorf("%w: incomplete offered projection", ErrComputeInvalidLifecycleState)
		}
		deadline, err := time.Parse(time.RFC3339, state.Offer.ExpiresAt)
		if err != nil {
			return "", err
		}
		if now.Before(deadline) {
			return "", nil
		}
		expired := computeAssignmentExpired{
			AssignmentID:  state.Offer.AssignmentID,
			JobID:         state.Job.JobID,
			ExecutionID:   state.Offer.ExecutionID,
			Epoch:         state.Offer.Epoch,
			ControllerAt:  computeControllerTime(now),
			OfferDeadline: state.Offer.ExpiresAt,
		}
		if err := c.store.RecordAssignmentExpired(state.LastAppliedEventSequence, state.Fence.AuthenticatedNodeID, expired); err != nil {
			return "", err
		}
		return computeEventAssignmentExpired, nil
	case "running":
		deadlineValue := effectiveComputeLeaseDeadline(state)
		deadline, err := time.Parse(time.RFC3339, deadlineValue)
		if err != nil {
			return "", err
		}
		if now.Before(deadline) {
			return "", nil
		}
		if state.Offer == nil || state.Grant == nil {
			return "", fmt.Errorf("%w: incomplete running projection", ErrComputeInvalidLifecycleState)
		}
		lapsed := computeExecutionLapsed{
			AssignmentID:      state.Offer.AssignmentID,
			JobID:             state.Job.JobID,
			ExecutionID:       state.Fence.ExecutionID,
			Epoch:             state.Fence.Epoch,
			ControllerAt:      computeControllerTime(now),
			EffectiveDeadline: deadlineValue,
			Reason:            "lease_lost",
			Retry:             state.Job.Attempt < computeEffectiveMaxAttempts(state.Job.Spec),
		}
		if err := c.store.RecordExecutionLapsed(state.LastAppliedEventSequence, state.Fence.AuthenticatedNodeID, lapsed); err != nil {
			return "", err
		}
		return computeEventExecutionLapsed, nil
	default:
		return "", nil
	}
}
