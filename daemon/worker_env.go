package main

// The ONE environment every workflow worker and verification command runs
// with. Every runtime (claude/codex/muse), the verify/setup runner, the
// janitor, screens, preview spawns and the release train all call workerEnv
// or workerEnvForRepo — so this file is the single place where "what a
// worker may see" is decided. Repository-aware callers additionally receive
// the shared per-repository Cargo target directory.
//
// Secret-consuming runtimes build their process environment through
// headlessEnv(req, rt), which starts here and appends only the runtime's
// declared names, resolved late from the dataDir store. This allowlist never
// gains credential-shaped entries; the keep-list audit rule below is unchanged.

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// workerEnvKeepPosix is the POSIX keep-list from the accepted plan §18
// allowlist, including WSL_DISTRO_NAME (public process-startup identity): HOME covers claude/codex/muse config, PATH covers
// toolchains and Go policy flags keep verification from cold-starting. Cache
// paths are set explicitly below so daemon overrides cannot create duplicates.
var workerEnvKeepPosix = []string{
	"HOME", "USER", "LOGNAME", "SHELL", "LANG", "LC_ALL", "LC_CTYPE",
	"TERM", "TMPDIR", "TZ",
	// wslpath otherwise queries WSL init and can hang in detached release jobs.
	// Do not inherit WSLENV forwarding rules or stale WSL_INTEROP sockets.
	"WSL_DISTRO_NAME",
	"XDG_RUNTIME_DIR", "XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME",
	"GOPATH", "GOFLAGS", "GOTOOLCHAIN",
	"NODE_OPTIONS", "NVM_DIR",
}

// workerEnvKeepWindows is the Windows-only addition. A Windows child handed
// PATH alone dies before its first byte — observed 2026-08-18 on a real
// Windows 11 box: claude.exe with a cleared environment plus PATH exited
// 0xC0000409 (STATUS_STACK_BUFFER_OVERRUN) in ~100ms having written ZERO
// bytes to stdout and stderr, while the identical spawn with these variables
// streamed stream-json normally. Every workflow worker on Windows therefore
// died instantly and silently (user report: daemon 0.0.1010, task wft_88ab722b).
//
// AUDIT RULE for adding to this list: a variable is eligible only if it names
// a system/user LOCATION or states a process-startup fact. This stays an
// ALLOWLIST, so anything that can carry a credential — ANTHROPIC_*, *_TOKEN,
// *_KEY, CLAUDE_CODE_OAUTH_TOKEN, SOLU_*, GITHUB_TOKEN, NPM_TOKEN, AWS_* —
// is excluded by construction and must never be added here. Windows
// environment lookups are case-insensitive, so canonical casing is fine.
var workerEnvKeepWindows = []string{
	// Process-critical: the CRT, WinSxS activation contexts and the cmd
	// shims that npm-installed CLIs are cannot start without these.
	"SystemRoot", "windir", "SystemDrive", "ComSpec", "PATHEXT", "TEMP", "TMP",
	"OS", "NUMBER_OF_PROCESSORS", "PROCESSOR_ARCHITECTURE", "PROCESSOR_ARCHITEW6432",
	// Per-user locations: node's os.homedir()/os.tmpdir(), the npm prefix and
	// cache, and claude's ~/.claude config + credentials all resolve through
	// these. Without them the CLI cannot find its own login.
	"USERPROFILE", "HOMEDRIVE", "HOMEPATH", "APPDATA", "LOCALAPPDATA",
	"USERNAME", "USERDOMAIN", "PUBLIC",
	// Program locations: git-for-windows, node installs and .cmd shims.
	"ProgramFiles", "ProgramFiles(x86)", "ProgramW6432",
	"CommonProgramFiles", "CommonProgramFiles(x86)", "CommonProgramW6432",
	"ProgramData", "ALLUSERSPROFILE",
}

// workerEnvKeepList returns the allowlist for a target OS. The POSIX list is
// the base everywhere (Windows CLIs still honour HOME/LANG/NODE_OPTIONS and
// the Go caches); Windows adds what its process startup genuinely needs.
func workerEnvKeepList(goos string) []string {
	keep := make([]string, 0, len(workerEnvKeepPosix)+len(workerEnvKeepWindows))
	keep = append(keep, workerEnvKeepPosix...)
	if goos == "windows" {
		keep = append(keep, workerEnvKeepWindows...)
	}
	return keep
}

