// Package wire contains stable transport declarations shared by SoluTerminal
// compute controllers and workers.
package wire

import (
	"encoding/json"
	"errors"
)

// ProtocolRange is the D7/D9 protocol and optional-feature range advertised by
// one peer. Job and artifact schema versions are deliberately independent of
// this transport negotiation.
type ProtocolRange struct {
	MinMajor int         `json:"minMajor"`
	MaxMajor int         `json:"maxMajor"`
	MinMinor int         `json:"minMinor"`
	MaxMinor int         `json:"maxMinor"`
	Features FeatureBits `json:"features"`
}

// FeatureBits is the D7/D9 bitmap of optional protocol capabilities. Unknown
// advertised optional bits are ignored; unknown required bits fail closed.
type FeatureBits uint64

const (
	// FeatureSequencedLogs is the D7/D9 sequenced-log capability.
	FeatureSequencedLogs FeatureBits = 1 << iota
	// FeatureArtifactStreaming is the D7/D9 artifact-streaming capability.
	FeatureArtifactStreaming
	// FeatureLeaseFencing is the D7/D9 lease-fencing capability.
	FeatureLeaseFencing
)

const (
	// ComputeProtocolMajorV1 is the only protocol major selected by D7/D9 v1
	// negotiation.
	ComputeProtocolMajorV1 = 1
	// InitialEnvelopeSequenceV1 is the D5/D7/D12 first sequence in each
	// direction of an established v1 session.
	InitialEnvelopeSequenceV1 = 1
	// ComputeFeatureMaskV1 contains exactly the optional features defined by
	// D7/D9 for protocol v1.
	ComputeFeatureMaskV1 = FeatureSequencedLogs | FeatureArtifactStreaming | FeatureLeaseFencing
)

// ComputeEnvelope is the signed, per-direction sequenced session envelope
// defined by D5/D7/D12.
type ComputeEnvelope struct {
	ProtocolMajor    int             `json:"protocolMajor"`
	ProtocolMinor    int             `json:"protocolMinor"`
	SessionID        string          `json:"sessionId"`
	Sequence         uint64          `json:"sequence"`
	MessageID        string          `json:"messageId"`
	Timestamp        string          `json:"timestamp"`
	Type             string          `json:"type"`
	RequiredFeatures FeatureBits     `json:"requiredFeatures"`
	Payload          json.RawMessage `json:"payload"`
	Signature        string          `json:"signature"`
}

// NodeEnrollmentRequest is the D5/D7/D12 node identity enrollment form. The
// single-use enrollment token is header-only and is never part of this JSON
// contract.
type NodeEnrollmentRequest struct {
	SchemaVersion         int           `json:"schemaVersion"`
	NodePublicKey         string        `json:"nodePublicKey"`
	ControllerFingerprint string        `json:"controllerFingerprint"`
	Protocol              ProtocolRange `json:"protocol"`
}

// NodeEnrollmentResponse is the D5/D7/D12 controller-proof and pending node
// identity returned after successful enrollment.
type NodeEnrollmentResponse struct {
	SchemaVersion   int        `json:"schemaVersion"`
	ControllerProof string     `json:"controllerProof"`
	Node            NodeRecord `json:"node"`
}

// NodeSessionHello is the D5/D7/D12 first authenticated-session negotiation
// form sent by a node.
type NodeSessionHello struct {
	NodeID      string        `json:"nodeId"`
	Protocol    ProtocolRange `json:"protocol"`
	ClientNonce string        `json:"clientNonce"`
}

// NodeSessionChallenge is the D5/D7/D12 fresh controller challenge and pinned
// controller-identity proof.
type NodeSessionChallenge struct {
	ChallengeID     string `json:"challengeId"`
	Challenge       string `json:"challenge"`
	ControllerProof string `json:"controllerProof"`
}

// NodeSessionResponse is the D5/D7/D12 node proof over the session challenge.
type NodeSessionResponse struct {
	ChallengeID string `json:"challengeId"`
	NodeProof   string `json:"nodeProof"`
}

// NodeSessionEstablished is the D5/D7/D12 established-session reply, including
// independently initialized sequence numbers for both directions.
type NodeSessionEstablished struct {
	SessionID                       string             `json:"sessionId"`
	Protocol                        NegotiatedProtocol `json:"protocol"`
	NodeToControllerInitialSequence uint64             `json:"nodeToControllerInitialSequence"`
	ControllerToNodeInitialSequence uint64             `json:"controllerToNodeInitialSequence"`
}

