//go:build linux

package main

import (
	"os"
	"strconv"
	"strings"
	"syscall"
)

func readProcCPUTicks(pid int) (ticks uint64, starttime uint64, ok bool) {
	fields, ok := statFieldsAfterComm(pid)
	if !ok || len(fields) <= 19 {
		return 0, 0, false
	}
	utime, e1 := strconv.ParseUint(fields[11], 10, 64)
	stime, e2 := strconv.ParseUint(fields[12], 10, 64)
	start, e3 := strconv.ParseUint(fields[19], 10, 64)
	if e1 != nil || e2 != nil || e3 != nil {
		return 0, 0, false
	}
	return utime + stime, start, true
}

func readHostCPUTicks() (busy, total uint64, ok bool) {
	b, err := os.ReadFile("/proc/stat")
	if err != nil {
		return 0, 0, false
	}
	line := strings.SplitN(string(b), "\n", 2)[0]
	f := strings.Fields(line)
	if len(f) < 6 || f[0] != "cpu" {
		return 0, 0, false
	}
	vals := make([]uint64, 0, len(f)-1)
	for _, raw := range f[1:] {
		v, e := strconv.ParseUint(raw, 10, 64)
		if e != nil {
			return 0, 0, false
		}
		vals = append(vals, v)
		total += v
	}
	idle := vals[3]
	iowait := uint64(0)
	if len(vals) > 4 {
		iowait = vals[4]
	}
	return total - idle - iowait, total, true
}

func readLoadAvg1() (float64, bool) {
	b, e := os.ReadFile("/proc/loadavg")
	if e != nil {
		return 0, false
	}
	f := strings.Fields(string(b))
	if len(f) == 0 {
		return 0, false
	}
	v, e := strconv.ParseFloat(f[0], 64)
	return v, e == nil
}

func systemDiskUsage(path string) (free, total int64, ok bool) {
	if path == "" {
		return 0, 0, false
	}
	var st syscall.Statfs_t
	if syscall.Statfs(path, &st) != nil {
		return 0, 0, false
	}
	return int64(st.Bavail) * int64(st.Bsize), int64(st.Blocks) * int64(st.Bsize), true
}

func systemReadRSS(pid int) (int, int, bool) { return readVmRSSHWMkb(pid) }
