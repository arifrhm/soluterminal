package main

import (
	"bufio"

	"os"
	"os/exec"
	"path/filepath"
	"runtime"

	"strconv"
	"strings"
)

// memAvailableKB reads the kernel's best estimate of memory immediately
// available without swapping. Unknown readings fail open: resource governance
// may delay heavy work, but a missing platform signal must never deny it.
func memAvailableKB() (int64, bool) {
	if runtime.GOOS != "linux" {
		return 0, false
	}
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0, false
	}
	defer f.Close()
	s := bufio.NewScanner(f)
	for s.Scan() {
		fields := strings.Fields(s.Text())
		if len(fields) < 2 || fields[0] != "MemAvailable:" {
			continue
		}
		kb, err := strconv.ParseInt(fields[1], 10, 64)
		if err != nil || kb < 0 {
			return 0, false
		}
		return kb, true
	}
	return 0, false
}

// readMemTotalKB reads the kernel's total physical-memory estimate. Unknown
// readings retain the static resource defaults.
func readMemTotalKB() (int64, bool) {
	if runtime.GOOS != "linux" {
		return 0, false
	}
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0, false
	}
	defer f.Close()
	s := bufio.NewScanner(f)
	for s.Scan() {
		fields := strings.Fields(s.Text())
		if len(fields) < 2 || fields[0] != "MemTotal:" {
			continue
		}
		kb, err := strconv.ParseInt(fields[1], 10, 64)
		if err != nil || kb < 0 {
			return 0, false
		}
		return kb, true
	}
	return 0, false
}

// memTotalProbe is a test seam for proving that engine construction, not
// only the pure derivation helpers, honors adaptive machine-size defaults.
var memTotalProbe = readMemTotalKB

// goExecutable resolves Go against the allowlisted worker PATH and common
// install roots. Workflow commands may add a repository-specific Go path in
// their own shell, so the daemon's service PATH alone is not authoritative.
func goExecutable() (string, bool) {
	var dirs []string
	dirs = append(dirs, filepath.SplitList(workerPath())...)
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		dirs = append(dirs,
			filepath.Join(home, "go-sdk", "go", "bin"),
			filepath.Join(home, "go", "bin"),
		)
	}
	dirs = append(dirs, "/usr/local/go/bin")
	if matches, err := filepath.Glob("/usr/lib/go*/bin"); err == nil {
		dirs = append(dirs, matches...)
	}
	seen := make(map[string]struct{}, len(dirs))
	for _, dir := range dirs {
		if strings.TrimSpace(dir) == "" {
			continue
		}
		if _, ok := seen[dir]; ok {
			continue
		}
		seen[dir] = struct{}{}
		path, err := exec.LookPath(filepath.Join(dir, "go"))
		if err == nil {
			return path, true
		}
	}
	return "", false
}

// goCacheDir resolves only the ownership-proven workflow Go cache. Direct
// user caches remain outside recursive measurement and lifecycle cleanup.
func goCacheDir() (string, bool) {
	if raw := strings.TrimSpace(os.Getenv("SOLU_WORKFLOW_GOCACHE_DIR")); raw != "" && raw != "off" && filepath.IsAbs(raw) && !generatedUnsafeRoot(raw) {
		// The explicit operator override is unowned. Workers may use it, but
		// the janitor must never measure or delete it.
		return "", false
	}
	resolved, err := generatedResolve(storageGoBuild, "", true)
	if err == nil && resolved.Managed && resolved.Path != "" {
		return resolved.Path, true
	}
	// Allocation or registry failure must not silently make the janitor fall
	// through to the user's ordinary Go cache.
	return "", false
}

// xdgCacheRootPath resolves the user cache root without creating it. Storage
// observability reuses this so a read-only scan shares the exact producer path
// derivation without materializing directories.
func xdgCacheRootPath() string {
	if root := strings.TrimSpace(os.Getenv("XDG_CACHE_HOME")); root != "" {
		return root
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, ".cache")
}

// sharedGoCacheDirPath reports the legacy cache without creating/adopting it.
// Explicit external overrides are not walked. New producers use registry-owned
// paths, inventoried separately.
func sharedGoCacheDirPath() (string, bool) {
	root := xdgCacheRootPath()
	if root == "" {
		return "", false
	}
	return filepath.Join(root, "soluterminal", "go-build"), true
}

// sharedGoModCacheDirPath reports only the legacy, unowned module cache.
func sharedGoModCacheDirPath() (string, bool) {
	root := xdgCacheRootPath()
	if root == "" {
		return "", false
	}
	return filepath.Join(root, "soluterminal", "go-mod"), true
}

// sharedCargoTargetRootPath reports the legacy, unowned Cargo target parent.
func sharedCargoTargetRootPath() (string, bool) {
	root := xdgCacheRootPath()
	if root == "" {
		return "", false
	}
	return filepath.Join(root, "soluterminal", "cargo-target"), true
}

