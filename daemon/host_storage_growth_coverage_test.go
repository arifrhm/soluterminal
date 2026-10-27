package main

import (
	"encoding/json"
	"os"
	"testing"
	"time"
)

func hostGrowthHourlyFixture(start time.Time, hours int) []hostStorageEvent {
	var events []hostStorageEvent
	for i := 0; i <= hours; i++ {
		events = append(events, hostStorageEvent{At: start.Add(time.Duration(i) * time.Hour), Kind: "hourly_summary", HostFreeLastMB: 700_000 - int64(i)*100})
	}
	return events
}

func writeHostGrowthEvents(t *testing.T, path string, events []hostStorageEvent) {
	t.Helper()
	var data []byte
	for _, ev := range events {
		line, err := json.Marshal(ev)
		if err != nil {
			t.Fatal(err)
		}
		data = append(append(data, line...), '\n')
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestHostGrowthCoverageLatestContiguousSuffix(t *testing.T) {
	now := time.Date(2026, 8, 27, 0, 0, 0, 0, time.UTC)
	hourly := func(startHours, hours int) []hostStorageEvent {
		return hostGrowthHourlyFixture(now.Add(time.Duration(startHours)*time.Hour), hours)
	}
	sparse := []hostStorageEvent{
		{At: now.Add(-48 * time.Hour), Kind: "sample", HostFreeMB: 700_000},
		{At: now.Add(-48*time.Hour + 30*time.Minute), Kind: "sample", HostFreeMB: 700_000},
		{At: now, Kind: "sample", HostFreeMB: 700_000},
	}
	gapOverHour := hourly(-48, 48)
	gapOverHour[0].At = gapOverHour[0].At.Add(-time.Nanosecond)
	oldBurst := hourly(-100, 48)
	oldBurst[1].HostFreeLastMB -= 20_000
	for _, tc := range []struct {
		name      string
		events    []hostStorageEvent
		duplicate bool
		wantOK    bool
		wantHours float64
	}{
		{name: "three sparse samples spanning 48h", events: sparse},
		{name: "disjoint dense clusters", events: append(hourly(-72, 24), hourly(-24, 24)...), wantHours: 24},
		{name: "47h is insufficient", events: hourly(-47, 47), wantHours: 47},
		{name: "48h with exact 1h gaps", events: hourly(-48, 48), wantOK: true, wantHours: 48},
		{name: "gap exceeds 1h by 1ns", events: gapOverHour, wantHours: 47},
		{name: "old complete history cannot cover new sparse suffix", events: append(hourly(-100, 48), hourly(-1, 1)...), wantHours: 1},
		{name: "old disconnected burst excluded", events: append(oldBurst, hourly(-48, 48)...), wantOK: true, wantHours: 48},
		{name: "duplicates cannot extend 47h", events: hourly(-47, 47), duplicate: true, wantHours: 47},
		{name: "duplicates preserve valid 48h", events: hourly(-48, 48), duplicate: true, wantOK: true, wantHours: 48},
		{name: "future endpoint cannot complete coverage", events: hourly(-47, 48), wantHours: 47},
		{name: "1ns future endpoint excluded", events: hostGrowthHourlyFixture(now.Add(-48*time.Hour+time.Nanosecond), 48), wantHours: 47},
		{name: "future points do not discard valid past", events: hourly(-48, 60), wantOK: true, wantHours: 48},
		{name: "all future", events: hourly(1, 48)},
		{name: "zero timestamps", events: []hostStorageEvent{{Kind: "sample", HostFreeMB: 700_000}}},
		{name: "freshness exactly 1h", events: hourly(-49, 48), wantOK: true, wantHours: 48},
		{name: "freshness over 1h by 1ns", events: hostGrowthHourlyFixture(now.Add(-49*time.Hour-time.Nanosecond), 48), wantHours: 48},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ledger := newHostStorageLedger(t.TempDir())
			ledger.now = func() time.Time { return now }
			writeHostGrowthEvents(t, ledger.archivePath, tc.events)
			if tc.duplicate {
				for _, path := range []string{ledger.archivePath + ".1", ledger.path + ".2", ledger.path + ".1", ledger.path} {
					writeHostGrowthEvents(t, path, tc.events)
				}
			}
			stats, ok := ledger.GrowthStats(30 * time.Minute)
			if ok != tc.wantOK || stats.SampledHours != tc.wantHours {
				t.Fatalf("stats=%+v ok=%t, want ok=%t contiguous hours=%g", stats, ok, tc.wantOK, tc.wantHours)
			}
			if ok && (stats.WindowMBP50 != 100 || stats.WindowMBP99 != 100 || stats.WindowMBEWMA != 100 || stats.Bursty) {
				t.Fatalf("losses must use only the valid suffix: %+v", stats)
			}
		})
	}
}

func TestHostGrowthCoverageCacheExpiry(t *testing.T) {
	end := time.Date(2026, 8, 27, 0, 0, 0, 0, time.UTC)
	ledger := newHostStorageLedger(t.TempDir())
	writeHostGrowthEvents(t, ledger.archivePath, hostGrowthHourlyFixture(end.Add(-48*time.Hour), 48))
	now := end.Add(59 * time.Minute)
	ledger.now = func() time.Time { return now }
	if _, ok := ledger.GrowthStats(30 * time.Minute); !ok {
		t.Fatal("fresh 48h history was rejected")
	}
	now = end.Add(time.Hour)
	if _, ok := ledger.GrowthStats(30 * time.Minute); !ok {
		t.Fatal("exact freshness boundary was rejected")
	}
	now = now.Add(time.Nanosecond)
	if _, ok := ledger.GrowthStats(30 * time.Minute); ok {
		t.Fatal("cached success outlived newest sample freshness within the 5m cache TTL")
	}
}

func TestHostGrowthCoverageCacheClockRollback(t *testing.T) {
	end := time.Date(2026, 8, 27, 0, 0, 0, 0, time.UTC)
	ledger := newHostStorageLedger(t.TempDir())
	writeHostGrowthEvents(t, ledger.archivePath, hostGrowthHourlyFixture(end.Add(-48*time.Hour), 48))
	now := end
	ledger.now = func() time.Time { return now }
	if _, ok := ledger.GrowthStats(30 * time.Minute); !ok {
		t.Fatal("fresh 48h history was rejected")
	}
	now = end.Add(-time.Nanosecond)
	if stats, ok := ledger.GrowthStats(30 * time.Minute); ok || stats.SampledHours != 47 {
		t.Fatalf("clock rollback must discard cached success and filter the now-future endpoint: %+v ok=%t", stats, ok)
	}
}