// NodeRecord is the D5/D7/D12 controller-owned durable node identity and
// approval record. D6 keeps its admin assertions distinct from observations.
// No standalone ControllerRecord is defined: controller identity is represented
// only by the pinned fingerprint and proof fields specified by the design.
type NodeRecord struct {
	SchemaVersion        int                  `json:"schemaVersion"`
	NodeID               string               `json:"nodeId"`
	TenantID             string               `json:"tenantId"`
	PublicKeyFingerprint string               `json:"publicKeyFingerprint"`
	State                string               `json:"state"`
	ApprovedAt           *string              `json:"approvedAt,omitempty"`
	RevokedAt            *string              `json:"revokedAt,omitempty"`
	Admin                AdminCapabilities    `json:"admin"`
	Observed             ObservedCapabilities `json:"observed"`
	Protocol             ProtocolRange        `json:"protocol"`
}

// HandlerVersion is the D6/D7 stable identifier and exact version for an
// admin-approved or observed compiled handler.
type HandlerVersion struct {
	HandlerID string `json:"handlerId"`
	Version   int    `json:"version"`
}

// AdminCapabilities contains the D6/D7 controller-admin assertions that cap a
// node's authority; observations cannot widen them.
type AdminCapabilities struct {
	ApprovedWorkspaceIDs []string         `json:"approvedWorkspaceIds"`
	ApprovedHandlers     []HandlerVersion `json:"approvedHandlers"`
	Labels               []string         `json:"labels,omitempty"`
	HandlerPreference    map[string]int   `json:"handlerPreference,omitempty"`
}

// ObservedCapabilities contains the D6/D7 sampled machine facts that may
// reduce eligibility but never grant authority.
type ObservedCapabilities struct {
	SampledAt        string            `json:"sampledAt"`
	OS               string            `json:"os"`
	Architecture     string            `json:"architecture"`
	LogicalCPU       int               `json:"logicalCpu"`
	TotalRAMBytes    int64             `json:"totalRamBytes"`
	GPUs             []GPUCapability   `json:"gpus,omitempty"`
	DetectedHandlers []HandlerVersion  `json:"detectedHandlers"`
	ToolVersions     map[string]string `json:"toolVersions,omitempty"`
	WorkspaceDigests map[string]string `json:"workspaceDigests,omitempty"`
	CacheDigests     []string          `json:"cacheDigests,omitempty"`
}

// GPUCapability is the D6/D7 observed GPU inventory record.
type GPUCapability struct {
	Vendor      string `json:"vendor"`
	Model       string `json:"model"`
	MemoryBytes int64  `json:"memoryBytes,omitempty"`
}

// NodeTelemetry is the D6/D7 dynamic, sampled node-resource and health record.
type NodeTelemetry struct {
	SchemaVersion      int            `json:"schemaVersion"`
	SampledAt          string         `json:"sampledAt"`
	SessionSequence    uint64         `json:"sessionSequence"`
	AvailableRAMBytes  int64          `json:"availableRamBytes"`
	AvailableDiskBytes int64          `json:"availableDiskBytes"`
	CPUPercent         float64        `json:"cpuPercent"`
	Load               *float64       `json:"load,omitempty"`
	InteractiveIdleSec int64          `json:"interactiveIdleSec"`
	OnACPower          *bool          `json:"onAcPower,omitempty"`
	BatteryPercent     *int           `json:"batteryPercent,omitempty"`
	ActiveJobs         int            `json:"activeJobs"`
	ActiveByKind       map[string]int `json:"activeByKind,omitempty"`
	Health             string         `json:"health"`
}

