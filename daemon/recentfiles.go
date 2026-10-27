package main

import (
	"net/http"
	"path/filepath"
	"sync"
)

// recentFiles is a small, in-memory, local-only ring of files that agents
// recently CREATED or EDITED, fed from the Claude Code Edit/Write/MultiEdit/
// NotebookEdit hooks. It backs the sidebar's "files the agent just touched →
// one-tap open" list — built for vibe-coding users who drive agents and want
// to peek at / tweak what was just written without browsing the tree.
//
// Why a separate structure from the uiBus activity feed: that feed redacts
// file paths to basename-only (it's exposed broadly), so its entries aren't
// openable. Here we keep the FULL absolute path so the UI can open the file,
// and the serving endpoint transport-gates secret/credential paths so nothing
// leaks over the cloud relay (mirrors the file-read + git-diff guards).
//
// Recency is the point, so the ring is intentionally small and ephemeral
// (lost on daemon restart) — "what just happened", not a durable history.

type recentFileTouch struct {
	Path      string `json:"path"`                // absolute, openable
	Name      string `json:"name"`                // basename, for display
	Dir       string `json:"dir"`                 // parent dir, to disambiguate same-named files
	Tool      string `json:"tool"`                // Edit | MultiEdit | Write | NotebookEdit
	PaneID    string `json:"paneId,omitempty"`    // which agent pane touched it (best-effort)
	PaneTitle string `json:"paneTitle,omitempty"` // pane label, for display
	Ts        int64  `json:"ts"`                  // unix millis of the touch
}

type recentFilesRing struct {
	mu    sync.Mutex
	items []recentFileTouch // oldest-first; newest appended at the end
	max   int
}

func newRecentFilesRing() *recentFilesRing { return &recentFilesRing{max: 100} }

// record upserts a touch: any prior entry for the same path is dropped and a
// fresh one appended at the end, so the most-recently-touched file is always
// last. Bounded to `max` entries.
func (r *recentFilesRing) record(t recentFileTouch) {
	if t.Path == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]recentFileTouch, 0, len(r.items)+1)
	for _, it := range r.items {
		if it.Path != t.Path {
			out = append(out, it)
		}
	}
	out = append(out, t)
	if len(out) > r.max {
		out = out[len(out)-r.max:]
	}
	r.items = out
}

// recent returns up to `limit` touches, newest first.
func (r *recentFilesRing) recent(limit int) []recentFileTouch {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]recentFileTouch, 0, len(r.items))
	for i := len(r.items) - 1; i >= 0 && len(out) < limit; i-- {
		out = append(out, r.items[i])
	}
	return out
}

// hookEditedPath returns the absolute path of a file an agent CREATED or
// EDITED via this tool call, resolving a relative file_path against the hook's
// cwd. ok=false for non-editing tools (Read, Bash, Grep, …) — "touched" here
// means modified, not merely viewed.
func hookEditedPath(tool string, input map[string]any, cwd string) (string, bool) {
	switch tool {
	case "Edit", "MultiEdit", "Write", "NotebookEdit":
	default:
		return "", false
	}
	p, _ := input["file_path"].(string)
	if p == "" {
		return "", false
	}
	if !filepath.IsAbs(p) {
		if cwd == "" {
			return "", false
		}
		p = filepath.Join(cwd, p)
	}
	return filepath.Clean(p), true
}

type recentFilesResp struct {
	Files []recentFileTouch `json:"files"`
}

// GET /api/recent-files — newest-first list of agent-touched files. Over the
// cloud relay, secret/credential paths are omitted (never surfaced remotely).
func (a *apiServer) handleRecentFiles(w http.ResponseWriter, r *http.Request) {
	if a.recentFiles == nil {
		writeJSON(w, 200, recentFilesResp{Files: []recentFileTouch{}})
		return
	}
	over := fileAccessOverRelay(r)
	scope := a.relayFileScopeFor(r)
	items := a.recentFiles.recent(50)
	out := make([]recentFileTouch, 0, len(items))
	for _, it := range items {
		if credentialPathRefusal(it.Path, a.dataDir) != "" || (over && secretPathRefusal(it.Path) != "") {
			continue // don't reveal secret/credential file paths over the relay
		}
		if scope.refusal(it.Path) != "" {
			continue // nor paths outside the relay's workspace roots
		}
		out = append(out, it)
	}
	writeJSON(w, 200, recentFilesResp{Files: out})
}
