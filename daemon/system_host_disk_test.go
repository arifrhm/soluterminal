package main

import (
	"strings"
	"testing"
	"time"
)

func TestSystemLevelsSeparatesWindowsHostDisk(t *testing.T) {
	guestFree, guestTotal, hostFree, hostTotal, floor := int64(667000), int64(1048576), int64(41000), int64(2050000), int64(61500)
	rss, cpu := 100.0, 1.0
	mem := int64(10000)
	unexplained := int64(51095)
	sparse := false
	s := systemSample{DiskFreeMB: &guestFree, DiskTotalMB: &guestTotal, DiskFloorMB: 10240, MemAvailableMB: &mem, DaemonRssMB: &rss, DaemonCpuPct: &cpu, HostDisk: &systemHostDiskSample{State: hostStorageOK, Volume: `C:\`, FreeMB: &hostFree, TotalMB: &hostTotal, FloorMB: &floor, VHDXPhysicalMB: &hostTotal, Sparse: &sparse, Low: true, UnexplainedLossMB: &unexplained, Offender: &systemHostOffender{Text: `51.0 GB of WSL crash dumps (10 .dmp files) in %TEMP%\wsl-crashes`}}, EngineWarn: true, EngineHostDiskLow: true}
	_, state := systemLevels(s, time.Now(), levelState{})
	levels, _ := systemLevels(s, time.Now(), state)
	if levels.Areas["disk"] != "ok" || levels.Areas["hostDisk"] != "critical" || !strings.Contains(levels.Reason, "host disk:") || !strings.Contains(levels.Reason, `%TEMP%\wsl-crashes`) || !strings.Contains(levels.Reason, "unexplained host loss") {
		t.Fatalf("levels=%+v", levels)
	}
	var engine string
	for _, r := range levels.Reasons {
		if r.Key == "engine" {
			engine = r.Text
		}
	}
	if !strings.Contains(engine, "host disk floor active") {
		t.Fatalf("engine reason=%q all=%+v", engine, levels)
	}
}

func TestSystemLevelsNonWSLKeySetUnchanged(t *testing.T) {
	free, total := int64(100000), int64(200000)
	rss, cpu := 100.0, 1.0
	mem := int64(10000)
	levels, _ := systemLevels(systemSample{DiskFreeMB: &free, DiskTotalMB: &total, MemAvailableMB: &mem, DaemonRssMB: &rss, DaemonCpuPct: &cpu}, time.Now(), levelState{})
	if _, ok := levels.Areas["hostDisk"]; ok {
		t.Fatalf("native sample added hostDisk: %+v", levels.Areas)
	}
}
