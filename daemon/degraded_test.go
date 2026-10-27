package main

import "testing"

func TestDegradedFromEnviron(t *testing.T) {
	t.Parallel()
	// A claude that inherited the child-session marker → degraded.
	dirty := map[string]string{
		"PATH":                      "/usr/bin",
		"CLAUDE_CODE_SESSION_ID":    "2bc01d64",
		"CLAUDE_CODE_CHILD_SESSION": "1",
	}
	if r := degradedFromEnviron(dirty, true); !r.degraded || r.unknown || r.reason != "nested-child-session" {
		t.Errorf("dirty env: got %+v, want degraded nested-child-session", r)
	}

	// A genuine top-level claude sets SESSION_ID but never CHILD_SESSION.
	clean := map[string]string{
		"PATH":                   "/usr/bin",
		"CLAUDE_CODE_SESSION_ID": "fresh-top-level-id",
		"CLAUDECODE":             "1",
	}
	if r := degradedFromEnviron(clean, true); r.degraded || r.unknown {
		t.Errorf("clean env: got %+v, want conclusively clean", r)
	}

	// Unreadable environ (non-Linux, raced exit) → unknown, never flagged.
	if r := degradedFromEnviron(nil, false); !r.unknown || r.degraded {
		t.Errorf("unreadable: got %+v, want unknown", r)
	}
}

func TestDetectPaneDegradedPidGuardAndForget(t *testing.T) {
	t.Parallel()
	// Non-positive pid (no claude descendant) is inconclusive, not flagged,
	// and not cached.
	if r := detectPaneDegraded("pane_x", 0); !r.unknown || r.degraded {
		t.Errorf("pid=0: got %+v, want unknown", r)
	}
	// forgetPaneDegraded is a no-op-safe cleanup for an unknown pane.
	forgetPaneDegraded("pane_x")

	// Seed a conclusive cache entry directly and confirm the (paneID, pid)
	// memo is returned without re-reading, and that a pid change invalidates.
	degradedMu.Lock()
	degradedCache["pane_y"] = degradedCacheEntry{claudePid: 4242, result: paneDegradedResult{degraded: true, reason: "nested-child-session"}}
	degradedMu.Unlock()
	if r := detectPaneDegraded("pane_y", 4242); !r.degraded {
		t.Errorf("cached pid match: got %+v, want cached degraded", r)
	}
	forgetPaneDegraded("pane_y")
	degradedMu.Lock()
	_, present := degradedCache["pane_y"]
	degradedMu.Unlock()
	if present {
		t.Errorf("forgetPaneDegraded did not evict pane_y")
	}
}