// workerEnvFor is workerEnv with the OS and the environment source
// injected, so the allowlist is testable for a platform the test is not
// running on.
func workerEnvFor(goos string, lookup func(string) (string, bool)) []string {
	env := []string{"PATH=" + workerPath()}
	for _, k := range workerEnvKeepList(goos) {
		if v, ok := lookup(k); ok {
			env = append(env, k+"="+v)
		}
	}
	if dir := sharedGoCacheDir(); dir != "" {
		env = append(env, "GOCACHE="+dir)
	}
	if dir := sharedGoModCacheDir(); dir != "" {
		env = append(env, "GOMODCACHE="+dir)
	}
	if dir := sharedNpmCacheDir(); dir != "" {
		env = append(env, "npm_config_cache="+dir)
	}
	if dir := sharedPlaywrightBrowserDir(); dir != "" {
		env = append(env, "PLAYWRIGHT_BROWSERS_PATH="+dir)
	}
	return withResolvedNodeBin(env, resolveNodeBin)
}

type workerStorageMetadata struct {
	Managed   map[string]string
	Unmanaged map[string]string
	Notices   []string
}

// workerStorageMetadataForRepo is intentionally separate from environment
// strings: API/dry-run callers can explain ownership and refused overrides
// without exposing the daemon's full allowlisted environment.
func workerStorageMetadataForRepo(dir string) workerStorageMetadata {
	metadata := workerStorageMetadata{Managed: map[string]string{}, Unmanaged: map[string]string{}, Notices: generatedStorageNotices()}
	if raw := strings.TrimSpace(os.Getenv("SOLU_WORKFLOW_GOCACHE_DIR")); raw != "" && raw != "off" && filepath.IsAbs(raw) && !generatedUnsafeRoot(raw) {
		metadata.Unmanaged["GOCACHE"] = filepath.Clean(raw)
	}
	managedPath := func(category, identity string) string {
		resolved, err := generatedResolve(category, identity, false)
		if err != nil || !resolved.Managed {
			return ""
		}
		return resolved.Path
	}
	for _, item := range []struct{ key, value string }{
		{"GOCACHE", managedPath(storageGoBuild, "")},
		{"GOMODCACHE", managedPath(storageGoMod, "")},
		{"npm_config_cache", managedPath(storageNPM, "")},
		{"PLAYWRIGHT_BROWSERS_PATH", managedPath(storagePlaywright, "")},
		{"CARGO_TARGET_DIR", managedPath(storageCargo, cargoRepositoryIdentity(dir))},
	} {
		if item.value != "" {
			if _, external := metadata.Unmanaged[item.key]; external {
				continue
			}
			metadata.Managed[item.key] = item.value
		}
	}
	return metadata
}

// workerEnv is the ALLOWLISTED environment for workflow workers and
// verification commands. Workers must never inherit unrelated secrets from
// the daemon's environment (accepted plan §18) — only what CLIs and build
// tools genuinely need. A shared npm content cache avoids repeated
// OOM-adjacent refetch work across worktrees.
func workerEnv() []string { return workerEnvFor(runtime.GOOS, os.LookupEnv) }

// workerEnvOptions carries per-invocation environment overrides resolved by
// the engine. GoCacheDir is an already-created daemon-owned private Go build
// cache; empty means the canonical shared cache.
type workerEnvOptions struct {
	GoCacheDir string
}

// workerEnvForRepo adds the only repository-specific build cache. Cargo's
// own flocking makes one target directory safe across parallel worktrees. The
// variadic options keep every existing caller source-compatible; an explicit
// GoCacheDir replaces the canonical shared GOCACHE in place.
func workerEnvForRepo(dir string, opts ...workerEnvOptions) []string {
	env := workerEnv()
	if strings.TrimSpace(dir) != "" {
		if target := sharedCargoTargetDir(dir); target != "" {
			env = append(env, "CARGO_TARGET_DIR="+target)
		}
	}
	if len(opts) > 0 && strings.TrimSpace(opts[0].GoCacheDir) != "" {
		env = setEnvEntry(env, "GOCACHE", opts[0].GoCacheDir)
	}
	return env
}

// setEnvEntry rewrites the first key= entry and never emits a duplicate.
func setEnvEntry(env []string, key, value string) []string {
	prefix := key + "="
	out := make([]string, 0, len(env)+1)
	replaced := false
	for _, entry := range env {
		if strings.HasPrefix(entry, prefix) {
			if replaced {
				continue
			}
			out = append(out, prefix+value)
			replaced = true
			continue
		}
		out = append(out, entry)
	}
	if !replaced {
		out = append(out, prefix+value)
	}
	return out
}

// workerPath retains the daemon's service PATH and the established generic
// user-tool directories. Node version-manager selection is deliberately left
// to resolveNodeBin so worker and verification spawns use one detection path.
func workerPath() string {
	var parts []string
	if servicePath := os.Getenv("PATH"); servicePath != "" {
		parts = append(parts, servicePath)
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		for _, dir := range []string{
			filepath.Join(home, ".local", "bin"),
			filepath.Join(home, ".asdf", "shims"),
		} {
			if isExistingDir(dir) {
				parts = append(parts, dir)
			}
		}
	}
	return strings.Join(parts, string(os.PathListSeparator))
}

func isExistingDir(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}
