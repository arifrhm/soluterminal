package main

import (
	"sort"
	"time"
)

const hostGrowthMinimumHistory = 48 * time.Hour
const hostGrowthMaxSampleGap = time.Hour
const hostGrowthBurstMB int64 = 16 * 1024

// hostGrowthStats is the measured host-free loss over the same bounded
// window used by admission. Values are MB, not bytes, to match every host
// storage policy and API surface.
type hostGrowthStats struct {
	WindowMBP50  int64   `json:"windowMBP50"`
	WindowMBP99  int64   `json:"windowMBP99"`
	WindowMBEWMA int64   `json:"windowMBEWMA"`
	SampledHours float64 `json:"sampledHours"`
	Bursty       bool    `json:"bursty"`

	// Keep cached successes bounded by the newest observation's freshness.
	sampledThrough time.Time
}

func (s hostGrowthStats) freshAt(now time.Time) bool {
	return !s.sampledThrough.IsZero() && !s.sampledThrough.After(now) && now.Sub(s.sampledThrough) <= hostGrowthMaxSampleGap
}

type hostGrowthPoint struct {
	at   time.Time
	free int64
}

func (l *hostStorageLedger) GrowthStats(window time.Duration) (hostGrowthStats, bool) {
	if l == nil {
		return hostGrowthStats{}, false
	}
	if window <= 0 {
		window = 30 * time.Minute
	}
	l.mu.Lock()
	now := l.now()
	cacheAge := now.Sub(l.growthCachedAt)
	if !l.growthCachedAt.IsZero() && cacheAge >= 0 && cacheAge < 5*time.Minute && (!l.growthCachedOK || l.growthCached.freshAt(now)) {
		stats, ok := l.growthCached, l.growthCachedOK
		l.mu.Unlock()
		return stats, ok
	}
	path, archive := l.path, l.archivePath
	l.mu.Unlock()
	stats, ok := estimateHostGrowth(path, archive, window, now)
	l.mu.Lock()
	l.growthCachedAt, l.growthCached, l.growthCachedOK = now, stats, ok
	l.mu.Unlock()
	return stats, ok
}

func estimateHostGrowth(livePath, archivePath string, window time.Duration, now time.Time) (hostGrowthStats, bool) {
	var points []hostGrowthPoint
	for _, path := range []string{archivePath + ".1", archivePath, livePath + ".2", livePath + ".1", livePath} {
		for _, ev := range readHostStorageEvents(path, 8<<20, "") {
			if ev.At.IsZero() || ev.At.After(now) {
				continue
			}
			switch {
			case ev.Kind == "sample" && ev.HostFreeMB > 0:
				points = append(points, hostGrowthPoint{at: ev.At, free: ev.HostFreeMB})
			case ev.Kind == "hourly_summary" && ev.HostFreeLastMB > 0:
				points = append(points, hostGrowthPoint{at: ev.At, free: ev.HostFreeLastMB})
			}
		}
	}
	if len(points) < 2 {
		return hostGrowthStats{}, false
	}
	sort.SliceStable(points, func(i, j int) bool { return points[i].at.Before(points[j].at) })
	// Collapse duplicate timestamps introduced when the live and archive
	// horizons overlap. Prefer the last decoded point.
	dedup := points[:0]
	for _, point := range points {
		if len(dedup) > 0 && point.at.Equal(dedup[len(dedup)-1].at) {
			dedup[len(dedup)-1] = point
			continue
		}
		dedup = append(dedup, point)
	}
	points = dedup
	// Hourly archives are the coarsest supported history. A larger gap is
	// unobserved time, not evidence of zero growth: use only the latest
	// contiguous suffix, both for coverage and for every loss estimate.
	for i := len(points) - 1; i > 0; i-- {
		if points[i].at.Sub(points[i-1].at) > hostGrowthMaxSampleGap {
			points = points[i:]
			break
		}
	}
	span := points[len(points)-1].at.Sub(points[0].at)
	stats := hostGrowthStats{SampledHours: span.Hours(), sampledThrough: points[len(points)-1].at}
	if span < hostGrowthMinimumHistory || !stats.freshAt(now) {
		return stats, false
	}
	var losses []int64
	for i, j := 0, 1; i < len(points)-1; i++ {
		if j <= i {
			j = i + 1
		}
		target := points[i].at.Add(window)
		for j < len(points) && points[j].at.Before(target) {
			j++
		}
		if j >= len(points) || points[j].at.Sub(target) > time.Hour {
			continue
		}
		loss := points[i].free - points[j].free
		if loss < 0 {
			loss = 0
		}
		losses = append(losses, loss)
	}
	if len(losses) == 0 {
		return stats, false
	}
	sorted := append([]int64(nil), losses...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	percentile := func(p int) int64 {
		idx := (len(sorted)*p+99)/100 - 1
		return sorted[max(0, min(len(sorted)-1, idx))]
	}
	stats.WindowMBP50, stats.WindowMBP99 = percentile(50), percentile(99)
	ewma := float64(losses[0])
	for _, loss := range losses[1:] {
		ewma = .25*float64(loss) + .75*ewma
	}
	stats.WindowMBEWMA = int64(ewma + .5)
	for _, loss := range losses {
		if loss >= hostGrowthBurstMB {
			stats.Bursty = true
			break
		}
	}
	return stats, true
}
