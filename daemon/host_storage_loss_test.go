package main

import (
	"encoding/json"
	"testing"
	"time"
)

func lossPoint(at string, host, vhdx int64) hostLossPoint {
	t, _ := time.Parse(time.RFC3339, at)
	return hostLossPoint{At: t, HostFreeMB: host, VHDXPhysicalMB: vhdx, VHDXKnown: vhdx > 0}
}

func TestWfHostLossRecordedIncidentSequence(t *testing.T) {
	var events []hostStorageEvent
	if err := json.Unmarshal([]byte(`[
 {"at":"2026-08-26T20:02:00Z","kind":"sample","hostFreeMB":116960,"vhdxPhysicalMB":543380},
 {"at":"2026-08-26T20:23:00Z","kind":"sample","hostFreeMB":109469,"vhdxPhysicalMB":543380},
 {"at":"2026-08-26T20:29:00Z","kind":"sample","hostFreeMB":92045,"vhdxPhysicalMB":543380},
 {"at":"2026-08-26T20:34:00Z","kind":"sample","hostFreeMB":82228,"vhdxPhysicalMB":543380},
 {"at":"2026-08-26T20:45:00Z","kind":"sample","hostFreeMB":65865,"vhdxPhysicalMB":543380}
]`), &events); err != nil {
		t.Fatal(err)
	}
	tracker := newWfHostLossTracker()
	tracker.Seed(events)
	a := tracker.Assess(events[len(events)-1].At)
	if !a.Detected || a.HostLossMB != 51095 || a.VHDXGrowthMB != 0 || a.UnexplainedMB != 51095 || !a.VHDXKnown {
		t.Fatalf("assessment=%+v", a)
	}
	if got, want := hostLossText(a), "host free fell 51.1 GB in 43 min while the VHDX grew 0.0 GB"; got != want {
		t.Fatalf("loss text=%q want %q", got, want)
	}
}

func TestWfHostLossExplainedMixedAndUnknown(t *testing.T) {
	start := time.Date(2026, 8, 26, 20, 0, 0, 0, time.UTC)
	tests := []struct {
		name                        string
		hostEnd, vhdxStart, vhdxEnd int64
		wantDetected                bool
		wantUnexplained             int64
		wantKnown                   bool
	}{
		{"explained", 100000 - 16384, 500000, 500000 + 16384, false, 0, true},
		{"mixed", 100000 - 20480, 500000, 500000 + 8192, true, 12288, true},
		{"unknown", 100000 - 12288, 0, 0, true, 12288, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tracker := newWfHostLossTracker()
			tracker.Observe(hostLossPoint{At: start, HostFreeMB: 100000, VHDXPhysicalMB: tc.vhdxStart, VHDXKnown: tc.vhdxStart > 0})
			tracker.Observe(hostLossPoint{At: start.Add(40 * time.Minute), HostFreeMB: tc.hostEnd, VHDXPhysicalMB: tc.vhdxEnd, VHDXKnown: tc.vhdxEnd > 0})
			a := tracker.Assess(start.Add(40 * time.Minute))
			if a.Detected != tc.wantDetected || a.UnexplainedMB != tc.wantUnexplained || a.VHDXKnown != tc.wantKnown {
				t.Fatalf("assessment=%+v", a)
			}
		})
	}
}

func TestWfHostLossWindowDedupeAndPruning(t *testing.T) {
	tracker := &hostLossTracker{window: time.Hour, thresholdMB: hostLossThresholdMB, maxPoints: 3}
	start := time.Now().UTC().Truncate(time.Second)
	tracker.Observe(hostLossPoint{At: start.Add(-2 * time.Hour), HostFreeMB: 200000})
	if a := tracker.Assess(start); a.Detected || a.Samples != 0 {
		t.Fatalf("old point assessment=%+v", a)
	}
	for i := 0; i < 5; i++ {
		tracker.Observe(hostLossPoint{At: start.Add(time.Duration(i) * time.Minute), HostFreeMB: 100000 - int64(i)})
	}
	tracker.Observe(hostLossPoint{At: start.Add(4 * time.Minute), HostFreeMB: 90000})
	if len(tracker.points) != 3 {
		t.Fatalf("points=%d want max 3", len(tracker.points))
	}
	if a := tracker.Assess(start.Add(4 * time.Minute)); a.Samples != 3 {
		t.Fatalf("deduped samples=%d want 3", a.Samples)
	}

	// An out-of-order point is sorted into place and must not move the pruning
	// horizon backwards from the newest observation.
	tracker.Observe(hostLossPoint{At: start.Add(-3 * time.Hour), HostFreeMB: 120000})
	if len(tracker.points) != 3 || tracker.points[0].At.Before(start) {
		t.Fatalf("out-of-order pruning left stale points: %+v", tracker.points)
	}
}

