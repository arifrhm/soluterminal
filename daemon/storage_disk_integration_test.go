package main

import (
	"testing"
	"time"
)

// Both histories must survive: capacity continuity feeds observability while
// hourly buckets feed the opt-in adaptive floor. Cleanup is not a capacity
// sample and must not contaminate either history.
func TestHostStorageIntegratedHourlyHeartbeatAndOperations(t *testing.T) {
	l := newHostStorageLedger(t.TempDir())
	start := time.Date(2026, 9, 24, 0, 5, 0, 0, time.UTC)
	now := start
	l.now = func() time.Time { return now }
	p := diskPressureState{HostKnown: true, HostState: hostStorageOK, HostFreeMB: 100000, VHDXPhysicalMB: 550000}
	l.AppendSample("boot", p)
	l.AppendProducerSample("boot", storageProducerSample{At: now, Complete: true, Categories: map[string]int64{storageCatWorkflowWorktrees: 1 << 30}})
	l.AppendCleanupOperation(storageCleanupOperation{At: now, Kind: "worktree_gc", Layer: "guest", DryRun: true, Complete: true})
	for i := 1; i <= 6; i++ {
		now = start.Add(time.Duration(i) * time.Hour)
		l.AppendSample("boot", p)
	}
	var samples int
	for _, ev := range l.Tail(100) {
		if ev.Kind == "sample" {
			samples++
		}
		if ev.Kind == "hourly_summary" {
			t.Fatal("hourly archive leaked into live history")
		}
	}
	if samples != 2 {
		t.Fatalf("capacity continuity heartbeat lost: samples=%d", samples)
	}
	archive := readHostStorageEvents(l.archivePath, 1<<20, "")
	if len(archive) != 6 {
		t.Fatalf("deduped capacity samples lost hourly history: %+v", archive)
	}
	for _, ev := range archive {
		if ev.Kind != "hourly_summary" || ev.HostFreeLastMB != p.HostFreeMB || ev.Producer != nil || ev.CleanupOperation != nil {
			t.Fatalf("archive contains mixed event data: %+v", ev)
		}
	}
	if got := l.TailProducerSamples(100); len(got) != 1 {
		t.Fatalf("producer history lost or contaminated: %+v", got)
	}
	if got := l.TailOperations(100); len(got) != 1 || !got[0].DryRun || got[0].Executed {
		t.Fatalf("cleanup history lost or preview relabeled executed: %+v", got)
	}
}
