package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// paneOriginAliases remembers which pane ids a workspace restore replaced
// (old → new), so a pane restored later — from another workspace, after its
// starter's workspace came back first — still finds its starter and lead.
// Persisted under the data dir and bounded; informational like PaneOrigin.
type paneOriginAliases struct {
	mu   sync.Mutex
	path string
	m    map[string]paneOriginAlias
}

type paneOriginAlias struct {
	To string `json:"to"`
	At int64  `json:"at"`
}

const (
	paneOriginAliasFile = "pane-origin-aliases.json"
	paneOriginAliasMax  = 2000
)

func newPaneOriginAliases(dataDir string) *paneOriginAliases {
	s := &paneOriginAliases{path: filepath.Join(dataDir, paneOriginAliasFile), m: map[string]paneOriginAlias{}}
	if data, err := os.ReadFile(s.path); err == nil {
		_ = json.Unmarshal(data, &s.m)
	}
	return s
}

func (s *paneOriginAliases) add(remap map[string]string, now time.Time) {
	if s == nil || len(remap) == 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for from, to := range remap {
		if from == "" || to == "" || from == to {
			continue
		}
		s.m[from] = paneOriginAlias{To: to, At: now.UnixMilli()}
		// Keep every chain one hop long: ids that led to `from` now lead to `to`.
		for k, a := range s.m {
			if a.To == from && k != to {
				s.m[k] = paneOriginAlias{To: to, At: a.At}
			}
		}
		delete(s.m, to) // `to` is live again; it must not lead elsewhere
	}
	if len(s.m) > paneOriginAliasMax {
		keys := make([]string, 0, len(s.m))
		for k := range s.m {
			keys = append(keys, k)
		}
		sort.Slice(keys, func(i, j int) bool { return s.m[keys[i]].At > s.m[keys[j]].At })
		for _, k := range keys[paneOriginAliasMax:] {
			delete(s.m, k)
		}
	}
	if data, err := json.Marshal(s.m); err == nil {
		_ = writeFileAtomic(s.path, data, 0o600)
	}
}

// resolve follows restores forward to the latest id (chains are kept one hop
// long by add; the walk still stops on a loop in a hand-edited file).
func (s *paneOriginAliases) resolve(id string) string {
	if s == nil || id == "" {
		return id
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	seen := map[string]bool{}
	for !seen[id] {
		seen[id] = true
		next, ok := s.m[id]
		if !ok {
			break
		}
		id = next.To
	}
	return id
}

// resolveOrigin returns a copy of o with its starter and lead resolved.
func (s *paneOriginAliases) resolveOrigin(o *PaneOrigin) *PaneOrigin {
	if s == nil || o == nil {
		return o
	}
	c := o.clone()
	c.ByPaneID = s.resolve(c.ByPaneID)
	c.LeadPaneID = s.resolve(c.LeadPaneID)
	return c
}
