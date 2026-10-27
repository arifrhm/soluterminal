package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

type diskPressureState struct {
	GuestKnown, GuestLow                                  bool
	GuestFreeMB, GuestFloorMB                             int64
	GuestFreeBytes, GuestTotalBytes, GuestUsedBytes       int64
	HostKnown, HostLow, HostGateDisabled                  bool
	HostState, HostDetail, HostVolume, HostDistro         string
	HostFreeMB, HostTotalMB, HostFloorMB, HostBaseFloorMB int64
	HostFreeBytes, HostTotalBytes                         int64
	HostHysteresisMB                                      int64
	HostResumeFloorMB                                     int64
	HostFloorPct                                          int
	HostSampledAt                                         time.Time
	VHDXPhysicalMB, VHDXLogicalMB                         int64
	VHDXPhysicalBytes, VHDXLogicalBytes                   int64
	VHDXSparse                                            *bool
	VHDXPath, LocalAppData                                string
	Class, BlockingLayer                                  string
	HostLoss                                              hostLossAssessment
	HostOffender                                          *hostDirUsage
	HostOffenderScannedAt                                 time.Time
	HostFloorSource                                       string
	HostGrowth                                            hostGrowthStats
	HostGrowthKnown                                       bool
	HostBrowserReserveMB                                  int64
}

func (e *hostStorageService) diskPressure(ctx context.Context, class string) diskPressureState {
	p := e.guestDiskPressure(class)
	if e == nil || e.hostStorage == nil {
		p.HostState = hostStorageNotWSL
		return p
	}
	return e.diskPressureWithSample(p, e.hostStorage.Sample(ctx), class)
}

func (e *hostStorageService) hostDiskLatestPressure(class string) diskPressureState {
	p := e.guestDiskPressure(class)
	if e == nil || e.hostStorage == nil {
		p.HostState = hostStorageNotWSL
		return p
	}
	s, ok := e.hostStorage.Latest()
	if !ok {
		p.HostState = hostStorageUnsampled
		p.HostDetail = "host probe has not sampled yet"
		return p
	}
	return e.diskPressureWithSample(p, s, class)
}

func (e *hostStorageService) guestDiskPressure(class string) diskPressureState {
	p := diskPressureState{Class: class, BlockingLayer: "none"}
	if e == nil {
		return p
	}
	p.GuestFloorMB = int64(e.resolveDiskFloorMB().Value)
	if e.diskProbe != nil {
		if free, ok := e.diskProbe(e.diskFloorPath()); ok {
			p.GuestKnown = true
			p.GuestFreeBytes = free
			p.GuestFreeMB = free / (1024 * 1024)
			p.GuestLow = p.GuestFloorMB > 0 && p.GuestFreeMB < p.GuestFloorMB
		}
	}
	if p.GuestLow {
		p.BlockingLayer = "guest"
	}
	return p
}

