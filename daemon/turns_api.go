package main

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// GET /api/panes/{id}/turns — the pane's completed turns, newest first.
// GET /api/panes/{id}/turns/{n}/diff — one turn's diff in the same shape as
// /api/git/diff, so the DiffTile renders it unchanged.

func (a *apiServer) paneTurns(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, ok := a.mgr.Get(id); !ok {
		http.Error(w, "not found", 404)
		return
	}
	t := activeTurnTracker.Load()
	if t == nil {
		writeJSON(w, 200, map[string]any{"turns": []turnRecord{}})
		return
	}
	turns, activeStarted := t.list(id)
	for i := range turns {
		turns[i].BaseTree, turns[i].EndTree = "", ""
		if t.providerLifecycle && turns[i].Source == "" {
			turns[i].Note = "Legacy terminal activity estimate; completion unverified. " + turns[i].Note
		}
	}
	resp := map[string]any{"turns": turns}
	if activeStarted > 0 {
		resp["activeStartedMs"] = activeStarted
	}
	writeJSON(w, 200, resp)
}

func (a *apiServer) paneTurnDiff(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, ok := a.mgr.Get(id); !ok {
		http.Error(w, "not found", 404)
		return
	}
	n, err := strconv.Atoi(r.PathValue("n"))
	if err != nil {
		http.Error(w, "bad turn number", 400)
		return
	}
	t := activeTurnTracker.Load()
	if t == nil {
		http.Error(w, "turn tracking unavailable", 404)
		return
	}
	rec, ok := t.get(id, n)
	if !ok {
		http.Error(w, "no such turn", 404)
		return
	}
	if !rec.hasDiff() {
		msg := rec.Note
		if msg == "" {
			msg = "no snapshot for this turn"
		}
		http.Error(w, msg, 404)
		return
	}
	if reason := credentialPathRefusal(rec.Repo, a.dataDir); reason != "" {
		http.Error(w, reason, http.StatusForbidden)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	resp, err := buildTurnDiff(ctx, rec, fileAccessOverRelay(r), a.dataDir)
	if err != nil {
		if turnObjectMissing(err) {
			// gc pruned the unreachable snapshot trees.
			http.Error(w, "this turn's snapshot is no longer available", http.StatusGone)
			return
		}
		http.Error(w, "couldn't build this turn's diff: "+err.Error(), http.StatusServiceUnavailable)
		return
	}
	writeJSON(w, 200, resp)
}

// buildTurnDiff renders one turn's base→end diff in /api/git/diff's shape.
// --no-renames is load-bearing: with rename detection git prints a renamed
// path as "old => new", which the secret-path check cannot match — a secret
// file renamed during a turn would ship its contents over the relay.
func buildTurnDiff(ctx context.Context, rec turnRecord, overRelay bool, dataDir string) (map[string]any, error) {
	release, err := acquireDiffEndpointSlot(ctx)
	defer release()
	if err != nil {
		return nil, err
	}
	d, err := hardenedDiff(ctx, rec.Repo, []string{rec.BaseTree, rec.EndTree}, "", overRelay, dataDir)
	if err != nil {
		return nil, err
	}
	label := "Turn " + strconv.Itoa(rec.N)
	if rec.Branch != "" && rec.Branch != "HEAD" {
		label += " · " + rec.Branch
	}
	return map[string]any{
		"repo":             rec.Repo,
		"branch":           label,
		"staged":           false,
		"files":            d.files,
		"diff":             d.text,
		"truncated":        d.truncated,
		"hiddenSecret":     d.hidden,
		"turn":             rec.N,
		"startedMs":        rec.StartedMs,
		"endedMs":          rec.EndedMs,
		"overlapPanes":     rec.OverlapPanes,
		"skippedUntracked": rec.SkippedUntracked,
	}, nil
}

// turnSummaryFor is the DTO hook; empty when tracking is off or the pane has
// no turn yet.
func turnSummaryFor(paneID string) *paneTurnDTO {
	t := activeTurnTracker.Load()
	if t == nil {
		return nil
	}
	return t.summary(strings.TrimSpace(paneID))
}

// turnObjectMissing reports a git failure caused by a pruned snapshot tree.
func turnObjectMissing(err error) bool {
	msg := strings.ToLower(err.Error())
	for _, needle := range []string{"bad object", "bad revision", "not a tree object", "invalid object", "unable to read tree", "unknown revision"} {
		if strings.Contains(msg, needle) {
			return true
		}
	}
	return false
}
