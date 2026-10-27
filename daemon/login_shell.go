package main

// One resolution scheme for every daemon-spawned command whose argv[0] is a
// bare TOOLCHAIN name (npm, node, go, …). The release train's publish steps and
// the preview surface's build/serve commands both route through here; nobody
// should grow a second copy of this logic.

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

var (
	loginShellGOOS           = runtime.GOOS
	loginShellLookPath       = exec.LookPath
	loginShellResolveNodeBin = resolveNodeBin
	loginShellCommandOutput  = func(ctx context.Context, argv []string) ([]byte, error) {
		cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
		// CommandContext only kills the direct child. A login profile can leave
		// a grandchild holding stdout open, so bound the pipe drain as well.
		cmd.WaitDelay = 2 * time.Second
		return cmd.Output()
	}
)

const loginShellCommandTimeout = 5 * time.Second

// loginShellCommandPath resolves a bare tool name in the same login-shell
// environment pane and toolchain spawns use. The shell may print startup
// banners, so command -v's last non-empty output line is the only candidate.
// Resolution never starts the runtime binary itself.
func loginShellCommandPath(tool string) (string, error) {
	if loginShellGOOS == "windows" {
		return "", fmt.Errorf("login-shell resolution is unavailable on windows")
	}
	if _, err := loginShellLookPath("bash"); err != nil {
		return "", fmt.Errorf("login-shell resolution requires bash: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), loginShellCommandTimeout)
	defer cancel()
	script := loginShellInit() + "; command -v -- " + shellSingleQuote(tool)
	out, err := loginShellCommandOutput(ctx, []string{"bash", "-lc", script})
	if err != nil {
		return "", fmt.Errorf("login-shell resolution for %s failed: %w", tool, err)
	}
	lines := strings.Split(string(out), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		path := strings.TrimSpace(lines[i])
		if path == "" {
			continue
		}
		if !filepath.IsAbs(path) {
			return "", fmt.Errorf("login shell resolved %s to non-absolute path %q", tool, path)
		}
		if !isExistingFile(path) {
			return "", fmt.Errorf("login shell resolved %s to missing path %q", tool, path)
		}
		return path, nil
	}
	return "", fmt.Errorf("login shell did not resolve %s", tool)
}

func nodeSiblingTool(tool string, resolveNode func() (string, error)) (string, string, error) {
	nodeBin, err := resolveNode()
	if err != nil {
		return "", "", err
	}
	candidates := []string{filepath.Join(filepath.Dir(nodeBin), tool)}
	if loginShellGOOS == "windows" {
		candidates = append([]string{filepath.Join(filepath.Dir(nodeBin), tool+".cmd")}, candidates...)
	}
	for _, candidate := range candidates {
		if info, statErr := os.Stat(candidate); statErr == nil && !info.IsDir() {
			return candidate, nodeBin, nil
		}
	}
	return "", nodeBin, fmt.Errorf("%s missing beside node", tool)
}

func resolveNPMBin() (string, error) {
	nodeBin, err := resolveNodeBin()
	if err != nil {
		return "", fmt.Errorf("npm unavailable: %v; install Node.js with npm", err)
	}
	if npmBin, _, siblingErr := nodeSiblingTool("npm", func() (string, error) { return nodeBin, nil }); siblingErr == nil {
		return npmBin, nil
	} else if pathBin, pathErr := exec.LookPath("npm"); pathErr == nil {
		return pathBin, nil
	}
	return "", fmt.Errorf("node resolved at %s but npm is missing beside it and not found on PATH; install npm with Node.js", nodeBin)
}

func loginShellFallbackArgv(argv []string) []string {
	if len(argv) == 0 || (argv[0] != "npm" && argv[0] != "npx") {
		return argv
	}
	tool, _, err := nodeSiblingTool(argv[0], loginShellResolveNodeBin)
	if err != nil {
		return argv
	}
	resolved := append([]string(nil), argv...)
	resolved[0] = tool
	return resolved
}

// loginShellExecArgv wraps a shell script in a login-shell child so the
// command names inside it are resolved by the CHILD's environment, the way an
// interactive pane or a verify row resolves them.
//
// The bug it exists for: Go's exec.Command resolves a bare argv[0] against the
// DAEMON process's os.Getenv("PATH") — never against cmd.Env — so a command
// declared as {"npm", "ci"} runs whichever npm the daemon's own PATH names. The
// daemon's service environment has no nvm, so on a plain Linux box the lookup
// simply fails ("exec: \"npm\": executable file not found in $PATH"); on WSL it
// fails worse, because that PATH carries the Windows interop entries and the
// step exec's WINDOWS npm against UNC paths. The first autonomous release cut
// (rtr_08b7f788) died that way — cache under C:\Users\…\npm-cache, the vendored
// `file:` dependency resolved with the distro segment DOUBLED, node_modules
// cleanup failing with UNC EPERM/ENOENT — and preview acceptance degraded to
// "unavailable: exec npm" on every task for the same reason. Verify rows never
// hit this because their argv[0] is bash: the tool lookup happens inside the
// child, against cmd.Env's PATH. This helper gives other callers that shape.
//
// `bash -lc` ALONE is not enough: Ubuntu's ~/.bashrc returns early when
// non-interactive, so nvm is never sourced (already documented in
// tryLoginShellWrap, pane_spawn.go). Hence the shared loginShellInit prelude,
// which sources nvm/asdf/pyenv and prepends ~/.local/bin.
//
// When goExecutable resolves a real toolchain, its bin dir is prepended AFTER
// the prelude so it outranks any `go` shim sitting in a version manager's bin
// dir — mirroring ~/.bashrc, which prepends $GOROOT/bin once nvm has loaded.
// The env the caller already carries (workerEnv: npm_config_cache, GOFLAGS,
// resolved node bin) survives untouched — the prelude only PREPENDS to PATH.
func loginShellExecArgv(script string) []string {
	prelude := loginShellInit()
	if goBin, ok := goExecutable(); ok {
		prelude += "; export PATH=" + shellSingleQuote(filepath.Dir(goBin)) + `:"$PATH"`
	}
	return []string{"bash", "-lc", prelude + "; exec " + script}
}

// loginShellWrapArgv is the argv-shaped adapter over loginShellExecArgv, for
// callers that hold a command as []string rather than a script.
//
// CALLER CONTRACT: validate the RAW argv (validatePreviewArgv) BEFORE wrapping.
// The wrapped form is deliberately a `bash -lc <script>` shell string, which
// validatePreviewArgv (workflow_repo_config.go) rejects by design — validation
// must see what the repository configured, not what the daemon execs.
//
// The argv is returned unchanged when there is nothing to resolve. In the two
// no-bash fallthroughs, npm/npx are first replaced with an absolute executable
// beside resolveNodeBin when one exists:
//   - empty argv — nothing to run;
//   - GOOS=windows — no bash to wrap with;
//   - argv[0] contains a path separator — an explicit path never consults PATH;
//   - bash is not on PATH — mirroring tryLoginShellWrap's fallback for other tools.
//
// The script ends in `exec`, so bash replaces itself with the real command: the
// spawned pid IS the tool's pid, leaving process-identity and tree-kill
// semantics (previewProcessStartIdentity, previewStopTree) unchanged.
func loginShellWrapArgv(argv []string) []string {
	if len(argv) == 0 {
		return argv
	}
	if loginShellGOOS == "windows" {
		return loginShellFallbackArgv(argv)
	}
	if strings.ContainsAny(argv[0], `/\`) {
		return argv
	}
	if _, err := loginShellLookPath("bash"); err != nil {
		return loginShellFallbackArgv(argv)
	}
	parts := make([]string, len(argv))
	for i, arg := range argv {
		parts[i] = shellSingleQuote(arg)
	}
	return loginShellExecArgv(strings.Join(parts, " "))
}
