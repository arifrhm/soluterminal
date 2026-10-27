package main

import (
	"testing"
	"time"

	"soluterminal-shared/wire"
)

const (
	computeTestGiB = int64(1 << 30)
	computeTestTiB = int64(1 << 40)
)

func TestEvaluateComputeCandidateD10Filters(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*wire.ComputeJobRecord, *computeCandidateState)
		filter int
		reason string
	}{
		{
			name: "filter 1 unauthenticated wins over later failures",
			mutate: func(_ *wire.ComputeJobRecord, candidate *computeCandidateState) {
				candidate.SessionAuthenticated = false
				candidate.LocalEnabled = false
				candidate.Telemetry.Health = "degraded"
			},
			filter: computeFilterIdentity,
			reason: "node_unauthenticated",
		},
		{
			name: "filter 1 unknown identity",
			mutate: func(_ *wire.ComputeJobRecord, candidate *computeCandidateState) {
				candidate.Node.PublicKeyFingerprint = ""
			},
			filter: computeFilterIdentity,
			reason: "identity_unknown",
		},
		{
			name: "filter 1 tenant mismatch",
			mutate: func(job *wire.ComputeJobRecord, _ *computeCandidateState) {
				job.Authz.TenantID = "another-tenant"
			},
			filter: computeFilterIdentity,
			reason: "tenant_mismatch",
		},
		{
			name: "filter 1 principal not allowed",
			mutate: func(job *wire.ComputeJobRecord, _ *computeCandidateState) {
				job.Authz.Principal = "principal-denied"
			},
			filter: computeFilterIdentity,
			reason: "principal_not_allowed",
		},
		{
			name: "filter 2 unknown job data class",
			mutate: func(job *wire.ComputeJobRecord, _ *computeCandidateState) {
				job.Spec.DataClass = "secret"
			},
			filter: computeFilterDataPolicy,
			reason: "data_class_unknown",
		},
		{
			name: "filter 2 unknown policy data class",
			mutate: func(_ *wire.ComputeJobRecord, candidate *computeCandidateState) {
				candidate.Policy.MaximumDataClass = ""
			},
			filter: computeFilterDataPolicy,
			reason: "data_class_unknown",
		},
		{
			name: "filter 2 class exceeds policy",
			mutate: func(job *wire.ComputeJobRecord, candidate *computeCandidateState) {
				job.Spec.DataClass = "confidential"
				candidate.Policy.MaximumDataClass = "internal"
			},
			filter: computeFilterDataPolicy,
			reason: "data_class_exceeds_policy",
		},
		{
			name: "filter 2 requested owner absent",
			mutate: func(job *wire.ComputeJobRecord, _ *computeCandidateState) {
				job.Spec.Requirements.RequireDataOwner = true
				job.Spec.Requirements.DataOwnerNodeID = "node-owner"
			},
			filter: computeFilterDataPolicy,
			reason: "data_owner_required",
		},
		{
			name: "filter 2 confidential is owner only",
			mutate: func(job *wire.ComputeJobRecord, candidate *computeCandidateState) {
				job.Spec.DataClass = "confidential"
				candidate.Policy.MaximumDataClass = "restricted"
				job.Spec.Requirements.DataOwnerNodeID = "node-owner"
			},
			filter: computeFilterDataPolicy,
			reason: "data_owner_required",
		},
		{
			name: "filter 2 transfer rejected",
			mutate: func(_ *wire.ComputeJobRecord, candidate *computeCandidateState) {
				candidate.Node.Observed.WorkspaceDigests = nil
				candidate.Node.Observed.CacheDigests = nil
				candidate.Policy.AllowRemoteTransfer = false
			},
			filter: computeFilterDataPolicy,
			reason: "remote_transfer_disallowed",
		},
		{
			name: "filter 3 job kind policy rejection",
			mutate: func(_ *wire.ComputeJobRecord, candidate *computeCandidateState) {
				candidate.Policy.AllowedJobKinds = nil
			},
			filter: computeFilterAuthority,
			reason: "job_kind_disallowed",
		},
		{
			name: "filter 3 admin handler required despite observation",
			mutate: func(_ *wire.ComputeJobRecord, candidate *computeCandidateState) {
				candidate.Node.Admin.ApprovedHandlers = nil
			},
			filter: computeFilterAuthority,
			reason: "handler_disallowed",
		},
		{
			name: "filter 3 exact policy handler version",
			mutate: func(_ *wire.ComputeJobRecord, candidate *computeCandidateState) {
				candidate.Policy.AllowedHandlers[0].Version++
			},
			filter: computeFilterAuthority,
			reason: "handler_disallowed",
		},
		{
			name: "filter 3 admin workspace required despite observed digest",
			mutate: func(_ *wire.ComputeJobRecord, candidate *computeCandidateState) {
				candidate.Node.Admin.ApprovedWorkspaceIDs = nil
			},
			filter: computeFilterAuthority,
			reason: "workspace_disallowed",
		},
		{
			name: "filter 3 policy workspace required",
			mutate: func(_ *wire.ComputeJobRecord, candidate *computeCandidateState) {
				candidate.Policy.WorkspaceIDs = nil
			},
			filter: computeFilterAuthority,
			reason: "workspace_disallowed",
		},
		{
			name: "filter 3 artifact required",
			mutate: func(job *wire.ComputeJobRecord, _ *computeCandidateState) {
				job.Spec.Inputs = nil
			},
			filter: computeFilterAuthority,
			reason: "artifact_missing",
		},
		{
			name: "filter 3 artifact must be content addressed",
			mutate: func(job *wire.ComputeJobRecord, _ *computeCandidateState) {
				job.Spec.Inputs[0].SHA256 = ""
			},
			filter: computeFilterAuthority,
			reason: "artifact_missing",
		},
		{
			name: "filter 3 artifact store required",
			mutate: func(job *wire.ComputeJobRecord, _ *computeCandidateState) {
				job.Spec.Inputs[0].StoreID = ""
			},
			filter: computeFilterAuthority,
			reason: "artifact_missing",
		},
		{
			name: "filter 3 artifact source policy rejection",
			mutate: func(job *wire.ComputeJobRecord, _ *computeCandidateState) {
				job.Spec.Inputs[0].StoreID = "store-denied"
			},
			filter: computeFilterAuthority,
			reason: "artifact_source_disallowed",
		},
		{
			name: "filter 4 OS mismatch",
			mutate: func(job *wire.ComputeJobRecord, _ *computeCandidateState) {
				job.Spec.Requirements.OS = []string{"windows"}
			},
			filter: computeFilterCapabilities,
			reason: "os_unsupported",
		},
		{
			name: "filter 4 architecture mismatch",
			mutate: func(job *wire.ComputeJobRecord, _ *computeCandidateState) {
				job.Spec.Requirements.Architectures = []string{"arm64"}
			},
			filter: computeFilterCapabilities,
			reason: "architecture_unsupported",
		},
		{
			name: "filter 4 approved but unobserved handler unavailable",
			mutate: func(_ *wire.ComputeJobRecord, candidate *computeCandidateState) {
				candidate.Node.Observed.DetectedHandlers = nil
			},
			filter: computeFilterCapabilities,
			reason: "handler_unavailable",
		},
		{
			name: "filter 5 unknown negative resource",
			mutate: func(_ *wire.ComputeJobRecord, candidate *computeCandidateState) {
				candidate.Telemetry.AvailableRAMBytes = -1
			},
			filter: computeFilterResources,
			reason: "resource_unknown",
		},
		{
			name: "filter 5 unknown physical CPU inventory",
			mutate: func(_ *wire.ComputeJobRecord, candidate *computeCandidateState) {
				candidate.Node.Observed.LogicalCPU = 0
			},
			filter: computeFilterResources,
			reason: "resource_unknown",
		},
		{
			name: "filter 5 CPU exceeds controller ceiling",
			mutate: func(job *wire.ComputeJobRecord, candidate *computeCandidateState) {
				job.Spec.Requirements.MinimumCPU = 5
				candidate.Policy.MaxCPU = 4
			},
			filter: computeFilterResources,
			reason: "cpu_insufficient",
		},
		{
			name: "filter 5 GPU exceeds observed count",
			mutate: func(job *wire.ComputeJobRecord, candidate *computeCandidateState) {
				job.Spec.Requirements.MinimumGPU = 2
				candidate.Policy.MaxGPU = 2
			},
			filter: computeFilterResources,
			reason: "gpu_insufficient",
		},
		{
			name: "filter 5 RAM current capacity exclusion",
			mutate: func(job *wire.ComputeJobRecord, candidate *computeCandidateState) {
				candidate.Telemetry.AvailableRAMBytes = job.Spec.Requirements.MinimumRAMBytes - 1
			},
			filter: computeFilterResources,
			reason: "ram_insufficient",
		},
		{
			name: "filter 5 disk controller ceiling exclusion",
			mutate: func(job *wire.ComputeJobRecord, candidate *computeCandidateState) {
				candidate.Policy.MaxDiskBytes = job.Spec.Requirements.MinimumDiskBytes - 1
			},
			filter: computeFilterResources,
			reason: "disk_insufficient",
		},
		{
			name: "filter 6 local disabled",
			mutate: func(_ *wire.ComputeJobRecord, candidate *computeCandidateState) {
				candidate.LocalEnabled = false
			},
			filter: computeFilterEnablement,
			reason: "local_disabled",
		},
		{
			name: "filter 6 enrollment not enabled",
			mutate: func(_ *wire.ComputeJobRecord, candidate *computeCandidateState) {
				candidate.Node.State = "pending"
			},
			filter: computeFilterEnablement,
			reason: "controller_disabled",
		},
		{
			name: "filter 6 approval absent",
			mutate: func(_ *wire.ComputeJobRecord, candidate *computeCandidateState) {
				candidate.Node.ApprovedAt = nil
			},
			filter: computeFilterEnablement,
			reason: "controller_disabled",
		},
		{
			name: "filter 6 revocation present",
			mutate: func(_ *wire.ComputeJobRecord, candidate *computeCandidateState) {
				revoked := "2026-09-01T01:00:00Z"
				candidate.Node.RevokedAt = &revoked
			},
			filter: computeFilterEnablement,
			reason: "controller_disabled",
		},
		{
			name: "filter 6 human not idle",
			mutate: func(_ *wire.ComputeJobRecord, candidate *computeCandidateState) {
				candidate.Telemetry.InteractiveIdleSec = candidate.Policy.MinimumIdleSec - 1
			},
			filter: computeFilterEnablement,
			reason: "human_not_idle",
		},
		{
			name: "filter 6 AC unknown",
			mutate: func(_ *wire.ComputeJobRecord, candidate *computeCandidateState) {
				candidate.Telemetry.OnACPower = nil
			},
			filter: computeFilterEnablement,
			reason: "ac_required",
		},
		{
			name: "filter 6 battery unknown",
			mutate: func(_ *wire.ComputeJobRecord, candidate *computeCandidateState) {
				candidate.Telemetry.BatteryPercent = nil
			},
			filter: computeFilterEnablement,
			reason: "battery_unknown",
		},
		{
			name: "filter 6 battery low",
			mutate: func(_ *wire.ComputeJobRecord, candidate *computeCandidateState) {
				low := *candidate.Policy.MinimumBatteryPct - 1
				candidate.Telemetry.BatteryPercent = &low
			},
			filter: computeFilterEnablement,
			reason: "battery_low",
		},
		{
			name: "filter 7 protocol incompatible",
			mutate: func(_ *wire.ComputeJobRecord, candidate *computeCandidateState) {
				candidate.Protocol.Major = 2
			},
			filter: computeFilterProtocol,
			reason: "protocol_incompatible",
		},
		{
			name: "filter 7 feature union missing",
			mutate: func(job *wire.ComputeJobRecord, candidate *computeCandidateState) {
				job.Spec.RequiredFeatures = wire.FeatureSequencedLogs
				job.Spec.Requirements.RequiredFeatures = wire.FeatureLeaseFencing
				candidate.Protocol.Features = wire.FeatureSequencedLogs
			},
			filter: computeFilterProtocol,
			reason: "required_features_missing",
		},
		{
			name: "filter 8 global concurrency unknown",
			mutate: func(_ *wire.ComputeJobRecord, candidate *computeCandidateState) {
				candidate.Policy.MaxConcurrency = 0
			},
			filter: computeFilterConcurrency,
			reason: "concurrency_unknown",
		},
		{
			name: "filter 8 global concurrency full",
			mutate: func(_ *wire.ComputeJobRecord, candidate *computeCandidateState) {
				candidate.Telemetry.ActiveJobs = candidate.Policy.MaxConcurrency
			},
			filter: computeFilterConcurrency,
			reason: "global_concurrency_full",
		},
		{
			name: "filter 8 per-kind concurrency unknown",
			mutate: func(job *wire.ComputeJobRecord, candidate *computeCandidateState) {
				candidate.Policy.MaxConcurrencyByKind[job.Spec.JobKind] = 0
			},
			filter: computeFilterConcurrency,
			reason: "concurrency_unknown",
		},
		{
			name: "filter 8 negative per-kind count without configured limit",
			mutate: func(job *wire.ComputeJobRecord, candidate *computeCandidateState) {
				candidate.Policy.MaxConcurrencyByKind = nil
				candidate.Telemetry.ActiveByKind[job.Spec.JobKind] = -1
			},
			filter: computeFilterConcurrency,
			reason: "concurrency_unknown",
		},
		{
			name: "filter 8 per-kind concurrency full",
			mutate: func(job *wire.ComputeJobRecord, candidate *computeCandidateState) {
				candidate.Telemetry.ActiveByKind[job.Spec.JobKind] = candidate.Policy.MaxConcurrencyByKind[job.Spec.JobKind]
			},
			filter: computeFilterConcurrency,
			reason: "kind_concurrency_full",
		},
		{
			name: "filter 8 sparse per-kind count is zero",
			mutate: func(job *wire.ComputeJobRecord, candidate *computeCandidateState) {
				candidate.Telemetry.ActiveByKind = map[string]int{"other.kind": 99}
				candidate.Policy.MaxConcurrencyByKind[job.Spec.JobKind] = 1
			},
			filter: 0,
		},
		{
			name: "filter 9 unhealthy",
			mutate: func(_ *wire.ComputeJobRecord, candidate *computeCandidateState) {
				candidate.Telemetry.Health = "degraded"
			},
			filter: computeFilterFreshness,
			reason: "health_unavailable",
		},
		{
			name: "filter 9 heartbeat stale",
			mutate: func(_ *wire.ComputeJobRecord, candidate *computeCandidateState) {
				candidate.HeartbeatFresh = false
			},
			filter: computeFilterFreshness,
			reason: "heartbeat_stale",
		},
		{
			name: "filter 9 session lease invalid",
			mutate: func(_ *wire.ComputeJobRecord, candidate *computeCandidateState) {
				candidate.SessionLeaseValid = false
			},
			filter: computeFilterFreshness,
			reason: "session_invalid",
		},
		{
			name: "filter 9 telemetry freshness flag stale",
			mutate: func(_ *wire.ComputeJobRecord, candidate *computeCandidateState) {
				candidate.TelemetryFresh = false
			},
			filter: computeFilterFreshness,
			reason: "telemetry_stale",
		},
		{
			name: "filter 9 empty telemetry sample",
			mutate: func(_ *wire.ComputeJobRecord, candidate *computeCandidateState) {
				candidate.Telemetry.SampledAt = ""
			},
			filter: computeFilterFreshness,
			reason: "telemetry_stale",
		},
		{
			name: "filter 9 zero session sequence",
			mutate: func(_ *wire.ComputeJobRecord, candidate *computeCandidateState) {
				candidate.Telemetry.SessionSequence = 0
			},
			filter: computeFilterFreshness,
			reason: "telemetry_stale",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			job, candidate := eligibleComputeFixture()
			test.mutate(&job, &candidate)
			_, rejection := evaluateComputeCandidate(job, candidate)
			if test.filter == 0 {
				if rejection != nil {
					t.Fatalf("unexpected rejection: %+v", *rejection)
				}
				return
			}
			if rejection == nil {
				t.Fatalf("candidate accepted, want filter %d reason %q", test.filter, test.reason)
			}
			if rejection.NodeID != candidate.Node.NodeID || rejection.Filter != test.filter || rejection.Reason != test.reason {
				t.Fatalf("rejection = %+v, want node %q filter %d reason %q", *rejection, candidate.Node.NodeID, test.filter, test.reason)
			}
		})
	}
}

