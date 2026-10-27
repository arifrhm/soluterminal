package main

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// File-browsing endpoints that back the Files-section autocomplete
// (GET /api/dir/list) and the quick-open fuzzy finder
// (GET /api/files/search). Both are read-only — the actual editing paths
// still go through files.go.

// dirListEntry is one child of a directory.
type dirListEntry struct {
	Name  string `json:"name"`
	IsDir bool   `json:"isDir"`
	Size  int64  `json:"size"`
}

type dirListResp struct {
	Path    string         `json:"path"`   // canonicalized absolute
	Parent  string         `json:"parent"` // parent dir or "" at root
	Entries []dirListEntry `json:"entries"`
}

// Cap per-listing. Past this the dropdown is useless anyway — the user
// should narrow with more characters.
const maxDirListEntries = 2000

func (a *apiServer) listDirContents(w http.ResponseWriter, r *http.Request) {
	abs, err := canonicalizeDirPath(r.URL.Query().Get("path"))
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	if reason := credentialPathRefusal(abs, a.dataDir); reason != "" {
		http.Error(w, reason, http.StatusForbidden)
		return
	}
	// Over the relay, a directory is listable when it is within a workspace
	// root, or is an ancestor of one (then only the entries leading on to a
	// root are shown). See files_relay_roots.go.
	scope := a.relayFileScopeFor(r)
	listDir := abs
	if scope != nil {
		canon, ok := relayCanonicalPath(abs)
		if !ok || !(scope.allowsCanonical(canon) || scope.leadsToRootCanonical(canon)) {
			http.Error(w, relayOutsideRootsRefusal, http.StatusForbidden)
			return
		}
		listDir = canon
	}
	info, err := os.Stat(listDir)
	if err != nil {
		http.Error(w, "cannot stat: "+err.Error(), 404)
		return
	}
	if !info.IsDir() {
		http.Error(w, "path is not a directory", 400)
		return
	}
	raw, err := os.ReadDir(listDir)
	if err != nil {
		http.Error(w, "cannot read: "+err.Error(), 500)
		return
	}
	// Over the relay, hide secret/credential entries (e.g. .ssh, .aws, .env)
	// from the listing so remote browsing can't enumerate them by name. The
	// local operator sees the full directory.
	overRelay := fileAccessOverRelay(r)
	entries := make([]dirListEntry, 0, len(raw))
	for _, e := range raw {
		entryPath := filepath.Join(abs, e.Name())
		if credentialPathRefusal(entryPath, a.dataDir) != "" || (overRelay && secretPathRefusal(entryPath) != "") {
			continue
		}
		if !scope.relayDirEntryVisible(listDir, e.Name(), e.Type()&os.ModeSymlink != 0) {
			continue
		}
		// Size is cheap via Info(); ignore errors (stale entries during walk).
		var size int64
		if fi, err := e.Info(); err == nil {
			size = fi.Size()
		}
		entries = append(entries, dirListEntry{
			Name:  e.Name(),
			IsDir: e.IsDir(),
			Size:  size,
		})
	}
	// Dirs first, then files; alphabetical within each. Hidden entries
	// (leading dot) are kept but sorted after their non-hidden siblings so
	// they don't dominate the dropdown.
	sort.SliceStable(entries, func(i, j int) bool {
		if entries[i].IsDir != entries[j].IsDir {
			return entries[i].IsDir
		}
		iHidden := strings.HasPrefix(entries[i].Name, ".")
		jHidden := strings.HasPrefix(entries[j].Name, ".")
		if iHidden != jHidden {
			return !iHidden
		}
		return strings.ToLower(entries[i].Name) < strings.ToLower(entries[j].Name)
	})
	if len(entries) > maxDirListEntries {
		entries = entries[:maxDirListEntries]
	}
	parent := filepath.Dir(abs)
	if parent == abs {
		parent = ""
	}
	writeJSON(w, 200, dirListResp{
		Path:    abs,
		Parent:  parent,
		Entries: entries,
	})
}

