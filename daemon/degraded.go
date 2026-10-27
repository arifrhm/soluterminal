package main

import "sync"

// paneDegradedResult is the tri-state outcome of degraded-pane detection.
//
// A "degraded" pane is one whose running `claude` inherited the dirty
// CLAUDE_CODE_CHILD_SESSION env (the daemon was launched from inside a claude
// session, pre-self-clean) and therefore runs as a nested child session that
// NEVER writes its own transcript — so its conversation/Activity view is
// permanently empty until the pane's claude is replaced.
//
// unknown=true means detection was inconclusive (no claude descendant yet,
// environ unreadable, or non-Linux where /proc/<pid>/environ doesn't exist).
// Callers MUST NOT flag a pane as degraded on unknown — only a conclusive
// positive read surfaces to clients, so unaffected/undetectable panes never
// raise a false alarm.
type paneDegradedResult struct {
	degraded bool
	reason   string // "" unless degraded; e.g. "nested-child-session"
	unknown  bool
}

type degradedCacheEntry struct {
	claudePid int
	result    paneDegradedResult
}

var (
	degradedMu    sync.Mutex
	degradedCache = map[string]degradedCacheEntry{} // paneID -> entry
)

// detectPaneDegraded reports whether the pane's running `claude` (pid
// claudePid) is a nested child session that won't write a transcript.
//
// The result is memoized per (paneID, claudePid): a live process's environ is
// immutable (that immutability is the whole root cause — you can't un-dirty a
// running process), so the answer only changes when the claude pid changes (a
// respawn/migration), at which point the cache misses and we re-read. This
// makes steady-state /api/panes polling cost zero environ reads for
// already-classified panes. The map is bounded to one entry per pane; call
// forgetPaneDegraded on pane removal.
func detectPaneDegraded(paneID string, claudePid int) paneDegradedResult {
	if claudePid <= 0 {
		return paneDegradedResult{unknown: true}
	}
	degradedMu.Lock()
	if e, ok := degradedCache[paneID]; ok && e.claudePid == claudePid {
		degradedMu.Unlock()
		return e.result
	}
	degradedMu.Unlock()

	res := classifyClaudeEnviron(claudePid)
	// Only cache a CONCLUSIVE result. An unknown (raced exit / unreadable
	// environ / non-Linux) must be retried on the next poll, not pinned.
	if !res.unknown {
		degradedMu.Lock()
		degradedCache[paneID] = degradedCacheEntry{claudePid: claudePid, result: res}
		degradedMu.Unlock()
	}
	return res
}

// classifyClaudeEnviron reads the claude process's environment and classifies
// it. See degradedFromEnviron for the (pure, tested) rule.
func classifyClaudeEnviron(claudePid int) paneDegradedResult {
	env, ok := readProcEnviron(claudePid)
	return degradedFromEnviron(env, ok)
}

// degradedFromEnviron is the pure classification rule. It keys on the presence
// of CLAUDE_CODE_CHILD_SESSION: a genuine top-level claude sets its own
// CLAUDE_CODE_SESSION_ID but NEVER CHILD_SESSION, so CHILD_SESSION's only
// source is inheritance — the unambiguous degraded marker. ok=false (environ
// unreadable / non-Linux) is inconclusive → unknown.
func degradedFromEnviron(env map[string]string, ok bool) paneDegradedResult {
	if !ok {
		return paneDegradedResult{unknown: true}
	}
	if _, dirty := env[childSessionEnvKey]; dirty {
		return paneDegradedResult{degraded: true, reason: "nested-child-session"}
	}
	return paneDegradedResult{} // conclusively clean
}

// forgetPaneDegraded drops a pane's cached detection. Call when a pane is
// removed so the cache stays bounded to live panes.
func forgetPaneDegraded(paneID string) {
	degradedMu.Lock()
	delete(degradedCache, paneID)
	degradedMu.Unlock()
}
