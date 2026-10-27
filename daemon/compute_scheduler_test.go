package main

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"soluterminal-shared/wire"
)

func TestComputeScheduleEmptyAndRejectedSets(t *testing.T) {
	job, _ := eligibleComputeFixture()
	t.Run("empty", func(t *testing.T) {
		decision := computeSchedule(job, nil)
		if decision.SelectedNodeID != "" || decision.QueueReason != "no_eligible_node" || len(decision.Rejections) != 0 {
			t.Fatalf("decision = %+v", decision)
		}
	})

	t.Run("all rejected has one stable rejection per node", func(t *testing.T) {
		_, nodeZ := eligibleComputeFixture()
		nodeZ.Node.NodeID = "node-z"
		nodeZ.LocalEnabled = false
		_, nodeA := eligibleComputeFixture()
		nodeA.Node.NodeID = "node-a"
		nodeA.SessionAuthenticated = false

		decision := computeSchedule(job, []computeCandidateState{nodeZ, nodeA})
		want := []computeCandidateRejection{
			{NodeID: "node-a", Filter: computeFilterIdentity, Reason: "node_unauthenticated"},
			{NodeID: "node-z", Filter: computeFilterEnablement, Reason: "local_disabled"},
		}
		if decision.SelectedNodeID != "" || decision.QueueReason != "no_eligible_node" || !reflect.DeepEqual(decision.Rejections, want) {
			t.Fatalf("decision = %+v, want rejections %+v", decision, want)
		}
	})
}

func TestComputeScheduleFiltersBeforeScoring(t *testing.T) {
	job, ineligibleOwner := eligibleComputeFixture()
	ineligibleOwner.Node.NodeID = "node-owner"
	ineligibleOwner.Node.Admin.HandlerPreference[computeHandlerSharedTests] = 1000
	ineligibleOwner.LocalEnabled = false
	job.Spec.Requirements.DataOwnerNodeID = ineligibleOwner.Node.NodeID

	_, eligibleCached := eligibleComputeFixture()
	eligibleCached.Node.NodeID = "node-cached"
	eligibleCached.Node.Admin.HandlerPreference[computeHandlerSharedTests] = -1000

	decision := computeSchedule(job, []computeCandidateState{ineligibleOwner, eligibleCached})
	if decision.SelectedNodeID != "node-cached" || decision.QueueReason != "" {
		t.Fatalf("decision = %+v, want only eligible node", decision)
	}
	wantRejections := []computeCandidateRejection{{
		NodeID: "node-owner", Filter: computeFilterEnablement, Reason: "local_disabled",
	}}
	if !reflect.DeepEqual(decision.Rejections, wantRejections) {
		t.Fatalf("rejections = %+v, want %+v", decision.Rejections, wantRejections)
	}
}

