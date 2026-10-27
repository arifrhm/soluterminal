package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// This file is cross-platform (no build tag): the rollout filename/id helpers
// are pure string parsing shared by the Unix /proc discovery scanner and the
// spawn-time resumability gate below, and the gate itself is a plain
// filesystem walk that works everywhere the daemon runs.

// codexRolloutWalkCap bounds how many directory entries codexRolloutOnDisk
// will visit before giving up. Rollouts live under sessions/YYYY/MM/DD/, so a
// year of heavy daily use is a few hundred directories and a few thousand
// files; the cap only trips on a pathological tree, where we fail OPEN (treat
// the thread as resumable) rather than wrongly abandoning a conversation we
// simply couldn't finish looking for.
const codexRolloutWalkCap = 100_000

// codexSessionsRoots returns the candidate roots that may hold Codex rollout
// files: $CODEX_HOME/sessions when set on the daemon (mirrors
// codexOpenRootThreadsAt, which reads it from the agent process env), plus the
// conventional ~/.codex/sessions.
func codexSessionsRoots() []string {
	var roots []string
	if home := strings.TrimSpace(os.Getenv("CODEX_HOME")); home != "" && filepath.IsAbs(home) {
		roots = append(roots, filepath.Join(filepath.Clean(home), "sessions"))
	}
	if home, err := os.UserHomeDir(); err == nil && strings.TrimSpace(home) != "" {
		def := filepath.Join(home, ".codex", "sessions")
		if len(roots) == 0 || roots[0] != def {
			roots = append(roots, def)
		}
	}
	return roots
}

// codexRolloutOnDisk reports whether threadID's rollout file exists under a
// known sessions root — the Codex counterpart of claudeTranscriptOnDisk, used
// to gate a resume before launching it. Codex has no O(1) path (the rollout
// filename embeds an unknowable timestamp: rollout-<ts>-<id>.jsonl), so this
// walks the sessions tree with an early stop on match; it therefore runs ONLY
// on the spawn path (once per resume attempt), never on the /api/panes poll.
//
// Verdict semantics are deliberately asymmetric: answering "no" abandons the
// conversation (the caller downgrades to a fresh start), so "no" requires a
// full, successful enumeration that found nothing. Any inability to look —
// no enumerable sessions root, a walk error, the entry cap — fails OPEN and
// lets the resume proceed; at worst that reproduces today's behavior (the
// app-server/TUI rejects the id), never a silent wrong fresh start.
func codexRolloutOnDisk(threadID string) bool {
	return codexRolloutOnDiskAt(codexSessionsRoots(), threadID)
}

// codexRolloutVerifiedOnDisk is the fail-closed variant for destructive fleet
// replacement: the exact rollout must be found, not merely fail-open because
// no sessions root could be enumerated.
func codexRolloutVerifiedOnDisk(threadID string) bool {
	return codexRolloutVerifiedOnDiskAt(codexSessionsRoots(), threadID)
}

func codexRolloutVerifiedOnDiskAt(roots []string, threadID string) bool {
	id, ok := canonicalCodexThreadID(threadID)
	if !ok {
		return false
	}
	for _, root := range roots {
		if fi, err := os.Stat(root); err != nil || !fi.IsDir() {
			continue
		}
		visited := 0
		verified := false
		_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			visited++
			if visited > codexRolloutWalkCap {
				return fs.SkipAll
			}
			if d.IsDir() {
				return nil
			}
			fid, matches := rolloutFilenameThreadID(d.Name())
			if matches && fid == id && codexRolloutFileHasIdentity(path, id) {
				verified = true
				return fs.SkipAll
			}
			return nil
		})
		if verified {
			return true
		}
	}
	return false
}

// codexRolloutFileHasIdentity proves that a filename match is a readable
// rollout for the requested root conversation. Fleet replacement is
// destructive, so unlike the ordinary resume helper it cannot rely on a name
// alone or fail open on malformed metadata.
func codexRolloutFileHasIdentity(path, threadID string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 4096), 64*1024)
	if !scanner.Scan() {
		return false
	}
	var meta struct {
		Type    string `json:"type"`
		Payload struct {
			ID        string `json:"id"`
			SessionID string `json:"session_id"`
		} `json:"payload"`
	}
	if err := json.Unmarshal(scanner.Bytes(), &meta); err != nil || meta.Type != "session_meta" {
		return false
	}
	id, idOK := canonicalCodexThreadID(meta.Payload.ID)
	sessionID, sessionOK := canonicalCodexThreadID(meta.Payload.SessionID)
	return idOK && sessionOK && id == threadID && sessionID == threadID
}

// codexRolloutOnDiskAt is the root-injectable implementation (unit-testable
// without touching the real ~/.codex tree).
func codexRolloutOnDiskAt(roots []string, threadID string) bool {
	found, enumerated := codexRolloutStatusAt(roots, threadID)
	return found || !enumerated
}

func codexRolloutStatusAt(roots []string, threadID string) (found, enumerated bool) {
	id, ok := canonicalCodexThreadID(threadID)
	if !ok {
		// Not a canonical thread id: it can never match a rollout filename, so
		// there is nothing a walk could find. Let the resume proceed and fail
		// with the provider's own error rather than silently dropping the id.
		return false, false
	}
	for _, root := range roots {
		if fi, err := os.Stat(root); err != nil || !fi.IsDir() {
			continue
		}
		visited := 0
		found := false
		walkClean := true
		err := filepath.WalkDir(root, func(_ string, d fs.DirEntry, err error) error {
			if err != nil {
				walkClean = false
				return fs.SkipDir
			}
			visited++
			if visited > codexRolloutWalkCap {
				walkClean = false
				return fs.SkipAll
			}
			if d.IsDir() {
				return nil
			}
			if fid, ok := rolloutFilenameThreadID(d.Name()); ok && fid == id {
				found = true
				return fs.SkipAll
			}
			return nil
		})
		if found {
			return true, enumerated
		}
		if err != nil && !errors.Is(err, fs.SkipAll) {
			walkClean = false
		}
		if walkClean {
			enumerated = true
		}
	}
	return false, enumerated
}

// rolloutFilenameThreadID extracts the thread id from a Codex rollout filename
// (rollout-<timestamp>-<uuid>.jsonl). Shared by the /proc discovery scanner
// and codexRolloutOnDisk.
func rolloutFilenameThreadID(name string) (string, bool) {
	if !strings.HasPrefix(name, "rollout-") || !strings.HasSuffix(name, ".jsonl") {
		return "", false
	}
	stem := strings.TrimSuffix(name, ".jsonl")
	if len(stem) < 37 || stem[len(stem)-37] != '-' {
		return "", false
	}
	return canonicalCodexThreadID(stem[len(stem)-36:])
}

// canonicalCodexThreadID lower-cases and validates a Codex thread id (UUID
// shape). Shared by discovery, adoption, and the resumability gate.
func canonicalCodexThreadID(raw string) (string, bool) {
	id := strings.ToLower(strings.TrimSpace(raw))
	if len(id) != 36 {
		return "", false
	}
	for i := 0; i < len(id); i++ {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if id[i] != '-' {
				return "", false
			}
			continue
		}
		if !((id[i] >= '0' && id[i] <= '9') || (id[i] >= 'a' && id[i] <= 'f')) {
			return "", false
		}
	}
	return id, true
}