func capEvent(at string, boot string, hostMB, vhdxMB int64) hostStorageEvent {
	t, _ := time.Parse(time.RFC3339, at)
	return hostStorageEvent{At: t, BootID: boot, Kind: "sample", HostFreeMB: hostMB, VHDXPhysicalMB: vhdxMB}
}

func TestStorageCapacityGrowthUsesDailyWindowsAndIgnoresOtherKinds(t *testing.T) {
	now, _ := time.Parse(time.RFC3339, "2026-09-24T12:00:00Z")
	events := []hostStorageEvent{
		capEvent("2026-09-23T12:00:00Z", "boot-a", 120000, 900000),
		capEvent("2026-09-23T22:00:00Z", "boot-a", 110000, 905000),
		capEvent("2026-09-24T06:00:00Z", "boot-a", 90000, 920000),
		capEvent("2026-09-24T11:00:00Z", "boot-a", 80000, 949000),
		// Operations and producer rows must not enter capacity math.
		{At: now, Kind: "cleanup_operation", CleanupOperation: &storageCleanupOperation{At: now, Kind: "worktree_gc", Executed: true}},
		{At: now, Kind: "producer_sample", Producer: &storageProducerSample{At: now, Complete: true, Categories: map[string]int64{storageCatWorkflowWorktrees: 5 << 30}}},
	}
	samples := StorageCapacitySamples(events)
	if len(samples) != 4 {
		t.Fatalf("capacity samples = %+v", samples)
	}
	day := StorageGrowthWindow(samples, now, 24*time.Hour)
	if !day.Available || day.Continuity != storageContinuityOK {
		t.Fatalf("day growth = %+v", day)
	}
	vhdx := growthDeltaForTest(day, storageCatCapacityVHDX)
	if vhdx == nil || vhdx.DeltaBytes != 49000<<20 {
		t.Fatalf("vhdx delta = %+v", vhdx)
	}
	hostFree := growthDeltaForTest(day, storageCatCapacityHostFree)
	if hostFree == nil || hostFree.DeltaBytes != -40000<<20 {
		t.Fatalf("host free delta = %+v", hostFree)
	}

	// A reboot or a time gap returns unavailable continuity, never a zero delta.
	reboot := StorageGrowthWindow(StorageCapacitySamples([]hostStorageEvent{
		capEvent("2026-09-23T12:00:00Z", "boot-a", 120000, 900000),
		capEvent("2026-09-24T11:00:00Z", "boot-b", 80000, 949000),
	}), now, 24*time.Hour)
	if reboot.Available || reboot.Continuity != storageContinuityReboot {
		t.Fatalf("reboot growth = %+v", reboot)
	}
	gap := StorageGrowthWindow(StorageCapacitySamples([]hostStorageEvent{
		capEvent("2026-09-23T12:00:00Z", "boot-a", 120000, 900000),
		capEvent("2026-09-24T11:00:00Z", "boot-a", 80000, 949000),
	}), now, 24*time.Hour)
	if gap.Available || (gap.Continuity != storageContinuityGap && gap.Continuity != storageContinuityNoBaseline) {
		t.Fatalf("gap growth = %+v", gap)
	}
}

func growthDeltaForTest(window storageGrowthWindow, category string) *storageCategoryDelta {
	for i := range window.Deltas {
		if window.Deltas[i].Category == category {
			return &window.Deltas[i]
		}
	}
	return nil
}