func TestComputeScheduleRejectsEveryNonD11Tuple(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*wire.ComputeJobSpec)
	}{
		{name: "job kind", mutate: func(spec *wire.ComputeJobSpec) { spec.JobKind = "repo.verify.v2" }},
		{name: "handler ID", mutate: func(spec *wire.ComputeJobSpec) { spec.Handler.HandlerID = "shared-tests" }},
		{name: "handler version", mutate: func(spec *wire.ComputeJobSpec) { spec.Handler.Version = 2 }},
		{name: "profile", mutate: func(spec *wire.ComputeJobSpec) { spec.HandlerProfile = "other-profile.v1" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			job, candidate := eligibleComputeFixture()
			test.mutate(&job.Spec)
			candidate.SessionAuthenticated = false // proves nodes are not evaluated
			decision := computeSchedule(job, []computeCandidateState{candidate})
			if decision.SelectedNodeID != "" || decision.QueueReason != "unsupported_compute_job" || len(decision.Rejections) != 0 {
				t.Fatalf("decision = %+v, want unsupported tuple without candidate evaluation", decision)
			}
		})
	}
}

func eligibleComputeFixture() (wire.ComputeJobRecord, computeCandidateState) {
	handler := wire.HandlerVersion{HandlerID: computeHandlerSharedTests, Version: computeHandlerVersionV1}
	approvedAt := "2026-09-01T00:00:00Z"
	onAC := true
	battery := 90
	minimumBattery := 50
	load := 0.25
	job := wire.ComputeJobRecord{
		SchemaVersion: 1,
		JobID:         "job-1",
		Spec: wire.ComputeJobSpec{
			SchemaVersion:  1,
			JobKind:        computeJobKindRemoteVerify,
			Handler:        handler,
			HandlerProfile: computeProfileSharedTests,
			WorkspaceID:    "workspace-1",
			Revision:       "revision-1",
			SourceDigest:   "sha256:source-1",
			DataClass:      "internal",
			Requirements: wire.JobRequirements{
				OS:               []string{"linux"},
				Architectures:    []string{"amd64"},
				MinimumCPU:       2,
				MinimumRAMBytes:  2 * computeTestGiB,
				MinimumDiskBytes: computeTestGiB,
				RequiredFeatures: wire.FeatureSequencedLogs,
			},
			Inputs: []wire.ArtifactRef{{
				SchemaVersion: 1,
				StoreID:       "store-1",
				ArtifactID:    "artifact-1",
				SHA256:        "sha256:artifact-1",
				MediaType:     "application/x-tar",
				SizeBytes:     1024,
			}},
		},
		Authz: wire.ComputeAuthz{TenantID: "tenant-1", Principal: "principal-1"},
	}
	candidate := computeCandidateState{
		Node: wire.NodeRecord{
			SchemaVersion:        1,
			NodeID:               "node-a",
			TenantID:             "tenant-1",
			PublicKeyFingerprint: "fingerprint-a",
			State:                "enabled",
			ApprovedAt:           &approvedAt,
			Admin: wire.AdminCapabilities{
				ApprovedWorkspaceIDs: []string{"workspace-1"},
				ApprovedHandlers:     []wire.HandlerVersion{handler},
				HandlerPreference:    map[string]int{computeHandlerSharedTests: 10},
			},
			Observed: wire.ObservedCapabilities{
				SampledAt:        "2026-09-01T01:00:00Z",
				OS:               "linux",
				Architecture:     "amd64",
				LogicalCPU:       8,
				TotalRAMBytes:    16 * computeTestGiB,
				GPUs:             []wire.GPUCapability{{Vendor: "test", Model: "gpu"}},
				DetectedHandlers: []wire.HandlerVersion{handler},
				WorkspaceDigests: map[string]string{"workspace-1": "sha256:source-1"},
				CacheDigests:     []string{"sha256:other"},
			},
		},
		Policy: wire.ComputePolicy{
			SchemaVersion:        1,
			TenantID:             "tenant-1",
			AllowedPrincipals:    []string{"principal-1"},
			WorkspaceIDs:         []string{"workspace-1"},
			AllowedArtifacts:     []string{"store-1"},
			AllowedHandlers:      []wire.HandlerVersion{handler},
			AllowedJobKinds:      []string{computeJobKindRemoteVerify},
			MaximumDataClass:     "restricted",
			AllowRemoteTransfer:  true,
			MaxCPU:               8,
			MaxGPU:               1,
			MaxRAMBytes:          16 * computeTestGiB,
			MaxDiskBytes:         computeTestTiB,
			MaxConcurrency:       4,
			MaxConcurrencyByKind: map[string]int{computeJobKindRemoteVerify: 2},
			MinimumIdleSec:       300,
			RequireAC:            true,
			MinimumBatteryPct:    &minimumBattery,
		},
		Telemetry: wire.NodeTelemetry{
			SchemaVersion:      1,
			SampledAt:          "2026-09-01T01:00:00Z",
			SessionSequence:    7,
			AvailableRAMBytes:  12 * computeTestGiB,
			AvailableDiskBytes: 100 * computeTestGiB,
			CPUPercent:         20,
			Load:               &load,
			InteractiveIdleSec: 600,
			OnACPower:          &onAC,
			BatteryPercent:     &battery,
			ActiveJobs:         1,
			ActiveByKind:       map[string]int{computeJobKindRemoteVerify: 1},
			Health:             "healthy",
		},
		Protocol: wire.NegotiatedProtocol{
			Major:    wire.ComputeProtocolMajorV1,
			Minor:    0,
			Features: wire.ComputeFeatureMaskV1,
		},
		SessionAuthenticated: true,
		LocalEnabled:         true,
		HeartbeatFresh:       true,
		SessionLeaseValid:    true,
		TelemetryFresh:       true,
		LastAssignedAt:       time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC),
	}
	return job, candidate
}
