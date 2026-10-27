// Package main contains the pure Phase 1 compute scheduler. D6 is represented
// by separate controller authority, worker observation, telemetry, and session
// facts; D10 is implemented as filter-before-score eligibility followed by a
// deterministic lexicographic rank; and D11 restricts scheduling to the sole
// remote shared-test tuple below.
//
// Deliberate deferrals sanctioned by the design are: D10 queue ordering,
// preemption/checkpointing, and active-job
// cancellation; D11's next Windows-installer slice, TradingStack designed
// seam, deferred browser QA, and deferred AI inference/remote agents; and
// D8/D12 transport, real execution, and remote-session reconciliation.
package main

import (
	"math/big"
	"sort"
	"time"

	"soluterminal-shared/wire"
)

const (
	computeJobKindRemoteVerify = "repo.verify.v1"
	computeHandlerSharedTests  = "soluterminal-shared-tests"
	computeHandlerVersionV1    = 1
	computeProfileSharedTests  = "soluterminal-shared-tests.v1"
)

type computeScheduleDecision struct {
	SelectedNodeID string
	QueueReason    string
	Rejections     []computeCandidateRejection
}

type computeEligibleCandidate struct {
	candidate computeCandidateState
	locality  computeDataLocality
}

// computeSchedule is a pure D10 decision: it does not mutate input records or
// candidates, persist a reservation, or silently fall back to local execution.
func computeSchedule(job wire.ComputeJobRecord, candidates []computeCandidateState) computeScheduleDecision {
	if !computeSupportedJob(job.Spec) {
		return computeScheduleDecision{QueueReason: "unsupported_compute_job"}
	}

	eligible := make([]computeEligibleCandidate, 0, len(candidates))
	rejections := make([]computeCandidateRejection, 0, len(candidates))
	for _, candidate := range candidates {
		locality, rejection := evaluateComputeCandidate(job, candidate)
		if rejection != nil {
			rejections = append(rejections, *rejection)
			continue
		}
		eligible = append(eligible, computeEligibleCandidate{candidate: candidate, locality: locality})
	}
	sort.Slice(rejections, func(i, j int) bool {
		if rejections[i].NodeID != rejections[j].NodeID {
			return rejections[i].NodeID < rejections[j].NodeID
		}
		if rejections[i].Filter != rejections[j].Filter {
			return rejections[i].Filter < rejections[j].Filter
		}
		return rejections[i].Reason < rejections[j].Reason
	})
	if len(eligible) == 0 {
		return computeScheduleDecision{
			QueueReason: "no_eligible_node",
			Rejections:  rejections,
		}
	}

	sort.Slice(eligible, func(i, j int) bool {
		return computeCandidateRanksBefore(job, eligible[i], eligible[j])
	})
	return computeScheduleDecision{
		SelectedNodeID: eligible[0].candidate.Node.NodeID,
		Rejections:     rejections,
	}
}

func computeSupportedJob(spec wire.ComputeJobSpec) bool {
	return spec.JobKind == computeJobKindRemoteVerify &&
		spec.Handler.HandlerID == computeHandlerSharedTests &&
		spec.Handler.Version == computeHandlerVersionV1 &&
		spec.HandlerProfile == computeProfileSharedTests
}

func computeCandidateRanksBefore(job wire.ComputeJobRecord, left, right computeEligibleCandidate) bool {
	if left.locality != right.locality {
		return left.locality < right.locality
	}
	leftPreference := left.candidate.Node.Admin.HandlerPreference[job.Spec.Handler.HandlerID]
	rightPreference := right.candidate.Node.Admin.HandlerPreference[job.Spec.Handler.HandlerID]
	if leftPreference != rightPreference {
		return leftPreference > rightPreference
	}
	if comparison := computeCompareRatio(
		int64(left.candidate.Telemetry.ActiveJobs), int64(left.candidate.Policy.MaxConcurrency),
		int64(right.candidate.Telemetry.ActiveJobs), int64(right.candidate.Policy.MaxConcurrency),
	); comparison != 0 {
		return comparison < 0
	}
	leftRAM, leftRAMCapacity := computeRAMHeadroom(job.Spec, left.candidate)
	rightRAM, rightRAMCapacity := computeRAMHeadroom(job.Spec, right.candidate)
	if comparison := computeCompareRatio(leftRAM, leftRAMCapacity, rightRAM, rightRAMCapacity); comparison != 0 {
		return comparison > 0
	}
	if comparison := computeCompareAssignmentAge(left.candidate.LastAssignedAt, right.candidate.LastAssignedAt); comparison != 0 {
		return comparison < 0
	}
	return left.candidate.Node.NodeID < right.candidate.Node.NodeID
}

func computeRAMHeadroom(spec wire.ComputeJobSpec, candidate computeCandidateState) (int64, int64) {
	effectiveCapacity := min(candidate.Policy.MaxRAMBytes, candidate.Node.Observed.TotalRAMBytes)
	if effectiveCapacity <= 0 {
		return 0, 1
	}
	usableRAM := min(candidate.Telemetry.AvailableRAMBytes, effectiveCapacity)
	return usableRAM - spec.Requirements.MinimumRAMBytes, effectiveCapacity
}

// computeCompareRatio compares leftNumerator/leftDenominator with the right
// ratio exactly. Eligible scheduler inputs have positive denominators; the
// defensive denominator normalization keeps the helper total for unit tests.
func computeCompareRatio(leftNumerator, leftDenominator, rightNumerator, rightDenominator int64) int {
	if leftDenominator <= 0 {
		leftNumerator, leftDenominator = 0, 1
	}
	if rightDenominator <= 0 {
		rightNumerator, rightDenominator = 0, 1
	}
	var left, right big.Int
	left.Mul(big.NewInt(leftNumerator), big.NewInt(rightDenominator))
	right.Mul(big.NewInt(rightNumerator), big.NewInt(leftDenominator))
	return left.Cmp(&right)
}

// computeCompareAssignmentAge returns -1 when left is older (and therefore
// ranks first). A zero timestamp means never assigned and is oldest.
func computeCompareAssignmentAge(left, right time.Time) int {
	if left.IsZero() != right.IsZero() {
		if left.IsZero() {
			return -1
		}
		return 1
	}
	if left.Equal(right) {
		return 0
	}
	if left.Before(right) {
		return -1
	}
	return 1
}
