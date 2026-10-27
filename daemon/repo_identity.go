package main

// Canonical repository identity for scheduler coordination. Overlap metadata
// and the landing coordinator must agree that /repo, /repo/web, and a symlink
// alias of /repo are the SAME repository — filepath.Clean alone treated them
// as three unrelated paths, so same-repo tasks missed each other's holds and
// landings could race. Git's common dir is the one identity every checkout
// view of a repository shares (including linked worktrees).

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

var repoKeyCache = struct {
	sync.Mutex
	m map[string]repoKeyCacheEntry
}{m: map[string]repoKeyCacheEntry{}}

type repoKeyCacheEntry struct {
	key        string
	anchorPath string
	anchorInfo os.FileInfo
	commonInfo os.FileInfo
}

// gitToplevel returns the working-tree toplevel a human recognizes and the
// submit validation expects, rather than the git-common-dir coordination key.
func gitToplevel(dir string) (string, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	out, err := runGit(abs, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", err
	}
	top := strings.TrimSpace(out)
	if top == "" {
		return "", fmt.Errorf("git returned an empty repository toplevel")
	}
	return top, nil
}

// canonicalRepoKey resolves repoPath to a stable coordination key: the
// absolute, symlink-resolved git common directory. Non-git paths (and any git
// failure) fall back to the symlink-resolved absolute path, then to
// filepath.Clean — the function always returns a usable key.
func canonicalRepoKey(repoPath string) string {
	if key, ok := cachedCanonicalRepoKey(repoPath); ok {
		return key
	}
	key := computeRepoKey(repoPath)
	rememberCanonicalRepoKey(repoPath, key)
	return key
}

func cachedCanonicalRepoKey(repoPath string) (string, bool) {
	repoKeyCache.Lock()
	entry, ok := repoKeyCache.m[repoPath]
	repoKeyCache.Unlock()
	if !ok || entry.anchorInfo == nil || entry.commonInfo == nil {
		return "", false
	}
	anchor, anchorErr := os.Stat(entry.anchorPath)
	common, commonErr := os.Stat(entry.key)
	if anchorErr != nil || commonErr != nil || !os.SameFile(anchor, entry.anchorInfo) || !os.SameFile(common, entry.commonInfo) {
		repoKeyCache.Lock()
		delete(repoKeyCache.m, repoPath)
		repoKeyCache.Unlock()
		return "", false
	}
	return entry.key, true
}

func rememberCanonicalRepoKey(repoPath, key string) {
	if repoPath == "" || key == "" {
		return
	}
	anchorPath := repoPath
	if absolute, err := filepath.Abs(repoPath); err == nil {
		anchorPath = absolute
	}
	anchorInfo, anchorErr := os.Stat(anchorPath)
	commonInfo, commonErr := os.Stat(key)
	if anchorErr != nil || commonErr != nil {
		return
	}
	repoKeyCache.Lock()
	repoKeyCache.m[repoPath] = repoKeyCacheEntry{key: key, anchorPath: anchorPath, anchorInfo: anchorInfo, commonInfo: commonInfo}
	repoKeyCache.Unlock()
}

// cargoRepositoryIdentity includes the common directory's filesystem
// identity. Linked worktrees therefore share one target, while replacing a
// repository at the same pathname produces a fresh Cargo root and leaves the
// old registered root as inactive owned storage.
func cargoRepositoryIdentity(repoPath string) string {
	common := canonicalRepoKey(repoPath)
	if common == "" {
		return ""
	}
	identity, err := generatedIdentity(common)
	if err == nil && identity.Valid {
		return fmt.Sprintf("%s|dev=%d|ino=%d", common, identity.Device, identity.Inode)
	}
	info, err := os.Stat(common)
	if err != nil {
		return common
	}
	return fmt.Sprintf("%s|size=%d|mtime=%d", common, info.Size(), info.ModTime().UnixNano())
}

func computeRepoKey(repoPath string) string {
	abs, err := filepath.Abs(repoPath)
	if err != nil {
		return filepath.Clean(repoPath)
	}
	resolved := abs
	if r, err := filepath.EvalSymlinks(abs); err == nil {
		resolved = r
	}
	out, err := runGit(resolved, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		// Older git without --path-format: the toplevel is an equivalent
		// identity for everything except linked worktrees.
		out, err = runGit(resolved, "rev-parse", "--show-toplevel")
		if err != nil {
			return resolved
		}
	}
	dir := strings.TrimSpace(out)
	if dir == "" {
		return resolved
	}
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(resolved, dir)
	}
	if r, err := filepath.EvalSymlinks(dir); err == nil {
		dir = r
	}
	return filepath.Clean(dir)
}