// canonicalizeDirPath: like canonicalizeFilePath but tailored to dirs.
// "" or "~" → home. Accepts trailing slash.
func canonicalizeDirPath(p string) (string, error) {
	if p == "" || p == "~" {
		if home, err := os.UserHomeDir(); err == nil && home != "" {
			return home, nil
		}
		return "", errors.New("path required")
	}
	if strings.HasPrefix(p, "~/") {
		home, _ := os.UserHomeDir()
		p = filepath.Join(home, p[2:])
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	return filepath.Clean(abs), nil
}

// fileSearchHit is one match from the fuzzy walker. IsDir lets the
// client render directory hits differently (trailing slash, folder
// glyph) and pick different open-semantics — opening a file opens a
// FileTile; "opening" a directory pins it as the Files-section anchor.
type fileSearchHit struct {
	Path  string `json:"path"` // absolute
	Name  string `json:"name"` // basename
	Rel   string `json:"rel"`  // relative to the walk root
	IsDir bool   `json:"isDir"`
}

type fileSearchResp struct {
	Root   string          `json:"root"`
	Hits   []fileSearchHit `json:"hits"`
	Walked int             `json:"walked"` // files scanned — for debugging if the walk gave up
	Capped bool            `json:"capped"` // true if we stopped early at walkCap
}

// Walk caps. On a 2k-file project the walk finishes in a few ms; on a
// 200k-file monorepo we stop early rather than hang the UI.
const (
	walkCap           = 15000
	maxSearchHits     = 50
	defaultSearchHits = 30
)

// skipDirNames is the hardcoded noise-dir list. Matches .gitignore
// conventions for common ecosystems without us having to parse one.
// Add sparingly — each entry here means files inside are invisible to
// quick-open, which is surprising if you actually need them.
var skipDirNames = map[string]bool{
	".git":          true,
	"node_modules":  true,
	".next":         true,
	".nuxt":         true,
	".svelte-kit":   true,
	".cache":        true,
	".turbo":        true,
	"dist":          true,
	"build":         true,
	"out":           true,
	"target":        true, // rust, jvm
	".venv":         true,
	"venv":          true,
	"__pycache__":   true,
	".pytest_cache": true,
	".mypy_cache":   true,
	".ruff_cache":   true,
	".tox":          true,
	".gradle":       true,
	".idea":         true,
	".vscode":       true,
	"vendor":        true, // go, php
	"bin":           true, // many, but usually generated
	"obj":           true, // .net
	"coverage":      true,
}

func (a *apiServer) searchFiles(w http.ResponseWriter, r *http.Request) {
	root, err := canonicalizeDirPath(r.URL.Query().Get("root"))
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	if reason := credentialPathRefusal(root, a.dataDir); reason != "" {
		http.Error(w, reason, http.StatusForbidden)
		return
	}
	// Over the relay the walk root must itself be within a workspace root.
	scope := a.relayFileScopeFor(r)
	if reason := scope.refusal(root); reason != "" {
		http.Error(w, reason, http.StatusForbidden)
		return
	}
	if info, err := os.Stat(root); err != nil {
		http.Error(w, "root not found: "+err.Error(), 404)
		return
	} else if !info.IsDir() {
		http.Error(w, "root is not a directory", 400)
		return
	}

	q := strings.TrimSpace(r.URL.Query().Get("q"))
	limit := defaultSearchHits
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= maxSearchHits {
			limit = n
		}
	}
	// kind: "files" (default), "dirs", or "all". Backwards-compatible —
	// existing callers get file-only results unchanged.
	kind := r.URL.Query().Get("kind")
	includeFiles := kind == "" || kind == "files" || kind == "all"
	includeDirs := kind == "dirs" || kind == "all"

	// Only redact secret/credential hits over the relay; local quick-open
	// should be able to find the operator's own .env / key files.
	hits, walked, capped := walkFileSearch(root, q, limit, includeFiles, includeDirs, fileAccessOverRelay(r), a.dataDir)
	if scope != nil {
		// The walk does not descend symlinked directories, but a symlinked
		// file (or the data dir under an explicit home root) can still point
		// out of the roots; drop such hits.
		kept := hits[:0]
		for _, h := range hits {
			if scope.refusal(h.Path) == "" {
				kept = append(kept, h)
			}
		}
		hits = kept
	}
	writeJSON(w, 200, fileSearchResp{
		Root:   root,
		Hits:   hits,
		Walked: walked,
		Capped: capped,
	})
}