// ComputePolicy is the D6/D7 controller-owned maximum authority and resource
// policy applied to one node.
type ComputePolicy struct {
	SchemaVersion            int              `json:"schemaVersion"`
	TenantID                 string           `json:"tenantId"`
	AllowedPrincipals        []string         `json:"allowedPrincipals"`
	WorkspaceIDs             []string         `json:"workspaceIds"`
	AllowedArtifacts         []string         `json:"allowedArtifactStores"`
	AllowedHandlers          []HandlerVersion `json:"allowedHandlers"`
	AllowedJobKinds          []string         `json:"allowedJobKinds"`
	MaximumDataClass         string           `json:"maximumDataClass"`
	AllowRemoteTransfer      bool             `json:"allowRemoteTransfer"`
	MaxCPU                   int              `json:"maxCpu"`
	MaxGPU                   int              `json:"maxGpu"`
	MaxRAMBytes              int64            `json:"maxRamBytes"`
	MaxDiskBytes             int64            `json:"maxDiskBytes"`
	MaxConcurrency           int              `json:"maxConcurrency"`
	MaxConcurrencyByKind     map[string]int   `json:"maxConcurrencyByKind,omitempty"`
	MinimumIdleSec           int64            `json:"minimumIdleSec"`
	RequireAC                bool             `json:"requireAc"`
	MinimumBatteryPct        *int             `json:"minimumBatteryPct,omitempty"`
	CancelOnActivityHandlers []HandlerVersion `json:"cancelOnActivityHandlers,omitempty"`
}

// ArtifactRef is the D6/D7/D9 independently schema-versioned,
// content-addressed input or output record. It deliberately contains no URL,
// path, credential, or generic secret field.
type ArtifactRef struct {
	SchemaVersion int    `json:"schemaVersion"`
	StoreID       string `json:"storeId"`
	ArtifactID    string `json:"artifactId"`
	SHA256        string `json:"sha256"`
	MediaType     string `json:"mediaType"`
	SizeBytes     int64  `json:"sizeBytes"`
}

// JobRequirements is the D6/D7/D9 declarative eligibility requirement set for
// a compute job.
type JobRequirements struct {
	OS               []string    `json:"os,omitempty"`
	Architectures    []string    `json:"architectures,omitempty"`
	MinimumCPU       int         `json:"minimumCpu,omitempty"`
	MinimumGPU       int         `json:"minimumGpu,omitempty"`
	MinimumRAMBytes  int64       `json:"minimumRamBytes,omitempty"`
	MinimumDiskBytes int64       `json:"minimumDiskBytes,omitempty"`
	RequiredFeatures FeatureBits `json:"requiredFeatures"`
	RequireDataOwner bool        `json:"requireDataOwner,omitempty"`
	DataOwnerNodeID  string      `json:"dataOwnerNodeId,omitempty"`
}

// ComputeJobSpec is the D6/D7/D9 immutable, independently schema-versioned job
// description. It uses approved IDs and profiles, never executable paths,
// shell/argv, environment, URLs, credentials, or generic secrets.
type ComputeJobSpec struct {
	SchemaVersion    int             `json:"schemaVersion"`
	JobKind          string          `json:"jobKind"`
	Handler          HandlerVersion  `json:"handler"`
	HandlerProfile   string          `json:"handlerProfile"`
	WorkspaceID      string          `json:"workspaceId"`
	Revision         string          `json:"revision"`
	SourceDigest     string          `json:"sourceDigest"`
	DataClass        string          `json:"dataClass"`
	Requirements     JobRequirements `json:"requirements"`
	Inputs           []ArtifactRef   `json:"inputs"`
	Priority         string          `json:"priority"`
	TimeoutSec       int             `json:"timeoutSec"`
	CancelPolicy     string          `json:"cancelPolicy"`
	MaxAttempts      int             `json:"maxAttempts"`
	RetryTimeout     bool            `json:"retryTimeout,omitempty"`
	RequiredFeatures FeatureBits     `json:"requiredFeatures"`
}

// ComputeJobSubmitRequest is the D6/D7/D9 untrusted submission form.
// ComputeAuthz cannot be submitted: the controller derives and stamps it only
// after authenticating the request.
type ComputeJobSubmitRequest struct {
	IdempotencyKey string         `json:"idempotencyKey"`
	Spec           ComputeJobSpec `json:"spec"`
}

// ComputeAuthz is the D6/D7/D9 durable controller-derived authority stamped
// after request authentication; it is not accepted from a submission body.
type ComputeAuthz struct {
	TenantID    string `json:"tenantId"`
	Principal   string `json:"principal"`
	SubmittedAt string `json:"submittedAt"`
}

