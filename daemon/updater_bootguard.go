package main

// Systemd update rollback (the production path's brick-loop guard).
//
// On the systemd Apply path the daemon swaps its binary and exits, letting
// `Restart=always` re-exec the new binary as the same unit. That path has no
// health handshake: if the new binary log.Fatalf's on boot, systemd crash-loops
// it until StartLimitBurst trips and the unit goes `failed` — dead, with the
// broken binary installed. (The NON-systemd path is already protected by the
// successor boot-marker handshake in Apply.)
//
// The guard: the predecessor drops a `.update-pending` marker before exiting;
// the freshly-restarted binary, at the very TOP of main, increments an attempt
// counter, and after maxUpdateBootAttempts genuine boot failures restores the
// parked `.old` and execs it in place (same systemd-tracked PID). The marker is
// cleared at the POST-INIT, PRE-BIND commit point (clearUpdatePendingMarker),
// reached on every boot that clears all fatal-prone init — so a binary that
// boots fine but loses the port to a FOREIGN process (EADDRINUSE) is never
// counted as a failed update, and a binary that crashes DURING init is.

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
)

// maxUpdateBootAttempts is how many failed post-update boots we tolerate before
// rolling back. 3 absorbs a one-off transient (slow disk, a state-file lock)
// without a spurious rollback, while still reverting a genuinely-broken binary
// within ~RestartSec*3 — comfortably inside systemd's StartLimitBurst headroom.
const maxUpdateBootAttempts = 3

type updatePending struct {
	To       string `json:"to"`
	From     string `json:"from"`
	Attempts int    `json:"attempts"`
}

func updatePendingPath(selfPath string) string { return selfPath + ".update-pending" }

// selfBinaryPath resolves the running binary's real (symlink-evaluated) path,
// matching how Apply/rollback resolve it so they all touch the same files.
func selfBinaryPath() (string, bool) {
	p, err := os.Executable()
	if err != nil {
		return "", false
	}
	if rp, e := filepath.EvalSymlinks(p); e == nil {
		p = rp
	}
	return p, true
}

// writeUpdatePending records that an update was just applied under systemd, so
// the successor's boot guard can roll back if the new binary fails to boot.
// Called by the predecessor on the systemd Apply path, just before it exits.
func writeUpdatePending(selfPath, to, from string) {
	b, _ := json.Marshal(updatePending{To: to, From: from, Attempts: 0})
	if err := os.WriteFile(updatePendingPath(selfPath), b, 0o644); err != nil {
		slog.Warn("update-pending marker write failed", "path", updatePendingPath(selfPath), "err", err)
	}
}

func readUpdatePending(selfPath string) *updatePending {
	b, err := os.ReadFile(updatePendingPath(selfPath))
	if err != nil {
		return nil
	}
	var p updatePending
	if json.Unmarshal(b, &p) != nil {
		return nil
	}
	return &p
}

// clearUpdatePendingMarker removes the marker. Called at the post-init/pre-bind
// commit point: by reaching it, the binary has proven it boots past all
// fatal-prone init, so it must NOT be rolled back on a later restart. No-op when
// there's no marker (the common, non-update boot).
func clearUpdatePendingMarker() {
	selfPath, ok := selfBinaryPath()
	if !ok {
		return
	}
	_ = os.Remove(updatePendingPath(selfPath))
}

// maybeRollbackAfterFailedUpdate runs at the very TOP of main(), BEFORE any
// fatal-prone init and BEFORE maybeReexecCleanEnv. If a freshly-swapped binary
// has failed to reach the commit point maxUpdateBootAttempts times, it is bad:
// restore the parked .old and exec it in place. Loop-guarded by
// SOLU_ROLLBACK_DONE so the restored binary can't re-trigger the guard. No-op
// when there's no pending marker — i.e. on every normal boot, and on every
// platform other than the systemd Apply path that writes the marker.
func maybeRollbackAfterFailedUpdate() {
	selfPath, ok := selfBinaryPath()
	if !ok {
		return
	}
	maybeRollbackForSelf(selfPath)
}

// execInPlaceHook is the indirection point for the in-place exec so tests can
// stub it (the real execInPlace would replace the test process). Defaults to the
// platform implementation.
var execInPlaceHook = execInPlace

// maybeRollbackForSelf is the path-parameterized core, split out for testing.
func maybeRollbackForSelf(selfPath string) {
	pend := readUpdatePending(selfPath)
	if pend == nil {
		return
	}
	if os.Getenv("SOLU_ROLLBACK_DONE") == "1" {
		// Already rolled back in this exec lineage — don't loop. Drop the
		// marker so a later unrelated restart starts clean.
		_ = os.Remove(updatePendingPath(selfPath))
		return
	}
	if pend.Attempts >= maxUpdateBootAttempts {
		slog.Error("new binary failed to boot cleanly; rolling back to previous",
			"to", pend.To, "from", pend.From, "attempts", pend.Attempts)
		if err := rollbackSwappedBinary(selfPath); err != nil {
			// Nothing to roll back to (no .old, or ring exhausted). Looping
			// forever on a guard that can't act is worse than surfacing the
			// broken binary — clear the marker and let systemd keep trying.
			slog.Error("rollback failed; clearing marker to avoid a boot loop", "err", err)
			_ = os.Remove(updatePendingPath(selfPath))
			return
		}
		writeVersionMarker(selfPath, pend.From)
		_ = os.Remove(updatePendingPath(selfPath))
		// Exec the restored binary in place so systemd keeps the same MainPID.
		// Preserve the env (incl. SOLU_ENV_CLEANED if set) and add the
		// loop guard so this can't re-trigger.
		env := append(os.Environ(), "SOLU_ROLLBACK_DONE=1")
		if err := execInPlaceHook(selfPath, os.Args, env); err != nil {
			slog.Error("exec restored binary failed; letting systemd restart", "err", err)
		}
		return
	}
	// Count this boot attempt and continue. We only roll back after
	// maxUpdateBootAttempts genuine failures; the marker is cleared the moment
	// init succeeds (clearUpdatePendingMarker at the pre-bind commit point), so
	// a boot that reaches that point never increments again.
	pend.Attempts++
	b, _ := json.Marshal(pend)
	_ = os.WriteFile(updatePendingPath(selfPath), b, 0o644)
}