func walkFileSearch(root, q string, limit int, includeFiles, includeDirs, redactSecrets bool, dataDirs ...string) (hits []fileSearchHit, walked int, capped bool) {
	dataDir := ""
	if len(dataDirs) > 0 {
		dataDir = dataDirs[0]
	}
	lowerQ := strings.ToLower(q)
	// Rank candidates so "exact basename" beats "matches in path": we
	// keep both a primary list (basename-contains / fuzzy) and a fallback
	// (path-only fuzzy) and merge.
	type ranked struct {
		hit   fileSearchHit
		score int // higher = better
	}
	var ranks []ranked

	scoreOf := func(name, rel string) int {
		lowerName := strings.ToLower(name)
		lowerRel := strings.ToLower(rel)
		if lowerQ == "" {
			return 1 // everything qualifies with empty query
		}
		if lowerName == lowerQ {
			return 100
		}
		if strings.HasPrefix(lowerName, lowerQ) {
			return 80
		}
		if strings.Contains(lowerName, lowerQ) {
			return 60
		}
		if strings.Contains(lowerRel, lowerQ) {
			return 40
		}
		if fuzzyMatch(lowerName, lowerQ) {
			return 20
		}
		if fuzzyMatch(lowerRel, lowerQ) {
			return 10
		}
		return 0
	}

	walkErr := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			// Unreadable subtree (permissions) — skip without aborting.
			if d != nil && d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if walked >= walkCap {
			capped = true
			return filepath.SkipAll
		}
		if d.IsDir() {
			name := d.Name()
			if path != root && credentialPathRefusal(path, dataDir) != "" {
				return filepath.SkipDir
			}
			if path != root && skipDirNames[name] {
				return filepath.SkipDir
			}
			// Hidden dirs other than the ones we've whitelisted above are
			// skipped by default — .ssh, .aws, etc. If the query starts
			// with "." the user is probably hunting for one of those, so
			// let it through.
			if path != root && strings.HasPrefix(name, ".") && !strings.HasPrefix(lowerQ, ".") {
				return filepath.SkipDir
			}
			if includeDirs && path != root {
				rel, _ := filepath.Rel(root, path)
				if score := scoreOf(name, rel); score > 0 {
					ranks = append(ranks, ranked{
						hit: fileSearchHit{
							Path:  path,
							Name:  name,
							Rel:   rel,
							IsDir: true,
						},
						score: score,
					})
				}
			}
			return nil
		}
		walked++
		if !includeFiles {
			return nil
		}
		if credentialPathRefusal(path, dataDir) != "" || (redactSecrets && secretPathRefusal(path) != "") {
			// Over the relay, don't surface secret/credential files in
			// quick-open — readFile would 403 them anyway, and the path
			// itself can be sensitive. Locally the operator sees them.
			return nil
		}
		name := d.Name()
		rel, _ := filepath.Rel(root, path)
		if score := scoreOf(name, rel); score > 0 {
			ranks = append(ranks, ranked{
				hit: fileSearchHit{
					Path: path,
					Name: name,
					Rel:  rel,
				},
				score: score,
			})
		}
		return nil
	})
	if walkErr != nil && !errors.Is(walkErr, filepath.SkipAll) {
		// Non-fatal: return what we have.
		_ = walkErr
	}

	sort.SliceStable(ranks, func(i, j int) bool {
		if ranks[i].score != ranks[j].score {
			return ranks[i].score > ranks[j].score
		}
		// Shorter relative path as tiebreaker — "foo.ts" beats
		// "a/b/c/d/foo.ts" when both basenames match equally.
		if len(ranks[i].hit.Rel) != len(ranks[j].hit.Rel) {
			return len(ranks[i].hit.Rel) < len(ranks[j].hit.Rel)
		}
		return ranks[i].hit.Rel < ranks[j].hit.Rel
	})
	if len(ranks) > limit {
		ranks = ranks[:limit]
	}
	hits = make([]fileSearchHit, len(ranks))
	for i, r := range ranks {
		hits[i] = r.hit
	}
	return
}
