package main

// Controller-owned persistence declarations for the Compute Fabric D2/D8
// store. Protocol values remain owned by shared/wire; these types contain only
// materialized state and persistence metadata that never crosses the wire.

import (
	"encoding/json"
	"errors"
	"fmt"

	"soluterminal-shared/wire"
)

const (
	// computeStoreReaderVersion is the control-semantic reader supported by
	// this binary. Fresh stores stay at the P1b version until the first grant,
	// expiry, or lapse makes version-2 semantics durable.
	computeStoreReaderVersion        = 2
	computeInitialStoreReaderVersion = 1
)

const (
	computeEventJobCreated        = "job.created"
	computeEventAssignmentOffered = "assignment.offered"
	computeEventAssignmentClaimed = "assignment.claimed"
	computeEventAssignmentGranted = "assignment.granted"
	computeEventAssignmentExpired = "assignment.expired"
	computeEventLeaseRenewed      = "lease.renewed"
	computeEventLogChunkAccepted  = "logs.chunk_accepted"
	computeEventExecutionLapsed   = "execution.lapsed"
	computeEventExecutionTerminal = "execution.terminal"
)

const (
	computeDispositionAccepted   = "accepted"
	computeDispositionStaleFence = "stale_fence"
)

var (
	ErrComputeIdempotencyConflict     = errors.New("compute idempotency conflict")
	ErrComputeConflictingDuplicate    = errors.New("compute conflicting duplicate")
	ErrComputeStaleControllerSequence = errors.New("compute stale controller sequence")
	ErrComputeStaleExecutionFence     = errors.New("compute stale execution fence")
	ErrComputeMalformedRecord         = errors.New("compute malformed record")
	ErrComputeCorruptStore            = errors.New("compute corrupt store")
	ErrComputeInvalidLifecycleState   = errors.New("compute invalid lifecycle state")
	ErrComputeExpiredOffer            = errors.New("compute assignment offer expired")
	ErrComputeExpiredLease            = errors.New("compute execution lease expired")
)

// ComputeIdempotencyConflictError identifies the durable job which already
// owns a controller submission scope without exposing the scope's internal
// index encoding.
type ComputeIdempotencyConflictError struct {
	ExistingJobID string
	ExistingHash  string
	RequestedHash string
}

func (e *ComputeIdempotencyConflictError) Error() string {
	return fmt.Sprintf("%v: job %s has %s, request has %s", ErrComputeIdempotencyConflict, e.ExistingJobID, e.ExistingHash, e.RequestedHash)
}

func (e *ComputeIdempotencyConflictError) Unwrap() error { return ErrComputeIdempotencyConflict }

type computeExecutionFence struct {
	JobID               string `json:"jobId"`
	ExecutionID         string `json:"executionId"`
	AuthenticatedNodeID string `json:"authenticatedNodeId"`
	Epoch               uint64 `json:"epoch"`
}

// computeAssignmentExpired is controller-internal persistence state. It is
// deliberately not a wire DTO: expiry is reconstructed from controller time
// and the exact authority that was withdrawn.
type computeAssignmentExpired struct {
	AssignmentID  string `json:"assignmentId"`
	JobID         string `json:"jobId"`
	ExecutionID   string `json:"executionId"`
	Epoch         uint64 `json:"epoch"`
	ControllerAt  string `json:"controllerAt"`
	OfferDeadline string `json:"offerDeadline"`
}

// computeExecutionLapsed is the durable D8 lease-loss decision. Retry is a
// controller decision recorded with the effective grant/renewal deadline so
// replay never re-evaluates policy using a newer binary.
type computeExecutionLapsed struct {
	AssignmentID      string `json:"assignmentId"`
	JobID             string `json:"jobId"`
	ExecutionID       string `json:"executionId"`
	Epoch             uint64 `json:"epoch"`
	ControllerAt      string `json:"controllerAt"`
	EffectiveDeadline string `json:"effectiveLeaseDeadline"`
	Reason            string `json:"reason"`
	Retry             bool   `json:"retry"`
}

type computeAppliedRecord struct {
	Digest      string `json:"digest"`
	Disposition string `json:"disposition"`
}

type computeLogChunkReceipt struct {
	Sequence   uint64 `json:"sequence"`
	ByteOffset int64  `json:"byteOffset"`
	SizeBytes  int    `json:"sizeBytes"`
	Digest     string `json:"digest"`
	EOF        bool   `json:"eof,omitempty"`
}

type computeLogStreamState struct {
	JobID              string                            `json:"jobId"`
	ExecutionID        string                            `json:"executionId"`
	Epoch              uint64                            `json:"epoch"`
	Stream             string                            `json:"stream"`
	ContiguousSequence uint64                            `json:"contiguousSequence"`
	NextByteOffset     int64                             `json:"nextByteOffset"`
	EOF                bool                              `json:"eof,omitempty"`
	Receipts           map[uint64]computeLogChunkReceipt `json:"receipts"`
}

// computeJobState is the materialized controller projection. The job record
// and every protocol transition are the P1a wire declarations, not parallel
// daemon DTOs.
type computeJobState struct {
	Job                      wire.ComputeJobRecord             `json:"job"`
	Offer                    *wire.AssignmentOffer             `json:"offer,omitempty"`
	Claim                    *wire.AssignmentClaim             `json:"claim,omitempty"`
	Grant                    *wire.AssignmentGrant             `json:"grant,omitempty"`
	Lease                    *wire.LeaseRenewal                `json:"lease,omitempty"`
	Terminal                 *wire.TerminalResult              `json:"terminal,omitempty"`
	LatestAssignmentExpiry   *computeAssignmentExpired         `json:"latestAssignmentExpiry,omitempty"`
	LatestExecutionLapse     *computeExecutionLapsed           `json:"latestExecutionLapse,omitempty"`
	Fence                    computeExecutionFence             `json:"fence"`
	LogStreams               map[string]*computeLogStreamState `json:"logStreams"`
	AppliedRecords           map[string]computeAppliedRecord   `json:"appliedRecords"`
	LastAppliedEventSequence uint64                            `json:"lastAppliedEventSequence"`
}

func computeEffectiveMaxAttempts(spec wire.ComputeJobSpec) int {
	if spec.MaxAttempts > 0 {
		return spec.MaxAttempts
	}
	if spec.JobKind == computeJobKindRemoteVerify &&
		spec.Handler.HandlerID == computeHandlerSharedTests &&
		spec.Handler.Version == computeHandlerVersionV1 &&
		spec.HandlerProfile == computeProfileSharedTests {
		return 2
	}
	return 1
}

// computeStoredRecord is the sole payload envelope added by the store. It
// preserves controller-authenticated identity, dedupe metadata, audit
// disposition, and the exact canonical P1a wire value.
type computeStoredRecord struct {
	RecordID            string          `json:"recordId"`
	Digest              string          `json:"digest"`
	AuthenticatedNodeID string          `json:"authenticatedNodeId,omitempty"`
	Disposition         string          `json:"disposition"`
	Record              json.RawMessage `json:"record"`
}

type computeIdempotencyEntry struct {
	TenantID       string `json:"tenantId"`
	Principal      string `json:"principal"`
	IdempotencyKey string `json:"idempotencyKey"`
	SpecHash       string `json:"specHash"`
	JobID          string `json:"jobId"`
}

type computeControllerState struct {
	Entries []computeIdempotencyEntry `json:"entries"`
}