func TestComputeScheduleLexicographicScoreDimensions(t *testing.T) {
	tests := []struct {
		name      string
		configure func(*wireFixturePair)
		want      string
	}{
		{
			name: "owner locality before cached",
			configure: func(pair *wireFixturePair) {
				pair.left.Node.NodeID = "node-owner"
				pair.right.Node.NodeID = "node-cached"
				pair.job.Spec.Requirements.DataOwnerNodeID = "node-owner"
				pair.left.Node.Admin.HandlerPreference[computeHandlerSharedTests] = -100
				pair.right.Node.Admin.HandlerPreference[computeHandlerSharedTests] = 100
			},
			want: "node-owner",
		},
		{
			name: "cached locality before permitted transfer",
			configure: func(pair *wireFixturePair) {
				pair.left.Node.NodeID = "node-transfer"
				pair.left.Node.Observed.WorkspaceDigests = nil
				pair.left.Node.Observed.CacheDigests = nil
				pair.right.Node.NodeID = "node-cached"
				pair.left.Node.Admin.HandlerPreference[computeHandlerSharedTests] = 100
				pair.right.Node.Admin.HandlerPreference[computeHandlerSharedTests] = -100
			},
			want: "node-cached",
		},
		{
			name: "higher handler preference",
			configure: func(pair *wireFixturePair) {
				pair.left.Node.NodeID = "node-low"
				pair.right.Node.NodeID = "node-high"
				pair.left.Node.Admin.HandlerPreference[computeHandlerSharedTests] = 1
				pair.right.Node.Admin.HandlerPreference[computeHandlerSharedTests] = 2
			},
			want: "node-high",
		},
		{
			name: "lower exact rational load one third before two fifths",
			configure: func(pair *wireFixturePair) {
				pair.left.Node.NodeID = "node-one-third"
				pair.left.Telemetry.ActiveJobs = 1
				pair.left.Policy.MaxConcurrency = 3
				pair.right.Node.NodeID = "node-two-fifths"
				pair.right.Telemetry.ActiveJobs = 2
				pair.right.Policy.MaxConcurrency = 5
			},
			want: "node-one-third",
		},
		{
			name: "greater exact post-reservation RAM ratio",
			configure: func(pair *wireFixturePair) {
				pair.job.Spec.Requirements.MinimumRAMBytes = 2
				pair.left.Node.NodeID = "node-six-tenths"
				pair.left.Policy.MaxRAMBytes = 10
				pair.left.Node.Observed.TotalRAMBytes = 10
				pair.left.Telemetry.AvailableRAMBytes = 8
				pair.right.Node.NodeID = "node-eleven-twentieths"
				pair.right.Policy.MaxRAMBytes = 20
				pair.right.Node.Observed.TotalRAMBytes = 20
				pair.right.Telemetry.AvailableRAMBytes = 13
			},
			want: "node-six-tenths",
		},
		{
			name: "never assigned is oldest",
			configure: func(pair *wireFixturePair) {
				pair.left.Node.NodeID = "node-never"
				pair.left.LastAssignedAt = time.Time{}
				pair.right.Node.NodeID = "node-assigned"
				pair.right.LastAssignedAt = time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
			},
			want: "node-never",
		},
		{
			name: "older assignment timestamp",
			configure: func(pair *wireFixturePair) {
				pair.left.Node.NodeID = "node-old"
				pair.left.LastAssignedAt = time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
				pair.right.Node.NodeID = "node-new"
				pair.right.LastAssignedAt = time.Date(2026, 8, 2, 0, 0, 0, 0, time.UTC)
			},
			want: "node-old",
		},
		{
			name: "lexical node ID final tie break with absent preference equal zero",
			configure: func(pair *wireFixturePair) {
				pair.left.Node.NodeID = "node-b"
				pair.left.Node.Admin.HandlerPreference = nil
				pair.right.Node.NodeID = "node-a"
				pair.right.Node.Admin.HandlerPreference = map[string]int{computeHandlerSharedTests: 0}
			},
			want: "node-a",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			pair := newWireFixturePair()
			test.configure(&pair)
			decision := computeSchedule(pair.job, []computeCandidateState{pair.left, pair.right})
			if decision.SelectedNodeID != test.want || decision.QueueReason != "" || len(decision.Rejections) != 0 {
				t.Fatalf("decision = %+v, want selected %q", decision, test.want)
			}
		})
	}
}

func TestComputeScheduleDeterministicAcrossCallsAndPermutations(t *testing.T) {
	job, nodeA := eligibleComputeFixture()
	nodeA.Node.NodeID = "node-a"
	nodeA.Node.Admin.HandlerPreference[computeHandlerSharedTests] = 1
	_, nodeB := eligibleComputeFixture()
	nodeB.Node.NodeID = "node-b"
	nodeB.Node.Admin.HandlerPreference[computeHandlerSharedTests] = 5
	_, nodeC := eligibleComputeFixture()
	nodeC.Node.NodeID = "node-c"
	nodeC.Node.Admin.HandlerPreference[computeHandlerSharedTests] = 3
	_, nodeD := eligibleComputeFixture()
	nodeD.Node.NodeID = "node-d"
	nodeD.SessionLeaseValid = false

	permutations := [][]computeCandidateState{
		{nodeA, nodeB, nodeC, nodeD},
		{nodeD, nodeC, nodeB, nodeA},
		{nodeB, nodeD, nodeA, nodeC},
		{nodeC, nodeA, nodeD, nodeB},
		{nodeA, nodeD, nodeC, nodeB},
		{nodeB, nodeC, nodeA, nodeD},
	}
	want := computeSchedule(job, permutations[0])
	if want.SelectedNodeID != "node-b" {
		t.Fatalf("baseline = %+v", want)
	}
	for repetition := 0; repetition < 20; repetition++ {
		for index, permutation := range permutations {
			got := computeSchedule(job, permutation)
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("repetition %d permutation %d = %+v, want %+v", repetition, index, got, want)
			}
		}
	}
}

func TestComputeScheduleDoesNotMutateInputs(t *testing.T) {
	job, left := eligibleComputeFixture()
	_, right := eligibleComputeFixture()
	left.Node.NodeID = "node-z"
	right.Node.NodeID = "node-a"
	candidates := []computeCandidateState{left, right}
	before, err := json.Marshal(struct {
		Job        any
		Candidates any
	}{job, candidates})
	if err != nil {
		t.Fatal(err)
	}
	_ = computeSchedule(job, candidates)
	after, err := json.Marshal(struct {
		Job        any
		Candidates any
	}{job, candidates})
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatalf("inputs mutated\nbefore: %s\nafter:  %s", before, after)
	}
}

type wireFixturePair struct {
	job         wire.ComputeJobRecord
	left, right computeCandidateState
}

func newWireFixturePair() wireFixturePair {
	job, left := eligibleComputeFixture()
	_, right := eligibleComputeFixture()
	left.Node.NodeID = "node-left"
	right.Node.NodeID = "node-right"
	return wireFixturePair{job: job, left: left, right: right}
}
