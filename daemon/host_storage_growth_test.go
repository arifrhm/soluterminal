package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeHostGrowthFixture(t *testing.T, path string, hours int, variable bool) time.Time {
	t.Helper()
	start := time.Date(2026, 8, 24, 0, 0, 0, 0, time.UTC)
	free := int64(700_000)
	data := make([]byte, 0, hours*6*128)
	for i := 0; i <= hours*6; i++ {
		if i > 0 {
			// 7,302 MB/h, matching the 2026-08-27 observed host-free loss.
			free -= 1_217
			// A bounded, non-bursty fluctuation gives p99 enough variance to
			// distinguish sparse from non-sparse safety bases.
			if variable && i%60 == 30 {
				free -= 8_000
			}
			if variable && i%60 == 31 {
				free += 8_000
			}
		}
		ev := hostStorageEvent{At: start.Add(time.Duration(i) * 10 * time.Minute), Kind: "sample", HostFreeMB: free, VHDXPhysicalMB: 400_000 + int64(i)*40}
		line, err := json.Marshal(ev)
		if err != nil {
			t.Fatal(err)
		}
		data = append(data, line...)
		data = append(data, '\n')
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return start.Add(time.Duration(hours) * time.Hour)
}

func TestEstimateHostGrowthMeasuredProfileAndMinimumHistory(t *testing.T) {
	dir := t.TempDir()
	live := filepath.Join(dir, "host_storage_events.jsonl")
	now := writeHostGrowthFixture(t, live, 50, false)
	stats, ok := estimateHostGrowth(live, filepath.Join(dir, "missing-archive"), 30*time.Minute, now)
	if !ok || stats.SampledHours < 50 || stats.Bursty {
		t.Fatalf("stats=%+v ok=%t", stats, ok)
	}
	if stats.WindowMBP50 < 3_600 || stats.WindowMBP50 > 3_700 || stats.WindowMBP99 < 3_600 || stats.WindowMBP99 > 3_700 {
		t.Fatalf("7.3 GB/h profile produced %+v", stats)
	}

	now = writeHostGrowthFixture(t, live, 47, false)
	if short, ok := estimateHostGrowth(live, filepath.Join(dir, "missing-archive"), 30*time.Minute, now); ok || short.SampledHours >= 48 {
		t.Fatalf("short history was accepted: %+v ok=%t", short, ok)
	}
}

func TestEstimateHostGrowthMarksLargeWindowBurst(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "host_storage_events.jsonl")
	now := writeHostGrowthFixture(t, path, 50, false)
	events := readHostStorageEvents(path, 8<<20, "")
	events[len(events)/2].HostFreeMB -= 20_000
	var data []byte
	for _, ev := range events {
		line, _ := json.Marshal(ev)
		data = append(data, line...)
		data = append(data, '\n')
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	stats, ok := estimateHostGrowth(path, filepath.Join(dir, "archive"), 30*time.Minute, now)
	if !ok || !stats.Bursty {
		t.Fatalf("burst not detected: %+v ok=%t", stats, ok)
	}
}

func TestEstimateHostGrowthFromHourlyArchiveOnly(t *testing.T) {
	dir := t.TempDir()
	archive := filepath.Join(dir, "host_storage_hourly.jsonl")
	start := time.Date(2026, 8, 24, 0, 0, 0, 0, time.UTC)
	var data []byte
	for i := 0; i <= 50; i++ {
		ev := hostStorageEvent{At: start.Add(time.Duration(i) * time.Hour), Kind: "hourly_summary", HostFreeLastMB: 700000 - int64(i)*4000}
		line, err := json.Marshal(ev)
		if err != nil {
			t.Fatal(err)
		}
		data = append(append(data, line...), '\n')
	}
	if err := os.WriteFile(archive, data, 0600); err != nil {
		t.Fatal(err)
	}
	stats, ok := estimateHostGrowth(filepath.Join(dir, "missing-live"), archive, 30*time.Minute, start.Add(50*time.Hour))
	if !ok || stats.WindowMBP99 != 4000 || stats.SampledHours != 50 {
		t.Fatalf("archive stats=%+v ok=%t", stats, ok)
	}
}

func TestMeasuredHostDiskFloorTypicalProfileUsesSharedLowerClamp(t *testing.T) {
	// The machine's cited 7.3 GB/hour host-free loss is about 3,648 MB per
	// 30-minute admission window. Both safety bases remain below the explicit
	// 32,768 MB lower clamp at that profile, so sparse state intentionally does
	// not change the standing floor until observed p99 loss is materially larger.
	stats := hostGrowthStats{WindowMBP99: 3_648}
	if nonSparse, sparse := measuredHostDiskFloorMB(stats, false), measuredHostDiskFloorMB(stats, true); nonSparse != 32_768 || sparse != 32_768 {
		t.Fatalf("typical-profile floors non-sparse=%d sparse=%d, want shared 32768 MB clamp", nonSparse, sparse)
	}
}
