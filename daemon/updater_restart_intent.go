package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	restartIntentAutoQuiet         = "auto_quiet"
	restartIntentHumanForced       = "human_forced"
	restartIntentCriticalPolicy    = "critical_policy"
	restartIntentFleetSafeBoundary = "fleet_safe_boundary"
	restartIntentHumanRollback     = "human_rollback"

	restartIntentMaxAge           = 15 * time.Minute
	restartIntentMaxInvocationIDs = 50
)

type restartIntent struct {
	Reason          string   `json:"reason"`
	From            string   `json:"from"`
	To              string   `json:"to"`
	DrainDurationMs int64    `json:"drainDurationMs"`
	Inflight        int      `json:"inflight"`
	InvocationIDs   []string `json:"invocationIds,omitempty"`
	WrittenAtMs     int64    `json:"writtenAtMs"`
}

// Apply's restartGate is the production single-writer invariant. This mutex
// also makes the file helper safe for same-process recovery and test callers:
// every write uses the fixed same-directory temporary filename.
var restartIntentMu sync.Mutex

func restartIntentPath(selfPath string) string { return selfPath + ".restart-intent" }

// writeRestartIntent uses a same-directory temp + rename and fsyncs both the
// record and (best effort) its directory. The swap must not begin unless this
// audit record is durable.
func writeRestartIntent(selfPath string, ri restartIntent) error {
	if strings.TrimSpace(selfPath) == "" {
		return errors.New("restart intent self path is empty")
	}
	restartIntentMu.Lock()
	defer restartIntentMu.Unlock()

	if len(ri.InvocationIDs) > restartIntentMaxInvocationIDs {
		ri.InvocationIDs = append([]string(nil), ri.InvocationIDs[:restartIntentMaxInvocationIDs]...)
	}
	if !validRestartIntentReason(ri.Reason) || ri.WrittenAtMs <= 0 {
		return errors.New("invalid restart intent")
	}
	b, err := json.Marshal(ri)
	if err != nil {
		return err
	}
	path := restartIntentPath(selfPath)
	// Any orphan belongs to an earlier aborted attempt. Removing it before the
	// atomic write also gives Windows rename the same replace semantics as Unix.
	clearRestartIntentUnlocked(selfPath)
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	removeTmp := true
	defer func() {
		_ = f.Close()
		if removeTmp {
			_ = os.Remove(tmp)
		}
	}()
	if _, err = f.Write(b); err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	removeTmp = false
	if dir, err := os.Open(filepath.Dir(path)); err == nil {
		_ = dir.Sync()
		_ = dir.Close()
	}
	return nil
}

// readAndClearRestartIntent consumes the record exactly once. Invalid and
// stale records are deleted and treated as crash/unknown attribution.
func readAndClearRestartIntent(selfPath string, now time.Time) *restartIntent {
	if strings.TrimSpace(selfPath) == "" {
		return nil
	}
	restartIntentMu.Lock()
	defer restartIntentMu.Unlock()

	path := restartIntentPath(selfPath)
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	clearRestartIntentUnlocked(selfPath)
	var ri restartIntent
	if err := json.Unmarshal(b, &ri); err != nil || !validRestartIntentReason(ri.Reason) || ri.WrittenAtMs <= 0 {
		return nil
	}
	written := time.UnixMilli(ri.WrittenAtMs)
	if now.Sub(written) > restartIntentMaxAge {
		return nil
	}
	if len(ri.InvocationIDs) > restartIntentMaxInvocationIDs {
		ri.InvocationIDs = append([]string(nil), ri.InvocationIDs[:restartIntentMaxInvocationIDs]...)
	}
	return &ri
}

func clearRestartIntent(selfPath string) {
	if strings.TrimSpace(selfPath) == "" {
		return
	}
	restartIntentMu.Lock()
	defer restartIntentMu.Unlock()
	clearRestartIntentUnlocked(selfPath)
}

func clearRestartIntentUnlocked(selfPath string) {
	_ = os.Remove(restartIntentPath(selfPath))
	_ = os.Remove(restartIntentPath(selfPath) + ".tmp")
}

func validRestartIntentReason(reason string) bool {
	switch reason {
	case restartIntentAutoQuiet, restartIntentHumanForced, restartIntentCriticalPolicy, restartIntentFleetSafeBoundary, restartIntentHumanRollback:
		return true
	default:
		return false
	}
}

func readRestartIntentForSelf(now time.Time) *restartIntent {
	selfPath, ok := selfBinaryPath()
	if !ok {
		return nil
	}
	return readAndClearRestartIntent(selfPath, now)
}