func (e *hostStorageService) diskPressureWithSample(p diskPressureState, s hostStorageSample, class string) diskPressureState {
	p.HostState, p.HostDetail, p.HostVolume, p.HostDistro = s.State, s.Detail, s.Volume, s.Distro
	p.HostSampledAt, p.VHDXSparse, p.VHDXPath, p.LocalAppData = s.At, s.VHDXSparse, s.VHDXPath, s.LocalAppData
	p.VHDXPhysicalMB, p.VHDXLogicalMB = s.VHDXPhysicalBytes/(1024*1024), s.VHDXLogicalBytes/(1024*1024)
	p.VHDXPhysicalBytes, p.VHDXLogicalBytes = s.VHDXPhysicalBytes, s.VHDXLogicalBytes
	if s.Available && s.TotalBytes > 0 {
		p.HostKnown = true
		p.HostFreeMB, p.HostTotalMB = s.FreeBytes/(1024*1024), s.TotalBytes/(1024*1024)
		p.HostFreeBytes, p.HostTotalBytes = s.FreeBytes, s.TotalBytes
		absCap := e.resolveHostDiskFloorMB()
		pctCap := e.resolveHostDiskFloorPct()
		p.HostFloorPct = pctCap.Value
		p.HostBaseFloorMB = hostDiskEffectiveFloorMB(p.HostTotalMB, absCap.Value, pctCap.Value)
		p.HostFloorSource = absCap.Source
		if p.HostTotalMB*int64(pctCap.Value)/100 > int64(absCap.Value) {
			p.HostFloorSource = pctCap.Source
		}
		hostGateDisabled := hostDiskGateDisabled(absCap, pctCap)
		p.HostGateDisabled = hostGateDisabled
		// Operator-set Settings/env floors must never be lowered by adaptive policy.
		if e.resolveAdaptiveHostDiskFloor().Value && !hostGateDisabled && absCap.Source == capSourceAdaptive && pctCap.Source == capSourceAdaptive && e.hostLedger != nil {
			window := e.hostDiskDeferMax
			if window <= 0 {
				window = 30 * time.Minute
			}
			if stats, ok := e.hostLedger.GrowthStats(window); ok {
				p.HostGrowth, p.HostGrowthKnown = stats, true
				p.HostBaseFloorMB = min(p.HostBaseFloorMB, measuredHostDiskFloorMB(stats, s.VHDXSparse != nil && *s.VHDXSparse))
				p.HostFloorSource = capSourceController
			}
		}
		p.HostFloorMB = p.HostBaseFloorMB
		// Explicit 0+0 floor overrides are the documented full rollback of host
		// gating; the browser reserve is an addend to the base floor, not an
		// independent floor, so it must not survive rollback.
		if class == "browser" && !hostGateDisabled {
			p.HostBrowserReserveMB = int64(e.resolveBrowserHostReserveMB().Value)
			p.HostFloorMB += p.HostBrowserReserveMB
		}
		p.HostHysteresisMB = int64(e.resolveHostDiskHysteresisMB().Value)
		p.HostLow = p.HostFloorMB > 0 && p.HostFreeMB < p.HostFloorMB
		// The browser reserve is admission-only. Shared hysteresis is anchored
		// to the class-independent floor so a deferred browser stage cannot
		// poison later generic admission.
		if p.HostBaseFloorMB > 0 && e.hostLowActive() && p.HostFreeMB < p.HostBaseFloorMB+p.HostHysteresisMB {
			p.HostLow = true
			p.HostResumeFloorMB = p.HostBaseFloorMB + p.HostHysteresisMB
		}
		if e.hostLoss != nil {
			e.hostLoss.Observe(hostLossPoint{At: s.At, HostFreeMB: p.HostFreeMB, VHDXPhysicalMB: p.VHDXPhysicalMB, VHDXKnown: s.VHDXPhysicalBytes > 0 && s.State != hostStorageVHDXUnresolved})
			now := time.Now()
			if e.clock != nil {
				now = e.clock.Now()
			}
			p.HostLoss = e.hostLoss.Assess(now)
		}
		if e.hostOffender != nil {
			if usages, _, ok := e.hostOffender.Latest(); ok {
				p.HostOffender = pickHostOffender(usages, 1<<30)
				if p.HostOffender != nil && p.HostOffender.NewestAt != nil {
					p.HostOffenderScannedAt = *p.HostOffender.NewestAt
				} else {
					p.HostOffenderScannedAt = s.At
				}
			}
		}
	}
	switch {
	case p.GuestLow && p.HostLow:
		p.BlockingLayer = "both"
	case p.HostLow:
		p.BlockingLayer = "host"
	case p.GuestLow:
		p.BlockingLayer = "guest"
	default:
		p.BlockingLayer = "none"
	}
	return p
}

