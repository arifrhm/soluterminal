package main

import (
	"time"

	"soluterminal-shared/wire"
)

// computeCandidateState keeps controller authority, worker observations, and
// controller-derived session facts separate. In particular, Node.Admin and
// Policy bound authority while Node.Observed and Telemetry can only reduce a
// candidate's eligibility.
type computeCandidateState struct {
	Node      wire.NodeRecord
	Policy    wire.ComputePolicy
	Telemetry wire.NodeTelemetry
	Protocol  wire.NegotiatedProtocol

	SessionAuthenticated bool
	LocalEnabled         bool
	HeartbeatFresh       bool
	SessionLeaseValid    bool
	TelemetryFresh       bool
	LastAssignedAt       time.Time
}

const (
	computeFilterIdentity = iota + 1
	computeFilterDataPolicy
	computeFilterAuthority
	computeFilterCapabilities
	computeFilterResources
	computeFilterEnablement
	computeFilterProtocol
	computeFilterConcurrency
	computeFilterFreshness
)

type computeCandidateRejection struct {
	NodeID string
	Filter int
	Reason string
}

type computeDataLocality int

const (
	computeLocalityOwner computeDataLocality = iota
	computeLocalityCached
	computeLocalityTransfer
)

// evaluateComputeCandidate applies the D10 hard filters in their numbered
// order and returns exactly the first failure. The locality result is valid
// only when rejection is nil.
func evaluateComputeCandidate(job wire.ComputeJobRecord, candidate computeCandidateState) (computeDataLocality, *computeCandidateRejection) {
	reject := func(filter int, reason string) (computeDataLocality, *computeCandidateRejection) {
		return computeLocalityTransfer, &computeCandidateRejection{
			NodeID: candidate.Node.NodeID,
			Filter: filter,
			Reason: reason,
		}
	}

	// D10 filter 1: authenticated identities, one tenant, and an authorized
	// controller-stamped submitting principal.
	if !candidate.SessionAuthenticated {
		return reject(computeFilterIdentity, "node_unauthenticated")
	}
	if candidate.Node.NodeID == "" || candidate.Node.PublicKeyFingerprint == "" ||
		candidate.Node.TenantID == "" || candidate.Policy.TenantID == "" ||
		job.Authz.TenantID == "" || job.Authz.Principal == "" {
		return reject(computeFilterIdentity, "identity_unknown")
	}
	if candidate.Node.TenantID != candidate.Policy.TenantID || candidate.Node.TenantID != job.Authz.TenantID {
		return reject(computeFilterIdentity, "tenant_mismatch")
	}
	if !computeContainsString(candidate.Policy.AllowedPrincipals, job.Authz.Principal) {
		return reject(computeFilterIdentity, "principal_not_allowed")
	}

	// D10 filter 2: classification and locality. Confidential and restricted
	// data are always owner-only under D6, regardless of transfer policy.
	dataClass, dataClassKnown := computeDataClassRank(job.Spec.DataClass)
	maximumClass, maximumClassKnown := computeDataClassRank(candidate.Policy.MaximumDataClass)
	if !dataClassKnown || !maximumClassKnown {
		return reject(computeFilterDataPolicy, "data_class_unknown")
	}
	if dataClass > maximumClass {
		return reject(computeFilterDataPolicy, "data_class_exceeds_policy")
	}
	locality := computeCandidateLocality(job.Spec, candidate.Node)
	isOwner := locality == computeLocalityOwner
	if (job.Spec.Requirements.RequireDataOwner || dataClass >= computeDataClassConfidential) && !isOwner {
		return reject(computeFilterDataPolicy, "data_owner_required")
	}
	if locality == computeLocalityTransfer && !candidate.Policy.AllowRemoteTransfer {
		return reject(computeFilterDataPolicy, "remote_transfer_disallowed")
	}

	// D10 filter 3: controller admin authority and node policy must intersect.
	if !computeContainsString(candidate.Policy.AllowedJobKinds, job.Spec.JobKind) {
		return reject(computeFilterAuthority, "job_kind_disallowed")
	}
	if !computeContainsHandler(candidate.Node.Admin.ApprovedHandlers, job.Spec.Handler) ||
		!computeContainsHandler(candidate.Policy.AllowedHandlers, job.Spec.Handler) {
		return reject(computeFilterAuthority, "handler_disallowed")
	}
	if !computeContainsString(candidate.Node.Admin.ApprovedWorkspaceIDs, job.Spec.WorkspaceID) ||
		!computeContainsString(candidate.Policy.WorkspaceIDs, job.Spec.WorkspaceID) {
		return reject(computeFilterAuthority, "workspace_disallowed")
	}
	if len(job.Spec.Inputs) == 0 {
		return reject(computeFilterAuthority, "artifact_missing")
	}
	for _, input := range job.Spec.Inputs {
		if input.StoreID == "" || input.SHA256 == "" {
			return reject(computeFilterAuthority, "artifact_missing")
		}
		if !computeContainsString(candidate.Policy.AllowedArtifacts, input.StoreID) {
			return reject(computeFilterAuthority, "artifact_source_disallowed")
		}
	}

	// D10 filter 4: observations can prove that approved capabilities are
	// present, but cannot grant an unapproved capability (D6).
	if len(job.Spec.Requirements.OS) > 0 &&
		!computeContainsString(job.Spec.Requirements.OS, candidate.Node.Observed.OS) {
		return reject(computeFilterCapabilities, "os_unsupported")
	}
	if len(job.Spec.Requirements.Architectures) > 0 &&
		!computeContainsString(job.Spec.Requirements.Architectures, candidate.Node.Observed.Architecture) {
		return reject(computeFilterCapabilities, "architecture_unsupported")
	}
	if !computeContainsHandler(candidate.Node.Observed.DetectedHandlers, job.Spec.Handler) {
		return reject(computeFilterCapabilities, "handler_unavailable")
	}

	// D10 filter 5: all requested floors fit both controller ceilings and
	// currently observed capacity. Negative facts, and zero-valued physical
	// CPU/RAM inventory that cannot represent a machine, fail closed.
	requirements := job.Spec.Requirements
	if requirements.MinimumCPU < 0 || requirements.MinimumGPU < 0 ||
		requirements.MinimumRAMBytes < 0 || requirements.MinimumDiskBytes < 0 ||
		candidate.Policy.MaxCPU < 0 || candidate.Policy.MaxGPU < 0 ||
		candidate.Policy.MaxRAMBytes < 0 || candidate.Policy.MaxDiskBytes < 0 ||
		candidate.Node.Observed.LogicalCPU <= 0 || candidate.Node.Observed.TotalRAMBytes <= 0 ||
		candidate.Telemetry.AvailableRAMBytes < 0 || candidate.Telemetry.AvailableDiskBytes < 0 {
		return reject(computeFilterResources, "resource_unknown")
	}
	if requirements.MinimumCPU > candidate.Policy.MaxCPU || requirements.MinimumCPU > candidate.Node.Observed.LogicalCPU {
		return reject(computeFilterResources, "cpu_insufficient")
	}
	if requirements.MinimumGPU > candidate.Policy.MaxGPU || requirements.MinimumGPU > len(candidate.Node.Observed.GPUs) {
		return reject(computeFilterResources, "gpu_insufficient")
	}
	if requirements.MinimumRAMBytes > candidate.Policy.MaxRAMBytes ||
		requirements.MinimumRAMBytes > candidate.Node.Observed.TotalRAMBytes ||
		requirements.MinimumRAMBytes > candidate.Telemetry.AvailableRAMBytes {
		return reject(computeFilterResources, "ram_insufficient")
	}
	if requirements.MinimumDiskBytes > candidate.Policy.MaxDiskBytes ||
		requirements.MinimumDiskBytes > candidate.Telemetry.AvailableDiskBytes {
		return reject(computeFilterResources, "disk_insufficient")
	}

	// D10 filter 6: local and controller enablement plus human-idle and power
	// policy. Required but unknown power telemetry fails closed.
	if !candidate.LocalEnabled {
		return reject(computeFilterEnablement, "local_disabled")
	}
	if candidate.Node.State != "enabled" || candidate.Node.ApprovedAt == nil || candidate.Node.RevokedAt != nil {
		return reject(computeFilterEnablement, "controller_disabled")
	}
	if candidate.Policy.MinimumIdleSec < 0 || candidate.Telemetry.InteractiveIdleSec < 0 ||
		candidate.Telemetry.InteractiveIdleSec < candidate.Policy.MinimumIdleSec {
		return reject(computeFilterEnablement, "human_not_idle")
	}
	if candidate.Policy.RequireAC && (candidate.Telemetry.OnACPower == nil || !*candidate.Telemetry.OnACPower) {
		return reject(computeFilterEnablement, "ac_required")
	}
	if candidate.Policy.MinimumBatteryPct != nil {
		minimumBattery := *candidate.Policy.MinimumBatteryPct
		if minimumBattery < 0 || minimumBattery > 100 || candidate.Telemetry.BatteryPercent == nil ||
			*candidate.Telemetry.BatteryPercent < 0 || *candidate.Telemetry.BatteryPercent > 100 {
			return reject(computeFilterEnablement, "battery_unknown")
		}
		if *candidate.Telemetry.BatteryPercent < minimumBattery {
			return reject(computeFilterEnablement, "battery_low")
		}
	}

	// D10 filter 7: only a negotiated compute-v1 session with every required
	// spec- or requirement-level feature can receive the job.
	if candidate.Protocol.Major != wire.ComputeProtocolMajorV1 {
		return reject(computeFilterProtocol, "protocol_incompatible")
	}
	requiredFeatures := job.Spec.RequiredFeatures | job.Spec.Requirements.RequiredFeatures
	if wire.CheckRequiredFeatures(candidate.Protocol.Features, requiredFeatures) != nil {
		return reject(computeFilterProtocol, "required_features_missing")
	}

	// D10 filter 8: sparse per-kind active counts mean zero, while configured
	// limits and all supplied counts must be valid.
	if candidate.Policy.MaxConcurrency <= 0 || candidate.Telemetry.ActiveJobs < 0 {
		return reject(computeFilterConcurrency, "concurrency_unknown")
	}
	kindLimit, kindConfigured := candidate.Policy.MaxConcurrencyByKind[job.Spec.JobKind]
	kindActive := candidate.Telemetry.ActiveByKind[job.Spec.JobKind]
	if kindActive < 0 || (kindConfigured && kindLimit <= 0) {
		return reject(computeFilterConcurrency, "concurrency_unknown")
	}
	if candidate.Telemetry.ActiveJobs >= candidate.Policy.MaxConcurrency {
		return reject(computeFilterConcurrency, "global_concurrency_full")
	}
	if kindConfigured && kindActive >= kindLimit {
		return reject(computeFilterConcurrency, "kind_concurrency_full")
	}

	// D10 filter 9: current healthy telemetry must belong to a fresh heartbeat
	// and a still-valid authenticated session lease.
	if candidate.Telemetry.Health != "healthy" {
		return reject(computeFilterFreshness, "health_unavailable")
	}
	if !candidate.HeartbeatFresh {
		return reject(computeFilterFreshness, "heartbeat_stale")
	}
	if !candidate.SessionLeaseValid {
		return reject(computeFilterFreshness, "session_invalid")
	}
	if !candidate.TelemetryFresh || candidate.Telemetry.SampledAt == "" || candidate.Telemetry.SessionSequence == 0 {
		return reject(computeFilterFreshness, "telemetry_stale")
	}

	return locality, nil
}

const computeDataClassConfidential = 2

func computeDataClassRank(dataClass string) (int, bool) {
	switch dataClass {
	case "public":
		return 0, true
	case "internal":
		return 1, true
	case "confidential":
		return computeDataClassConfidential, true
	case "restricted":
		return 3, true
	default:
		return 0, false
	}
}

func computeCandidateLocality(spec wire.ComputeJobSpec, node wire.NodeRecord) computeDataLocality {
	if spec.Requirements.DataOwnerNodeID != "" && spec.Requirements.DataOwnerNodeID == node.NodeID {
		return computeLocalityOwner
	}
	if spec.SourceDigest != "" {
		if digest, ok := node.Observed.WorkspaceDigests[spec.WorkspaceID]; ok && digest == spec.SourceDigest {
			return computeLocalityCached
		}
		for _, digest := range node.Observed.CacheDigests {
			if digest == spec.SourceDigest {
				return computeLocalityCached
			}
		}
	}
	return computeLocalityTransfer
}

func computeContainsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func computeContainsHandler(values []wire.HandlerVersion, target wire.HandlerVersion) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
