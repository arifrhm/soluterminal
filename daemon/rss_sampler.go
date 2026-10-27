//go:build !windows

package main

import (
	"context"
	"log"
	"os"
	"strconv"
	"strings"
	"time"
)

// rss_sampler.go periodically samples the resident memory of each pane's
// selected Claude or Codex descendant. That is the long-lived process a Focus
// "Restart agent" action replaces; sampling the lightweight `solud agent`
// wrapper would under-report the memory the action actually reclaims.
//
// Two hard-won constraints, both load-bearing:
//   - It samples the provider DESCENDANT, never the pane's own Pid. p.Pid is the
//     `solud agent` wrapper sitting flat at ~7-10MB; the provider heap lives in
//     the `claude` or `codex` child.
//   - It runs on a SLOW (60s) timer and reuses the 1s-TTL process-tree cache, so
//     it is nowhere near computeState's per-pane hot path — the one that
//     GC-death-spiraled the daemon on per-tick /proc scans (ringbuf.go).

const rssSamplerInterval = 60 * time.Second

// rssSamplerEnabled gates the sampler. DEFAULT-ON now that it feeds the header
// memory chip (one /proc read per native pane every 60s — trivial, and off the
// hot path). Retained as a kill-switch: set SOLU_RSS_SAMPLER to "0"/"false"/
// "off"/"no" to disable (chip then hides, no per-pane sampling).
func rssSamplerEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("SOLU_RSS_SAMPLER"))) {
	case "0", "false", "off", "no":
		return false
	default:
		return true
	}
}

// startRSSSampler launches the sampler goroutine when SOLU_RSS_SAMPLER is set;
// it's a no-op otherwise (zero cost for everyone not measuring).
func startRSSSampler(ctx context.Context, mgr *Manager) {
	if !rssSamplerEnabled() {
		return
	}
	log.Printf("[rss] sampler enabled (every %s) — logging Claude/Codex descendant VmRSS/VmHWM", rssSamplerInterval)
	go func() {
		// Populate restored panes immediately so a newly opened app does not wait
		// a full interval before its first RAM chip appears.
		sampleRSSOnce(mgr)
		ticker := time.NewTicker(rssSamplerInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				sampleRSSOnce(mgr)
			}
		}
	}()
}

func sampleRSSOnce(mgr *Manager) {
	tree := snapshotProcessTree()
	for _, p := range mgr.List() {
		snap := p.Snapshot()
		// Sample ANY running pane (native Focus AND terminal). Panes with no
		// descendant matching their selected provider resolve to 0 and hide the
		// chip.
		if snap.Status != PaneRunning || snap.Pid <= 0 {
			continue
		}
		agent := effectiveAgentKind(snap.Agent)
		agentPid := findAgentDescendantPID(tree, snap.Pid, agent, readComm)
		agentRSS, _, _ := readVmRSSHWMkb(snap.Pid)
		if agentPid == 0 {
			p.agentRssMB.Store(0) // no selected-provider child → hide the chip
			log.Printf("[rss] pane=%s wrapper.rss=%dMB provider=%s provider.rss=none", snap.ID, agentRSS/1024, agent)
			continue
		}
		rss, hwm, ok := readVmRSSHWMkb(agentPid)
		if !ok {
			continue
		}
		// Cache the current resident size for the DTO / header memory chip; the
		// chip reads this cached value, never /proc, so it stays off the hot path.
		p.agentRssMB.Store(int64(rss / 1024))
		conversationID := conversationIDForAgent(snap.Agent, snap.ClaudeSessionID, snap.CodexThreadID, snap.MuseSessionID)
		log.Printf("[rss] pane=%s wrapper.rss=%dMB provider=%s provider.rss=%dMB provider.hwm=%dMB conversation=%s",
			snap.ID, agentRSS/1024, agent, rss/1024, hwm/1024, shortSessionID(conversationID))
	}
}

func findAgentDescendantPID(tree processTreeSnapshot, root int, agent string, comm func(int) string) int {
	want := effectiveAgentKind(agent)
	for _, pid := range tree.descendantPids(root) {
		if pid != root && comm(pid) == want {
			return pid
		}
	}
	return 0
}

// readVmRSSHWMkb parses VmRSS (current resident) and VmHWM (peak resident), both
// in kB, from /proc/<pid>/status. VmHWM is the high-water mark — the number that
// reveals whether the heap once grew large even if a GC has since shrunk VmRSS.
func readVmRSSHWMkb(pid int) (rssKB, hwmKB int, ok bool) {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/status")
	if err != nil {
		return 0, 0, false
	}
	for _, line := range strings.Split(string(b), "\n") {
		switch {
		case strings.HasPrefix(line, "VmRSS:"):
			rssKB = parseStatusKB(line)
		case strings.HasPrefix(line, "VmHWM:"):
			hwmKB = parseStatusKB(line)
		}
	}
	return rssKB, hwmKB, true
}

// parseStatusKB pulls the integer kB value out of a "VmRSS:   12345 kB" line.
func parseStatusKB(line string) int {
	f := strings.Fields(line)
	if len(f) < 2 {
		return 0
	}
	v, _ := strconv.Atoi(f[1])
	return v
}