func (e *hostStorageService) refreshHostOffender(ctx context.Context, p *diskPressureState) {
	if e == nil || p == nil || e.hostOffender == nil || !p.HostKnown || (!p.HostLoss.Detected && !p.HostLow) {
		return
	}
	usages, _ := e.hostOffender.Measure(ctx)
	p.HostOffender = pickHostOffender(usages, 1<<30)
}

func (p diskPressureState) hostOffenderSuffix() string {
	parts := make([]string, 0, 2)
	if p.HostOffender != nil {
		parts = append(parts, hostOffenderText(p.HostOffender))
	}
	if p.HostLoss.Detected {
		parts = append(parts, hostLossText(p.HostLoss))
	}
	if len(parts) == 0 {
		return ""
	}
	return " — " + strings.Join(parts, "; ") + " (delete manually; see docs/RUNBOOK-wsl-host-disk.md)"
}

var browserCommandRE = regexp.MustCompile(`(?i)\b(e2e|ui smoke|playwright|chromium|browser|screenshot|ship-check)\b`)

func heavyCommandClass(cmdName string) string {
	name := strings.TrimSpace(cmdName)
	if name == "capture-screens" || name == "preview-accept" || browserCommandRE.MatchString(name) {
		return "browser"
	}
	return "generic"
}

type launcherPolicy struct {
	MinFreeMB  int       `json:"minFreeMB"`
	MinFreePct int       `json:"minFreePct"`
	UpdatedAt  time.Time `json:"updatedAt"`
	Source     string    `json:"source"`
}

func systemHostDiskFromPressure(p diskPressureState) *systemHostDiskSample {
	h := &systemHostDiskSample{State: p.HostState, Detail: p.HostDetail, Volume: p.HostVolume, Distro: p.HostDistro, FloorPct: p.HostFloorPct, HysteresisMB: p.HostHysteresisMB, Low: p.HostLow, VHDXPath: p.VHDXPath, Sparse: p.VHDXSparse, BlockingLayer: p.BlockingLayer}
	if p.HostKnown {
		free, total, floor, physical, logical := p.HostFreeMB, p.HostTotalMB, p.HostFloorMB, p.VHDXPhysicalMB, p.VHDXLogicalMB
		h.FreeMB, h.TotalMB, h.FloorMB = &free, &total, &floor
		h.VHDXPhysicalMB, h.VHDXLogicalMB = &physical, &logical
		if p.HostFreeBytes > 0 || p.HostTotalBytes > 0 {
			freeBytes, totalBytes := p.HostFreeBytes, p.HostTotalBytes
			h.FreeBytes, h.TotalBytes = &freeBytes, &totalBytes
		}
		allocated, apparent := p.VHDXPhysicalBytes, p.VHDXLogicalBytes
		if allocated > 0 {
			h.VHDXAllocatedBytes = &allocated
		}
		if apparent > 0 {
			h.VHDXApparentBytes = &apparent
		}
	}
	if !p.HostOffenderScannedAt.IsZero() {
		at := p.HostOffenderScannedAt
		h.OffenderScannedAt = &at
	}
	if p.HostOffender != nil && p.HostOffender.DumpBytes > 0 {
		dump := p.HostOffender.DumpBytes
		h.DumpBytes = &dump
	}
	if !p.HostSampledAt.IsZero() {
		at := p.HostSampledAt
		h.SampledAt = &at
	}
	if p.HostLoss.Detected {
		unexplained, growth := p.HostLoss.UnexplainedMB, p.HostLoss.VHDXGrowthMB
		h.UnexplainedLossMB, h.VHDXGrowthMB = &unexplained, &growth
		h.LossWindowMin = int(p.HostLoss.WindowEnd.Sub(p.HostLoss.WindowStart).Round(time.Minute) / time.Minute)
	}
	if p.HostOffender != nil {
		h.Offender = &systemHostOffender{Label: p.HostOffender.Label, Path: p.HostOffender.Path, MB: p.HostOffender.Bytes / (1024 * 1024), Files: p.HostOffender.Files, Dumps: p.HostOffender.DumpFiles, Complete: p.HostOffender.Complete, Text: hostOffenderText(p.HostOffender)}
	}
	if p.HostLow {
		base := p.HostBaseFloorMB
		if base == 0 {
			base = max(int64(0), p.HostFloorMB-p.HostBrowserReserveMB)
		}
		class := p.Class
		if class == "" {
			class = "generic"
		}
		needed := p.hostDiskNeededMB()
		h.WhyBlocked = &systemHostDiskWhyBlocked{Layer: "host", Class: class, FreeMB: p.HostFreeMB, FloorMB: needed, FloorBreakdown: systemHostDiskFloorBreakdown{BaseMB: base, BrowserReserveMB: p.HostBrowserReserveMB, HysteresisMB: max(int64(0), needed-p.HostFloorMB), Source: p.HostFloorSource}, DeficitMB: max(int64(0), needed-p.HostFreeMB)}
	}
	return h
}

