package main

import (
	"time"
)

type statsResolvedCap struct {
	Value  int    `json:"value"`
	Source string `json:"source"` // settings | env | adaptive
}

// statsResolvedFlag is a boolean knob's effective value plus provenance, so
// a stale toggle is as visible as a stale cap.
type statsResolvedFlag struct {
	Value  bool   `json:"value"`
	Source string `json:"source"` // settings | env | adaptive
}

type statsPressure struct {
	At             time.Time         `json:"at"`
	MemAvailableMB *int64            `json:"memAvailableMB,omitempty"`
	MemTotalMB     *int64            `json:"memTotalMB,omitempty"`
	DiskFreeMB     *int64            `json:"diskFreeMB,omitempty"`
	MemPSISome10   *float64          `json:"memPSISome10,omitempty"`
	MemPSISome60   *float64          `json:"memPSISome60,omitempty"`
	MemPSIFull10   *float64          `json:"memPSIFull10,omitempty"`
	MemPSIFull60   *float64          `json:"memPSIFull60,omitempty"`
	CPUPSISome60   *float64          `json:"cpuPSISome60,omitempty"`
	IOPSIFull60    *float64          `json:"ioPSIFull60,omitempty"`
	SwapOutPerSec  *float64          `json:"swapOutPerSec,omitempty"`
	OOMKillDelta   *int64            `json:"oomKillDelta,omitempty"`
	LoadAvg1       *float64          `json:"loadAvg1,omitempty"`
	NProc          int               `json:"nproc,omitempty"`
	Provenance     string            `json:"provenance"`
	Host           statsPressureHost `json:"host"`
}

type statsGitWedge struct {
	Command      string    `json:"command"`
	RepoKey      string    `json:"repoKey"`
	LandLockHeld bool      `json:"landLockHeld"`
	At           time.Time `json:"at"`
}

func statsPressureFromSample(s pressureSample, now time.Time) statsPressure {
	intp := func(ok bool, v int64) *int64 {
		if !ok {
			return nil
		}
		x := v
		return &x
	}
	floatp := func(ok bool, v float64) *float64 {
		if !ok {
			return nil
		}
		x := v
		return &x
	}
	out := statsPressure{At: s.At, MemAvailableMB: intp(s.MemAvailableMB >= 0, s.MemAvailableMB), MemTotalMB: intp(s.MemTotalMB >= 0, s.MemTotalMB), DiskFreeMB: intp(s.DiskFreeMB >= 0, s.DiskFreeMB), NProc: s.NProc, Provenance: s.Provenance}
	out.MemPSISome10, out.MemPSISome60 = floatp(s.MemPSI.OK, s.MemPSI.Some10), floatp(s.MemPSI.OK, s.MemPSI.Some60)
	out.MemPSIFull10, out.MemPSIFull60 = floatp(s.MemPSI.OK, s.MemPSI.Full10), floatp(s.MemPSI.OK, s.MemPSI.Full60)
	out.CPUPSISome60, out.IOPSIFull60 = floatp(s.CPUPSI.OK, s.CPUPSI.Some60), floatp(s.IOPSI.OK, s.IOPSI.Full60)
	out.SwapOutPerSec, out.OOMKillDelta, out.LoadAvg1 = floatp(s.SwapOutKnown, s.SwapOutPerSec), intp(s.OOMKillKnown, s.OOMKillDelta), floatp(s.LoadKnown, s.LoadAvg1)
	hostKnown := s.Host.Status == "ok" || s.Host.Status == "stale"
	out.Host = statsPressureHost{Status: s.Host.Status, FreeMB: intp(hostKnown, s.Host.FreeMB), TotalMB: intp(s.Host.TotalMB > 0, s.Host.TotalMB), Provenance: s.Host.Provenance, Error: s.Host.Err}
	if !s.Host.SampledAt.IsZero() {
		out.Host.AgeSec = int64(max(time.Duration(0), now.Sub(s.Host.SampledAt)).Seconds())
	}
	return out
}
