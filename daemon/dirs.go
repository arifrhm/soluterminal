package main

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"
)

// DirCandidate is one candidate cwd returned by /api/dirs.
type DirCandidate struct {
	Path   string `json:"path"`   // absolute
	Source string `json:"source"` // "recent" | "discovered"
	Label  string `json:"label"`  // user-friendly (~ replaced)
}

// CandidateRoots are the well-known project parents scanned for
// subdirectories. Each direct child of these is a candidate.
func CandidateRoots() []string {
	home, _ := os.UserHomeDir()
	if home == "" {
		return nil
	}
	return []string{
		filepath.Join(home, "wsl-projects"),
		filepath.Join(home, "projects"),
		filepath.Join(home, "src"),
		filepath.Join(home, "code"),
		filepath.Join(home, "dev"),
		home, // the home dir itself — lets `~` match
	}
}

// ListDirs returns a deduped, ranked list of candidate directories.
// `recent` is the caller's list of recently-used cwds (from panes.json).
// `q` is an optional query; if non-empty we fuzzy-filter.
func ListDirs(recent []string, q string, limit int) []DirCandidate {
	seen := map[string]bool{}
	// Non-nil so an empty result encodes as [] (not null) for the web picker.
	out := []DirCandidate{}

	add := func(path, source string) {
		if path == "" {
			return
		}
		path = filepath.Clean(path)
		if seen[path] {
			return
		}
		if st, err := os.Stat(path); err != nil || !st.IsDir() {
			return
		}
		seen[path] = true
		out = append(out, DirCandidate{
			Path:   path,
			Source: source,
			Label:  abbreviateHome(path),
		})
	}

	// 1) Recent cwds (ordered as-given — caller controls recency).
	for _, c := range recent {
		add(c, "recent")
	}

	// 2) Each root's immediate subdirs.
	for _, root := range CandidateRoots() {
		entries, err := os.ReadDir(root)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			// Skip hidden dirs unless the user's query clearly wants them.
			if strings.HasPrefix(e.Name(), ".") && !strings.HasPrefix(q, ".") {
				continue
			}
			add(filepath.Join(root, e.Name()), "discovered")
		}
	}

	if q != "" {
		out = fuzzyFilter(out, q)
	}

	// Stable ordering: recent first, then label-sorted.
	sort.SliceStable(out, func(i, j int) bool {
		if (out[i].Source == "recent") != (out[j].Source == "recent") {
			return out[i].Source == "recent"
		}
		return out[i].Label < out[j].Label
	})

	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

// fuzzyFilter keeps candidates whose label contains the query's characters in
// order, case-insensitive. Simple but effective for typo-tolerant matching.
func fuzzyFilter(in []DirCandidate, q string) []DirCandidate {
	q = strings.ToLower(q)
	out := in[:0]
	for _, c := range in {
		if fuzzyMatch(strings.ToLower(c.Label), q) || fuzzyMatch(strings.ToLower(c.Path), q) {
			out = append(out, c)
		}
	}
	return out
}

func fuzzyMatch(haystack, needle string) bool {
	if needle == "" {
		return true
	}
	i := 0
	for _, r := range haystack {
		if unicode.ToLower(r) == rune(needle[i]) {
			i++
			if i == len(needle) {
				return true
			}
		}
	}
	return false
}

func abbreviateHome(p string) string {
	home, _ := os.UserHomeDir()
	if home != "" && (p == home || strings.HasPrefix(p, home+"/")) {
		return "~" + strings.TrimPrefix(p, home)
	}
	return p
}