func (p diskPressureState) hostDiskNeededMB() int64 {
	return max(p.HostFloorMB, p.HostResumeFloorMB)
}

func (e *hostStorageService) publishLauncherPolicy(p diskPressureState) {
	if e == nil || p.LocalAppData == "" {
		return
	}
	// Publish resolved caps only; the controller floor is admission-only.
	absCap, pctCap := e.resolveHostDiskFloorMB(), e.resolveHostDiskFloorPct()
	// Disabling workflow admission must not also remove the launcher's WSL-start
	// safety check. Keep its last published non-zero policy (or its defaults when
	// no policy exists); launcher-only environment overrides remain independent.
	if hostDiskGateDisabled(absCap, pctCap) {
		return
	}
	// The launcher policy carries operator-chosen Settings/env floors verbatim,
	// but adaptive defaults protect different operations: the workflow's
	// 102400/5 floor defers admission around 100 GiB, while the launcher's
	// 16384/3 floor blocks WSL start. Publishing the adaptive workflow default
	// would make the launcher refuse to start the app below roughly 100 GiB.
	// Mapping each adaptive component also self-heals stale operator values when
	// an override is reverted; the mapped values participate in the cache below.
	abs, pct := absCap.Value, pctCap.Value
	if absCap.Source == capSourceAdaptive {
		abs = launcherHostDiskDefaultMB
	}
	if pctCap.Source == capSourceAdaptive {
		pct = launcherHostDiskDefaultPct
	}
	now := time.Now()
	if e.clock != nil {
		now = e.clock.Now()
	}
	e.launcherPolicyMu.Lock()
	defer e.launcherPolicyMu.Unlock()
	if abs == e.launcherPolicyAbs && pct == e.launcherPolicyPct && now.Sub(e.launcherPolicyAt) < 10*time.Minute {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	out, err := hostStorageWSLPath(ctx, p.LocalAppData)
	if err != nil {
		return
	}
	dir := filepath.Join(strings.TrimSpace(string(out)), "soluterminal")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return
	}
	b, _ := json.Marshal(launcherPolicy{MinFreeMB: abs, MinFreePct: pct, UpdatedAt: now.UTC(), Source: "solud"})
	tmp := filepath.Join(dir, ".host-disk-policy.tmp")
	if err := os.WriteFile(tmp, append(b, '\n'), 0600); err != nil {
		return
	}
	if err := os.Rename(tmp, filepath.Join(dir, "host-disk-policy.json")); err != nil {
		_ = os.Remove(tmp)
		return
	}
	e.launcherPolicyAbs, e.launcherPolicyPct, e.launcherPolicyAt = abs, pct, now
}

func (e *hostStorageService) publishLauncherPolicyFromSettings() {
	if e == nil {
		return
	}
	p := e.hostDiskLatestPressure("generic")
	e.launcherPolicyMu.Lock()
	e.launcherPolicyAt = time.Time{}
	e.launcherPolicyMu.Unlock()
	e.publishLauncherPolicy(p)
}