// AssignmentAuthorization is the D7/D8/D12 controller-derived authority bound
// to one assignment offer and immutable spec hash.
type AssignmentAuthorization struct {
	TenantID     string `json:"tenantId"`
	Principal    string `json:"principal"`
	AssignmentID string `json:"assignmentId"`
	SpecHash     string `json:"specHash"`
	IssuedAt     string `json:"issuedAt"`
	ExpiresAt    string `json:"expiresAt"`
}

// ComputeJobRecord is the D6/D7/D9 durable controller-owned job record joining
// an immutable spec to controller-derived authority.
type ComputeJobRecord struct {
	SchemaVersion  int            `json:"schemaVersion"`
	JobID          string         `json:"jobId"`
	IdempotencyKey string         `json:"idempotencyKey"`
	SpecHash       string         `json:"specHash"`
	Spec           ComputeJobSpec `json:"spec"`
	Authz          ComputeAuthz   `json:"authz"`
	State          string         `json:"state"`
	QueueReason    string         `json:"queueReason,omitempty"`
	Attempt        int            `json:"attempt"`
}

// AssignmentOffer is the D7/D8/D12 controller offer for one fenced execution
// epoch, including its bound authorization.
type AssignmentOffer struct {
	AssignmentID string                  `json:"assignmentId"`
	JobID        string                  `json:"jobId"`
	ExecutionID  string                  `json:"executionId"`
	Epoch        uint64                  `json:"epoch"`
	SpecHash     string                  `json:"specHash"`
	Spec         ComputeJobSpec          `json:"spec"`
	Authz        AssignmentAuthorization `json:"authz"`
	ExpiresAt    string                  `json:"expiresAt"`
}

// AssignmentClaim is the D7/D8/D12 worker's durable, deduplicated claim for an
// offered assignment and epoch.
type AssignmentClaim struct {
	AssignmentID string `json:"assignmentId"`
	JobID        string `json:"jobId"`
	ExecutionID  string `json:"executionId"`
	Epoch        uint64 `json:"epoch"`
	SpecHash     string `json:"specHash"`
	ReceiptID    string `json:"receiptId"`
}

// AssignmentGrant is the D7/D8/D12 controller grant that authorizes one
// execution epoch until its lease deadline.
type AssignmentGrant struct {
	AssignmentID string `json:"assignmentId"`
	JobID        string `json:"jobId"`
	ExecutionID  string `json:"executionId"`
	Epoch        uint64 `json:"epoch"`
	LeaseUntil   string `json:"leaseUntil"`
}

// LeaseRenewal is the D7/D8/D12 controller renewal for the current fenced
// execution epoch.
type LeaseRenewal struct {
	JobID       string `json:"jobId"`
	ExecutionID string `json:"executionId"`
	Epoch       uint64 `json:"epoch"`
	LeaseUntil  string `json:"leaseUntil"`
}

// CancelRequest is the D7/D8/D12 repeated, fenced cancellation request for one
// execution epoch.
type CancelRequest struct {
	JobID       string `json:"jobId"`
	ExecutionID string `json:"executionId"`
	Epoch       uint64 `json:"epoch"`
	ReasonCode  string `json:"reasonCode"`
}

// LogChunk is the D7/D8/D12 independently sequenced and integrity-pinned log
// payload for one execution stream.
type LogChunk struct {
	JobID       string `json:"jobId"`
	ExecutionID string `json:"executionId"`
	Epoch       uint64 `json:"epoch"`
	Stream      string `json:"stream"`
	Sequence    uint64 `json:"sequence"`
	ByteOffset  int64  `json:"byteOffset"`
	Data        string `json:"data"`
	SHA256      string `json:"sha256"`
	EOF         bool   `json:"eof,omitempty"`
}

// LogAck is the D7/D8/D12 highest-contiguous-sequence acknowledgement for one
// execution stream.
type LogAck struct {
	JobID              string `json:"jobId"`
	ExecutionID        string `json:"executionId"`
	Stream             string `json:"stream"`
	ContiguousSequence uint64 `json:"contiguousSequence"`
}

