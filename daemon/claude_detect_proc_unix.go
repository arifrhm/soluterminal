//go:build !windows

package main

import (
	"os"
	"strconv"
)

// claudeProcessAlive reports whether any process on this machine is currently
// running the claude CLI (comm == "claude" — the same match the rss sampler
// uses to find pane descendants). It exists to corroborate an ABSENT detect
// verdict: a live claude process with no binary on disk means the binary is
// mid-replacement (Claude self-update), not uninstalled. Load-immune by
// construction — one /proc readdir plus small reads, never a fork — and LAZY
// (called only when a completed probe said absent AND the disk cross-check
// missed, i.e. almost never). Scans ALL processes, not just pane descendants:
// any running claude proves the install is real enough not to alarm about.
func claudeProcessAlive() bool {
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
		if readComm(pid) == "claude" {
			return true
		}
	}
	return false
}
