package main

import "strings"

// Pinned Muse builds retain their versioned executable name in Linux comm
// (truncated to 15 bytes), rather than renaming themselves to "muse".
func isMuseProcessName(comm string) bool {
	return comm == "muse" || strings.HasPrefix(comm, "muse-bin-")
}

// isMuseCmdline recognizes a Muse argv when /proc comm matching misses. The
// `muse` launcher is a bash script, so during its update-check phase the
// process reports comm "bash", not "muse"; wrappers (version managers, shims)
// rename it too. The argv basename is the stable signal in those windows -
// the same NO-comm-prefilter rationale as the managed Pi/DeepSeek markers.
// Kept pure so the marker is pinned by a unit test, not only by a live tree.
//
// Only the exact basename "muse" and the versioned "muse-bin[-*]" binaries
// match: in particular a "muse-spark-*" model id riding in a --model arg must
// not mark a pane.
func isMuseCmdline(cmd string) bool {
	for _, field := range strings.Fields(cmd) {
		base := field
		if i := strings.LastIndexByte(field, '/'); i >= 0 {
			base = field[i+1:]
		}
		if base == "muse" || base == "muse-bin" || strings.HasPrefix(base, "muse-bin-") {
			return true
		}
	}
	return false
}

func adoptForegroundMuse(p *Pane, sessionID string) bool {
	p.mu.Lock()
	recipeChanged := p.replaceStaleForegroundRecipeLocked(agentMuse)
	changed := recipeChanged || p.Agent != agentMuse || p.codexIdentityBlocked.Load() != (sessionID == "")
	p.Agent = agentMuse
	// Share the foreground-ambiguity latch; preserve every provider's durable
	// ID while discovery is unavailable. Fleet replacement checks it for Muse.
	p.codexIdentityBlocked.Store(sessionID == "")
	if sessionID != "" && p.MuseSessionID != sessionID {
		p.MuseSessionID = sessionID
		changed = true
	}
	p.mu.Unlock()
	if changed && p.onPromptsCommit != nil {
		p.onPromptsCommit(p)
	}
	return changed
}
