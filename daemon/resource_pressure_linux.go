//go:build linux

package main

import (
	"bufio"
	"os"
	"runtime"
	"strconv"
	"strings"
)

func readPSI(kind string) (pSISample, bool) {
	f, err := os.Open("/proc/pressure/" + kind)
	if err != nil {
		return pSISample{}, false
	}
	defer f.Close()
	var out pSISample
	var some10, some60, full10, full60 bool
	s := bufio.NewScanner(f)
	for s.Scan() {
		fields := strings.Fields(s.Text())
		if len(fields) < 2 {
			continue
		}
		vals := map[string]float64{}
		for _, field := range fields[1:] {
			k, raw, ok := strings.Cut(field, "=")
			if !ok {
				continue
			}
			if v, err := strconv.ParseFloat(raw, 64); err == nil {
				vals[k] = v
			}
		}
		switch fields[0] {
		case "some":
			out.Some10, some10 = vals["avg10"]
			out.Some60, some60 = vals["avg60"]
		case "full":
			out.Full10, full10 = vals["avg10"]
			out.Full60, full60 = vals["avg60"]
		}
	}
	// A malformed or partial line is unknown, not zero pressure. Linux CPU PSI
	// may legitimately omit the full line; memory and I/O expose both lines.
	out.OK = some10 && some60 && s.Err() == nil && (kind == "cpu" || full10 && full60)
	return out, out.OK
}

func readVmstatCounters() (pswpout, oomKill uint64, ok bool) {
	f, err := os.Open("/proc/vmstat")
	if err != nil {
		return 0, 0, false
	}
	defer f.Close()
	var swapOK, oomOK bool
	s := bufio.NewScanner(f)
	for s.Scan() {
		fields := strings.Fields(s.Text())
		if len(fields) != 2 {
			continue
		}
		v, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil {
			continue
		}
		switch fields[0] {
		case "pswpout":
			pswpout, swapOK = v, true
		case "oom_kill":
			oomKill, oomOK = v, true
		}
	}
	return pswpout, oomKill, swapOK && oomOK && s.Err() == nil
}

func readProcCPUTimes(pid int) (utime, stime, cutime, cstime uint64, ok bool) {
	f, ok := statFieldsAfterComm(pid)
	if !ok || len(f) <= 14 {
		return 0, 0, 0, 0, false
	}
	vals := []*uint64{&utime, &stime, &cutime, &cstime}
	for i, dst := range vals {
		v, err := strconv.ParseUint(f[11+i], 10, 64)
		if err != nil {
			return 0, 0, 0, 0, false
		}
		*dst = v
	}
	return utime, stime, cutime, cstime, true
}

func defaultPressureProbes() pressureProbes {
	return pressureProbes{
		psi: readPSI, vmstat: readVmstatCounters, loadAvg: readLoadAvg1,
		nproc: runtime.NumCPU, memAvailKB: memAvailableKB, memTotalKB: readMemTotalKB,
		disk: diskFreeBytes, wsl: isInsideWSL,
	}
}
