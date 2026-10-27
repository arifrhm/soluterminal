package main

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// workflowStatus is the wire shape of a pane's in-progress background workflow.
// Present on the DTO only while nativeWorkflowActive is latched.
type workflowStatus struct {
	Running        bool  `json:"running"`        // always true when present (latch is set)
	AgentsTotal    int   `json:"agentsTotal"`    // distinct agents started (grows across phases)
	AgentsDone     int   `json:"agentsDone"`     // agents that have logged a result
	LastActivityMs int64 `json:"lastActivityMs"` // unix-ms of the freshest workflow file mtime (0 = nothing written yet)
}

// workflowJournalReadCap bounds the journal read so a pathological journal can't
// stall the poll path. Real journals are tens-to-hundreds of KB.
const workflowJournalReadCap = 8 << 20

// paneWorkflowStatus returns the pane's background-workflow status, or nil when
// no workflow is latched. Gated on the latch FIRST (one atomic load) so the
// common no-workflow pane does zero filesystem work.
func paneWorkflowStatus(p *Pane) *workflowStatus {
	if p.Kind != "native" || !p.nativeWorkflowActive.Load() {
		return nil
	}
	dir := workflowsDirForPane(p)
	if dir == "" {
		// Latched but the workflows dir isn't on disk yet (just launched, nothing
		// written) — still report "running" so the footer shows the workflow the
		// instant the turn settles, even before the first journal line lands.
		return &workflowStatus{Running: true}
	}
	if st := scanWorkflowStatus(dir); st != nil {
		return st
	}
	return &workflowStatus{Running: true}
}

// workflowsDirForPane resolves the session's subagents/workflows directory, or ""
// when it can't be located / doesn't exist. Prefers the authoritative transcript
// path's project dir (it follows session-id rotation), then falls back to the
// cwd-derived project slug under each claude config dir — the same resolution
// transcriptForPane / claudeTranscriptOnDisk use.
func workflowsDirForPane(p *Pane) string {
	snap := p.Snapshot()
	sid := strings.TrimSpace(snap.ClaudeSessionID)
	if sid == "" {
		return ""
	}
	// (0) Project dir of the pinned MAIN transcript: <projectDir>/<sid>.jsonl, so
	// the workflows live at <projectDir>/<sid>/subagents/workflows.
	if tp := strings.TrimSpace(snap.ClaudeTranscriptPath); tp != "" && !isSubagentTranscript(tp) {
		if dir := existingDir(filepath.Join(filepath.Dir(tp), sid, "subagents", "workflows")); dir != "" {
			return dir
		}
	}
	// (1) cwd → project slug under each config dir.
	cwd := expandHome(strings.TrimSpace(snap.Cwd))
	if cwd == "" {
		return ""
	}
	enc := claudeProjectDirName(cwd)
	for _, base := range claudeConfigDirs() {
		if dir := existingDir(filepath.Join(base, "projects", enc, sid, "subagents", "workflows")); dir != "" {
			return dir
		}
	}
	return ""
}

// existingDir returns path if it's an existing directory, else "".
func existingDir(path string) string {
	if fi, err := os.Stat(path); err == nil && fi.IsDir() {
		return path
	}
	return ""
}

// scanWorkflowStatus reads the most-recently-active wf_* subdir under the
// workflows dir and reports its agent counts + freshest mtime. Returns nil when
// there's no wf_* dir yet. "Most recently active" (freshest file mtime) handles a
// session that has several wf_* dirs from earlier runs — we report the live one.
func scanWorkflowStatus(dir string) *workflowStatus {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var bestDir string
	var bestMod time.Time
	for _, e := range entries {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), "wf_") {
			continue
		}
		entryDir := filepath.Join(dir, e.Name())
		if mod := newestModTime(entryDir); mod.After(bestMod) {
			bestMod = mod
			bestDir = entryDir
		}
	}
	if bestDir == "" {
		return nil
	}
	total, done := countWorkflowAgents(filepath.Join(bestDir, "journal.jsonl"))
	var lastMs int64
	if !bestMod.IsZero() {
		lastMs = bestMod.UnixMilli()
	}
	return &workflowStatus{
		Running:        true,
		AgentsTotal:    total,
		AgentsDone:     done,
		LastActivityMs: lastMs,
	}
}

// newestModTime returns the freshest mtime among the files directly inside entryDir
// (journal.jsonl + the agent-*.jsonl). This is the workflow's liveness heartbeat:
// the harness appends to these as subagents stream, so a recent mtime = working,
// a stale one = stalled. Zero time when the dir is unreadable / empty.
func newestModTime(entryDir string) time.Time {
	var newest time.Time
	entries, err := os.ReadDir(entryDir)
	if err != nil {
		return newest
	}
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			continue
		}
		if info.ModTime().After(newest) {
			newest = info.ModTime()
		}
	}
	return newest
}

// countWorkflowAgents reads the workflow journal and counts top-level
// {"type":"started"} and {"type":"result"} entries (one per agent edge). total =
// started, done = result. Parses each line's top-level "type" (not a raw
// substring count) so a nested result payload that happens to contain the literal
// string can't inflate the count. Bounded read; a malformed line is skipped.
func countWorkflowAgents(journalPath string) (total, done int) {
	f, err := os.Open(journalPath)
	if err != nil {
		return 0, 0
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, workflowJournalReadCap))
	if err != nil {
		return 0, 0
	}
	for _, line := range bytes.Split(data, []byte{'\n'}) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var rec struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(line, &rec) != nil {
			continue
		}
		switch rec.Type {
		case "started":
			total++
		case "result":
			done++
		}
	}
	return total, done
}
