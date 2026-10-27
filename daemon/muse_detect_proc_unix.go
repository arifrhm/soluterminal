//go:build !windows

package main

import (
	"os"
	"strconv"
)

// museProcessAlive reports whether any process on this machine is currently
// running the muse CLI (comm == "muse"). It exists to corroborate an ABSENT
// detect verdict: a live muse process with no binary on disk means the binary
// is mid-replacement (self-update), not uninstalled. Load-immune by
// construction — one /proc readdir plus small reads, never a fork — and LAZY
// (called only when a completed probe said absent AND the disk cross-check
// missed, i.e. almost never).
func museProcessAlive() bool {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return false
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		if readComm(pid) == "muse" {
			return true
		}
	}
	return false
}
