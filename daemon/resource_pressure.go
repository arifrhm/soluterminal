package main

import (
	"runtime"
	"time"
)

const (
	hostProbeTTL        = 30 * time.Second
	hostProbeStaleAfter = 90 * time.Second
)

// pSISample is one Linux pressure-stall sample. OK distinguishes an
// unavailable probe from a real zero-pressure reading.
type pSISample struct {
	Some10 float64 `json:"some10"`
	Some60 float64 `json:"some60"`
	Full10 float64 `json:"full10"`
	Full60 float64 `json:"full60"`
	OK     bool    `json:"ok"`
}

type hostMemSample struct {
	FreeMB     int64     `json:"freeMB,omitempty"`
	TotalMB    int64     `json:"totalMB,omitempty"`
	SampledAt  time.Time `json:"sampledAt,omitempty"`
	Status     string    `json:"status"`
	Err        string    `json:"error,omitempty"`
	Provenance string    `json:"provenance,omitempty"`
}

func (s hostMemSample) Fresh(now time.Time) bool {
	return s.Status == "ok" && !s.SampledAt.IsZero() && !now.Before(s.SampledAt) && now.Sub(s.SampledAt) <= hostProbeStaleAfter
}

type pressureSample struct {
	At             time.Time     `json:"at"`
	MemAvailableMB int64         `json:"memAvailableMB"`
	MemTotalMB     int64         `json:"memTotalMB"`
	DiskFreeMB     int64         `json:"diskFreeMB"`
	MemPSI         pSISample     `json:"memPSI"`
	CPUPSI         pSISample     `json:"cpuPSI"`
	IOPSI          pSISample     `json:"ioPSI"`
	SwapOutPerSec  float64       `json:"swapOutPerSec,omitempty"`
	SwapOutKnown   bool          `json:"swapOutKnown"`
	OOMKillDelta   int64         `json:"oomKillDelta,omitempty"`
	OOMKillKnown   bool          `json:"oomKillKnown"`
	LoadAvg1       float64       `json:"loadAvg1,omitempty"`
	LoadKnown      bool          `json:"loadKnown"`
	NProc          int           `json:"nproc"`
	Host           hostMemSample `json:"host"`
	Provenance     string        `json:"provenance"`
}

type vmstatCounters struct {
	At      time.Time
	PSWPOut uint64
	OOMKill uint64
	OK      bool
}

type pressureProbes struct {
	psi                    func(string) (pSISample, bool)
	vmstat                 func() (uint64, uint64, bool)
	loadAvg                func() (float64, bool)
	nproc                  func() int
	memAvailKB, memTotalKB func() (int64, bool)
	disk                   func(string) (int64, bool)
	host                   func() hostMemSample
	wsl                    func() bool
}

func samplePressure(p pressureProbes, _ *pressureSample, prev *vmstatCounters, now time.Time, diskPath string) (pressureSample, vmstatCounters) {
	s := pressureSample{At: now, MemAvailableMB: -1, MemTotalMB: -1, DiskFreeMB: -1, Provenance: runtime.GOOS}
	if p.wsl != nil && p.wsl() {
		s.Provenance = "wsl"
	}
	if p.memAvailKB != nil {
		if kb, ok := p.memAvailKB(); ok {
			s.MemAvailableMB = kb / 1024
		}
	}
	if p.memTotalKB != nil {
		if kb, ok := p.memTotalKB(); ok {
			s.MemTotalMB = kb / 1024
		}
	}
	if p.disk != nil {
		if b, ok := p.disk(diskPath); ok {
			s.DiskFreeMB = b / (1024 * 1024)
		}
	}
	if p.psi != nil {
		if v, ok := p.psi("memory"); ok {
			v.OK = true
			s.MemPSI = v
		}
		if v, ok := p.psi("cpu"); ok {
			v.OK = true
			s.CPUPSI = v
		}
		if v, ok := p.psi("io"); ok {
			v.OK = true
			s.IOPSI = v
		}
	}
	if p.loadAvg != nil {
		s.LoadAvg1, s.LoadKnown = p.loadAvg()
	}
	if p.nproc != nil {
		s.NProc = p.nproc()
	}
	if p.host != nil {
		s.Host = p.host()
	}
	if s.Host.Status == "" {
		s.Host.Status = "n/a"
	}
	next := vmstatCounters{At: now}
	if p.vmstat != nil {
		next.PSWPOut, next.OOMKill, next.OK = p.vmstat()
	}
	if prev != nil && prev.OK && next.OK && now.After(prev.At) {
		dt := now.Sub(prev.At).Seconds()
		if next.PSWPOut >= prev.PSWPOut {
			s.SwapOutKnown = true
			s.SwapOutPerSec = float64(next.PSWPOut-prev.PSWPOut) / dt
		}
		if next.OOMKill >= prev.OOMKill {
			s.OOMKillKnown = true
			s.OOMKillDelta = int64(next.OOMKill - prev.OOMKill)
		}
	}
	return s, next
}