// sharedNpmCacheDirPath reports the legacy, unowned npm cache.
func sharedNpmCacheDirPath() (string, bool) {
	root := xdgCacheRootPath()
	if root == "" {
		return "", false
	}
	return filepath.Join(root, "soluterminal", "npm-cache"), true
}

// sharedGoCacheDir returns one machine-level Go build cache. Go's build
// cache is content-addressed and uses its own file locking, so concurrent
// builds in separate worktrees can safely share it — the same concurrency
// argument sharedCargoTargetDir and sharedNpmCacheDir rely on.
//
// The ambient GOCACHE is deliberately NOT consulted. During the 2026-09-26 WSL
// disk incident the daemon inherited a per-invocation absolute GOCACHE and every
// workflow launch created its own multi-GiB /tmp/*-go-cache tree that nothing
// reclaimed until the root filesystem hit 100%. Every workflow launch now
// shares exactly one bounded cache; a task that genuinely needs isolation gets
// a private cache only through isolatedGoCacheDir.
func sharedGoCacheDir() string {
	if raw := strings.TrimSpace(os.Getenv("SOLU_WORKFLOW_GOCACHE_DIR")); raw != "" && raw != "off" {
		// A worktree-relative override would create a .gocache/ tree inside the
		// task worktree, which the engine's checkpoint commits would then
		// stage and the --ff-only landing would push verbatim. Only honor an
		// absolute path; anything else falls through to the shared default.
		if filepath.IsAbs(raw) && !generatedUnsafeRoot(raw) {
			if err := os.MkdirAll(raw, 0o755); err != nil {
				return ""
			}
			return raw
		}
	}
	resolved, err := generatedResolve(storageGoBuild, "", true)
	if err != nil {
		return ""
	}
	return resolved.Path
}

// isolatedGoCacheDir is the ONLY way a workflow task obtains a private Go
// build cache. It is reached exclusively through an explicit per-task
// isolation opt-in, so the shared cache stays the default. The key is
// sanitized to [A-Za-z0-9_.-] (empty becomes "anon") so a task id can never
// steer the directory outside os.TempDir.
func isolatedGoCacheDir(key string) (string, bool) {
	key = strings.Map(func(r rune) rune {
		switch {
		case r >= 'A' && r <= 'Z', r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '_', r == '.', r == '-':
			return r
		default:
			return '-'
		}
	}, key)
	if key == "" {
		key = "anon"
	}
	dir, err := os.MkdirTemp(os.TempDir(), "solu-wf-gocache-"+key+"-")
	if err != nil {
		return "", false
	}
	return dir, true
}

// sharedGoModCacheDir returns one machine-level module cache. The Go
// command coordinates concurrent module extraction with per-module lockfiles.
//
// Decision recorded for the 2026-09-26 remediation: acceptance criterion 2
// requires the ambient per-task GOMODCACHE to be IGNORED, so this deliberately
// diverges from the implementation-spec sentence that proposed honoring an
// absolute ambient GOMODCACHE. Honoring a per-invocation pin would let an
// inherited worker value recreate exactly the per-task tree the incident
// produced, on the root volume the fix is meant to protect. The module cache is
// therefore always the canonical $XDG_CACHE_HOME/soluterminal/go-mod location;
// an operator who needs a different volume relocates the whole cache root via
// XDG_CACHE_HOME (or SOLU_WORKFLOW_GOCACHE_DIR for the build cache) rather than
// pinning GOMODCACHE per invocation. A relative SOLU_WORKFLOW_GOCACHE_DIR is
// likewise rejected by sharedGoCacheDir.
func sharedGoModCacheDir() string {
	resolved, err := generatedResolve(storageGoMod, "", true)
	if err != nil {
		return ""
	}
	return resolved.Path
}

// sharedCargoTargetDir returns one target directory per repository. Cargo
// flocks a shared target directory, so parallel worktrees serialize conflicting
// writes instead of corrupting artifacts while unrelated repositories remain
// independent.
func sharedCargoTargetDir(dir string) string {
	key := cargoRepositoryIdentity(dir)
	if strings.TrimSpace(key) == "" {
		return ""
	}
	resolved, err := generatedResolve(storageCargo, key, true)
	if err != nil {
		return ""
	}
	return resolved.Path
}

// sharedNpmCacheDir returns one machine-level npm content cache. npm's
// cacache is concurrency-safe. Project install trees (node_modules) stay in
// each worktree because concurrent npm installs into one project tree are not
// safe; that is the intentionally retained per-task isolation boundary.
func sharedNpmCacheDir() string {
	resolved, err := generatedResolve(storageNPM, "", true)
	if err != nil {
		return ""
	}
	return resolved.Path
}

func sharedPlaywrightBrowserDir() string {
	resolved, err := generatedResolve(storagePlaywright, "", true)
	if err != nil {
		return ""
	}
	return resolved.Path
}
