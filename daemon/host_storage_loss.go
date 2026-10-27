package main

import (
	"fmt"
	"sort"
	"sync"
	"time"
)

const hostLossThresholdMB int64 = 8192

type hostLossPoint struct {
	At             time.Time
	HostFreeMB     int64
	VHDXPhysicalMB int64
	VHDXKnown      bool
}

type hostLossAssessment struct {
	Detected                 bool
	WindowStart, WindowEnd   time.Time
	Samples                  int
	HostLossMB, VHDXGrowthMB int64
	UnexplainedMB            int64
	VHDXKnown                bool
}

type hostLossTracker struct {
	mu          sync.Mutex
	points      []hostLossPoint
	window      time.Duration
	thresholdMB int64
	maxPoints   int
}

func newWfHostLossTracker() *hostLossTracker {
	return &hostLossTracker{window: time.Hour, thresholdMB: hostLossThresholdMB, maxPoints: 512}
}

func (t *hostLossTracker) Observe(p hostLossPoint) {
	if t == nil || p.HostFreeMB <= 0 || p.At.IsZero() {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	for i := range t.points {
		if t.points[i].At.Equal(p.At) {
			t.points[i] = p
			t.pruneLocked(t.points[len(t.points)-1].At)
			return
		}
	}
	t.points = append(t.points, p)
	sort.Slice(t.points, func(i, j int) bool { return t.points[i].At.Before(t.points[j].At) })
	t.pruneLocked(t.points[len(t.points)-1].At)
}

func (t *hostLossTracker) pruneLocked(now time.Time) {
	window := t.window
	if window <= 0 {
		window = time.Hour
	}
	cutoff := now.Add(-2 * window)
	first := sort.Search(len(t.points), func(i int) bool { return !t.points[i].At.Before(cutoff) })
	if first > 0 {
		t.points = append([]hostLossPoint(nil), t.points[first:]...)
	}
	maxPoints := t.maxPoints
	if maxPoints <= 0 {
		maxPoints = 512
	}
	if len(t.points) > maxPoints {
		t.points = append([]hostLossPoint(nil), t.points[len(t.points)-maxPoints:]...)
	}
}

func (t *hostLossTracker) Assess(now time.Time) hostLossAssessment {
	if t == nil {
		return hostLossAssessment{}
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	window := t.window
	if window <= 0 {
		window = time.Hour
	}
	cutoff := now.Add(-window)
	var selected []hostLossPoint
	for _, p := range t.points {
		if !p.At.Before(cutoff) && !p.At.After(now) {
			selected = append(selected, p)
		}
	}
	if len(selected) == 0 {
		return hostLossAssessment{}
	}
	earliest, latest := selected[0], selected[len(selected)-1]
	a := hostLossAssessment{
		WindowStart: earliest.At, WindowEnd: latest.At, Samples: len(selected),
		HostLossMB: earliest.HostFreeMB - latest.HostFreeMB,
		VHDXKnown:  earliest.VHDXKnown && latest.VHDXKnown,
	}
	if a.VHDXKnown && latest.VHDXPhysicalMB > earliest.VHDXPhysicalMB {
		a.VHDXGrowthMB = latest.VHDXPhysicalMB - earliest.VHDXPhysicalMB
	}
	a.UnexplainedMB = a.HostLossMB - a.VHDXGrowthMB
	threshold := t.thresholdMB
	if threshold <= 0 {
		threshold = hostLossThresholdMB
	}
	a.Detected = a.Samples >= 2 && a.UnexplainedMB >= threshold
	return a
}

func (t *hostLossTracker) Seed(events []hostStorageEvent) {
	for _, ev := range events {
		if ev.Kind != "sample" || ev.HostFreeMB <= 0 {
			continue
		}
		t.Observe(hostLossPoint{At: ev.At, HostFreeMB: ev.HostFreeMB, VHDXPhysicalMB: ev.VHDXPhysicalMB, VHDXKnown: ev.VHDXPhysicalMB > 0})
	}
}

func hostLossText(a hostLossAssessment) string {
	minutes := int64(a.WindowEnd.Sub(a.WindowStart).Round(time.Minute) / time.Minute)
	if minutes < 0 {
		minutes = 0
	}
	return fmt.Sprintf("host free fell %.1f GB in %d min while the VHDX grew %.1f GB", float64(a.HostLossMB)/1000, minutes, float64(a.VHDXGrowthMB)/1000)
}