// ComputeJobEvent is the D7/D8/D12 independently schema-versioned durable job
// event form.
type ComputeJobEvent struct {
	SchemaVersion int             `json:"schemaVersion"`
	Sequence      uint64          `json:"sequence"`
	At            string          `json:"at"`
	Type          string          `json:"type"`
	JobID         string          `json:"jobId"`
	ExecutionID   string          `json:"executionId,omitempty"`
	Epoch         uint64          `json:"epoch,omitempty"`
	Payload       json.RawMessage `json:"payload,omitempty"`
}

// ResourceUsage is the D7/D8/D12 optional bounded resource-usage projection
// attached to a terminal result.
type ResourceUsage struct {
	PeakRAMBytes int64   `json:"peakRamBytes,omitempty"`
	CPUSeconds   float64 `json:"cpuSeconds,omitempty"`
	ReadBytes    int64   `json:"readBytes,omitempty"`
	WriteBytes   int64   `json:"writeBytes,omitempty"`
}

// TerminalResult is the D7/D8/D12 fenced terminal execution receipt with
// independently schema-versioned artifacts.
type TerminalResult struct {
	SchemaVersion int            `json:"schemaVersion"`
	JobID         string         `json:"jobId"`
	NodeID        string         `json:"nodeId"`
	ExecutionID   string         `json:"executionId"`
	Epoch         uint64         `json:"epoch"`
	Handler       HandlerVersion `json:"handler"`
	SourceDigest  string         `json:"sourceDigest"`
	StartedAt     string         `json:"startedAt"`
	EndedAt       string         `json:"endedAt"`
	DurationMs    int64          `json:"durationMs"`
	ExitCode      *int           `json:"exitCode,omitempty"`
	ErrorKind     string         `json:"errorKind,omitempty"`
	Retryable     bool           `json:"retryable"`
	Usage         ResourceUsage  `json:"usage"`
	Artifacts     []ArtifactRef  `json:"artifacts,omitempty"`
}

// NegotiatedProtocol is the D7/D9 selected v1 protocol minor and known common
// optional-feature intersection.
type NegotiatedProtocol struct {
	Major    int         `json:"major"`
	Minor    int         `json:"minor"`
	Features FeatureBits `json:"features"`
}

// ErrIncompatibleVersion is the stable D7/D9 error returned when protocol or
// required-feature negotiation cannot safely proceed.
var ErrIncompatibleVersion = errors.New("incompatible_version")

// CheckRequiredFeatures applies the D7/D9 fail-closed required-feature rule.
// Unknown optional bits in available are ignored, while unknown required bits
// are incompatible.
func CheckRequiredFeatures(available, required FeatureBits) error {
	if required&^ComputeFeatureMaskV1 != 0 || available&required != required {
		return ErrIncompatibleVersion
	}
	return nil
}

// NegotiateProtocol performs D7/D9 framing negotiation independently of all
// job and artifact schemaVersion fields. It selects protocol major 1, the
// highest common minor, and only the known common optional features.
func NegotiateProtocol(local, remote ProtocolRange, required FeatureBits) (NegotiatedProtocol, error) {
	if !validProtocolRange(local) || !validProtocolRange(remote) ||
		!includesProtocolMajorV1(local) || !includesProtocolMajorV1(remote) {
		return NegotiatedProtocol{}, ErrIncompatibleVersion
	}

	minimumMinor := max(local.MinMinor, remote.MinMinor)
	maximumMinor := min(local.MaxMinor, remote.MaxMinor)
	if minimumMinor > maximumMinor {
		return NegotiatedProtocol{}, ErrIncompatibleVersion
	}

	features := local.Features & remote.Features & ComputeFeatureMaskV1
	if err := CheckRequiredFeatures(features, required); err != nil {
		return NegotiatedProtocol{}, err
	}
	return NegotiatedProtocol{
		Major:    ComputeProtocolMajorV1,
		Minor:    maximumMinor,
		Features: features,
	}, nil
}

func validProtocolRange(protocol ProtocolRange) bool {
	return protocol.MinMajor >= 0 && protocol.MaxMajor >= 0 &&
		protocol.MinMinor >= 0 && protocol.MaxMinor >= 0 &&
		protocol.MinMajor <= protocol.MaxMajor &&
		protocol.MinMinor <= protocol.MaxMinor
}

func includesProtocolMajorV1(protocol ProtocolRange) bool {
	return protocol.MinMajor <= ComputeProtocolMajorV1 && protocol.MaxMajor >= ComputeProtocolMajorV1
}
