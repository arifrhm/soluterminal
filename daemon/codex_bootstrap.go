package main

// Codex CLI bootstrap: detect whether the `codex` binary exists in the
// environment panes actually run in, and whether it is signed in, then surface
// that on GET /api/health so the setup checklist can guide install + sign-in
// instead of letting a Codex pane dead-end with "command not found".
//
// Deliberate parallel of claude_bootstrap.go / muse_bootstrap.go (three-way
// probe outcome, cached verdict, absence hysteresis, login-shell detection,
// local-only recheck and install-command endpoints), kept as a copy per the
// repo's per-provider convention. Codex-specific differences:
//   - Windows resolution goes through resolveProviderBin, which reads the LIVE
//     registry PATH and Codex's own install folder
//     (%LOCALAPPDATA%\Programs\OpenAI\Codex\bin), so a fresh install is found
//     without a daemon restart.
//   - Sign-in is detected WITHOUT reading the login: the live auth.json is only
//     stat'ed, and when it is absent (Codex can keep its login in the OS
//     credential store instead of a file) we ask Codex itself via
//     `codex login status`, using only its exit code. Its output — which can
//     include a masked API key — is discarded, never read or logged.

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"soluterminal-shared/providers"
)

// codexCLIStatus is the cached result of a Codex CLI probe.
type codexCLIStatus struct {
	checked   bool   // false until the first DEFINITIVE probe completes (UI shows "checking…")
	installed bool   // a runnable `codex` resolved in the pane environment
	version   string // first line of `codex --version`, "" if unknown
	authed    bool   // an auth.json exists, or `codex login status` exits 0
}

type codexProbeOutcome int

const (
	codexProbeFound  codexProbeOutcome = iota // codex resolved (version best-effort)
	codexProbeAbsent                          // the probe COMPLETED and found no codex
	codexProbeError                           // timeout / exec failure — no verdict, keep prior state
)

// codexProbeResult is what one detection pass reports. loginOK is only
// meaningful when the probe was asked to run `codex login status`.
type codexProbeResult struct {
	outcome codexProbeOutcome
	version string
	loginOK bool
}

// Same cadence and hysteresis shape as Claude/Muse: brisk while missing (a
// fresh install in a pane heals within a minute), brisker while an absence is
// suspected, relaxed once installed is confirmed.
const (
	codexProbeIntervalMissing = 60 * time.Second
	codexProbeIntervalSuspect = 30 * time.Second
	codexProbeIntervalPresent = 5 * time.Minute

	codexAbsentConfirmProbes = 3
	codexAbsentConfirmWindow = 2 * time.Minute
)

const (
	codexVersionMarker = "<<<SOLU_CODEX_VERSION>>>"
	codexAuthedMarker  = "<<<SOLU_CODEX_AUTHED>>>"
	codexAbsentMarker  = "<<<SOLU_CODEX_ABSENT>>>"
)

// codexRuntimeID is the provider manifest runtime whose BinarySpec describes
// where codex lives on every platform.
const codexRuntimeID = "codex-headless"

// refreshCodexStatus probes for the Codex CLI + sign-in and stores the result.
// Safe from any goroutine; concurrent callers coalesce onto one probe.
func (a *apiServer) refreshCodexStatus() {
	if !a.codexProbeMu.TryLock() {
		// Wait for the in-flight probe so an on-open recheck reflects it
		// instead of a premature checked:false.
		a.codexProbeMu.Lock()
		a.codexProbeMu.Unlock()
		return
	}
	defer a.codexProbeMu.Unlock()
	prev := a.currentCodexStatus()
	fileAuthed := a.codexAuthFileExists()
	detect := a.detectCodex
	if detect == nil {
		detect = detectCodexCLI
	}
	// Only ask Codex for its login status when the cheap file check misses:
	// that is exactly the credential-store case the file check cannot see.
	res := detect(!fileAuthed)

	next := prev
	switch res.outcome {
	case codexProbeFound:
		a.resetCodexAbsentHysteresis()
		next.checked, next.installed = true, true
		if res.version != "" {
			next.version = res.version
		}
		if prev.checked && !prev.installed {
			slog.Info("codex-detect: codex CLI detected", "version", next.version)
		}
	case codexProbeAbsent:
		if !a.confirmCodexAbsent(prev) {
			return
		}
		next.checked, next.installed, next.version = true, false, ""
	case codexProbeError:
		return
	}
	next.authed = next.installed && (fileAuthed || res.loginOK)
	a.codexStatMu.Lock()
	a.codexStat = next
	a.codexStatMu.Unlock()
}

// codexAuthFileExists reports whether Codex's live auth.json exists. It only
// stats the file: the sign-in tick never needs (or reads) the login itself.
func (a *apiServer) codexAuthFileExists() bool {
	path := ""
	if a.codexLogin != nil {
		path = a.codexLogin.livePath()
	} else {
		path = defaultCodexAuthPath()
	}
	if path == "" {
		return false
	}
	fi, err := os.Stat(path)
	return err == nil && fi.Mode().IsRegular() && fi.Size() > 0
}

func defaultCodexAuthPath() string {
	if dir := os.Getenv("CODEX_HOME"); dir != "" {
		return filepath.Join(dir, "auth.json")
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, ".codex", "auth.json")
}

// confirmCodexAbsent damps only the installed:true → absent transition; a
// first-ever verdict or an already-absent state applies immediately.
func (a *apiServer) confirmCodexAbsent(prev codexCLIStatus) bool {
	if !prev.checked || !prev.installed {
		return true
	}
	now := time.Now
	if a.nowFn != nil {
		now = a.nowFn
	}
	a.codexStatMu.Lock()
	defer a.codexStatMu.Unlock()
	if a.codexAbsentStreak == 0 {
		a.codexFirstAbsentAt = now()
	}
	a.codexAbsentStreak++
	elapsed := now().Sub(a.codexFirstAbsentAt)
	if a.codexAbsentStreak < codexAbsentConfirmProbes || elapsed < codexAbsentConfirmWindow {
		slog.Info("codex-detect: absent probe on a confirmed install — keeping verdict until absence is durable",
			"streak", a.codexAbsentStreak, "elapsed", elapsed.Round(time.Second))
		return false
	}
	slog.Warn("codex-detect: absence confirmed as durable — flipping installed→false",
		"streak", a.codexAbsentStreak, "elapsed", elapsed.Round(time.Second))
	a.codexAbsentStreak, a.codexFirstAbsentAt = 0, time.Time{}
	return true
}

func (a *apiServer) resetCodexAbsentHysteresis() {
	a.codexStatMu.Lock()
	defer a.codexStatMu.Unlock()
	a.codexAbsentStreak, a.codexFirstAbsentAt = 0, time.Time{}
}

func (a *apiServer) codexProbeInterval() time.Duration {
	a.codexStatMu.Lock()
	installed := a.codexStat.installed
	suspect := a.codexAbsentStreak > 0
	a.codexStatMu.Unlock()
	switch {
	case !installed:
		return codexProbeIntervalMissing
	case suspect:
		return codexProbeIntervalSuspect
	default:
		return codexProbeIntervalPresent
	}
}

func (a *apiServer) currentCodexStatus() codexCLIStatus {
	a.codexStatMu.Lock()
	defer a.codexStatMu.Unlock()
	return a.codexStat
}

// detectCodexCLI resolves codex where panes run it. Unix: a login shell with
// the shared init (~/.local/bin — where OpenAI's install.sh puts it — plus
// volta, nvm, asdf and the user's profile), matching pane spawn. Windows: the
// live registry PATH plus Codex's own install folder (resolveProviderBin).
func detectCodexCLI(checkLogin bool) codexProbeResult {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if runtime.GOOS == "windows" {
		return detectCodexWindows(ctx, checkLogin, resolveCodexBin)
	}
	bash, err := exec.LookPath("bash")
	if err != nil {
		if _, e := exec.LookPath("codex"); e == nil {
			return codexProbeResult{outcome: codexProbeFound}
		}
		return codexProbeResult{outcome: corroborateCodexAbsent(codexProbeAbsent, codexBinaryOnDisk)}
	}
	cmd := exec.CommandContext(ctx, bash, "-lc", codexProbeScript(checkLogin))
	cmd.WaitDelay = 2 * time.Second
	out, _ := cmd.Output()
	res := classifyCodexProbeOutput(string(out))
	res.outcome = corroborateCodexAbsent(res.outcome, codexBinaryOnDisk)
	if res.outcome == codexProbeError {
		// The login shell gave no verdict (a profile that exits early or
		// fails). Ask the binary itself when it is on disk, without a shell.
		if direct := detectCodexDirect(ctx, checkLogin); direct.outcome == codexProbeFound {
			return direct
		}
	}
	return res
}

// detectCodexDirect runs a codex found on PATH or in the manifest's home
// folders, with no shell. As in the shell probe, `login status` output is
// discarded (it can hold a masked key); only its exit code counts.
func detectCodexDirect(ctx context.Context, checkLogin bool) codexProbeResult {
	bin, err := exec.LookPath("codex")
	if err != nil {
		home, herr := os.UserHomeDir()
		if herr != nil || home == "" {
			return codexProbeResult{outcome: codexProbeError}
		}
		bin = codexBinaryPathUnderHome(home)
	}
	if bin == "" {
		return codexProbeResult{outcome: codexProbeError}
	}
	out, err := exec.CommandContext(ctx, bin, "--version").Output()
	if err != nil {
		return codexProbeResult{outcome: codexProbeError}
	}
	res := codexProbeResult{outcome: codexProbeFound, version: strings.TrimSpace(strings.SplitN(string(out), "\n", 2)[0])}
	if checkLogin {
		status := exec.CommandContext(ctx, bin, "login", "status")
		status.Stdout, status.Stderr = nil, nil
		res.loginOK = status.Run() == nil
	}
	return res
}

// codexProbeScript makes absence positively observable (a marker from a shell
// that ran to completion) and fences the version from any profile banner.
// `codex login status` runs AFTER the version so a slow status can't cost the
// install verdict; all of its output goes to /dev/null and only its exit code
// becomes a marker. `command` bypasses a shell function named codex (panes
// export one that adds TUI flags).
func codexProbeScript(checkLogin bool) string {
	login := ""
	if checkLogin {
		login = "; printf '\\n'; command codex login status </dev/null >/dev/null 2>&1 && printf '%s' '" + codexAuthedMarker + "'"
	}
	return loginShellInit() +
		"; if command -v codex >/dev/null 2>&1; then printf '%s' '" + codexVersionMarker + "'; command codex --version </dev/null" +
		login +
		"; else printf '%s' '" + codexAbsentMarker + "'; fi"
}

func classifyCodexProbeOutput(out string) codexProbeResult {
	if strings.Contains(out, codexVersionMarker) {
		return codexProbeResult{
			outcome: codexProbeFound,
			version: versionAfterMarker(out, codexVersionMarker),
			loginOK: strings.Contains(out, codexAuthedMarker),
		}
	}
	if strings.Contains(out, codexAbsentMarker) {
		return codexProbeResult{outcome: codexProbeAbsent}
	}
	return codexProbeResult{outcome: codexProbeError}
}

// detectCodexWindows resolves codex.exe through resolve (the live registry
// PATH and Codex's install folder). A full miss is definitive absence; a
// resolved binary whose --version fails is a probe failure, not absence.
func detectCodexWindows(ctx context.Context, checkLogin bool, resolve func() (string, error)) codexProbeResult {
	bin, err := resolve()
	if err != nil || bin == "" {
		return codexProbeResult{outcome: codexProbeAbsent}
	}
	cmd := exec.CommandContext(ctx, bin, "--version")
	applyNoWindow(cmd)
	out, err := cmd.Output()
	if err != nil {
		return codexProbeResult{outcome: codexProbeError}
	}
	res := codexProbeResult{outcome: codexProbeFound, version: firstLine(strings.TrimSpace(string(out)))}
	if checkLogin {
		// Stdout/Stderr stay nil: the status text can name a masked key, and
		// only the exit code is needed.
		status := exec.CommandContext(ctx, bin, "login", "status")
		applyNoWindow(status)
		res.loginOK = status.Run() == nil
	}
	return res
}

// corroborateCodexAbsent downgrades an absent verdict to "no verdict" when a
// codex binary is provably on disk (the probe shell's PATH was under-populated,
// e.g. nvm lost a fork under load). It can only suppress a false absent.
func corroborateCodexAbsent(outcome codexProbeOutcome, onDisk func() bool) codexProbeOutcome {
	if outcome == codexProbeAbsent && onDisk() {
		slog.Info("codex-detect: probe said absent but a codex binary is on disk — keeping prior verdict")
		return codexProbeError
	}
	return outcome
}

func codexBinaryOnDisk() bool {
	if _, err := exec.LookPath("codex"); err == nil {
		return true
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return false
	}
	return codexBinaryUnderHome(home)
}

// codexBinaryUnderHome checks the manifest's home layouts with stat/glob only.
func codexBinaryUnderHome(home string) bool {
	return codexBinaryPathUnderHome(home) != ""
}

// codexBinaryPathUnderHome returns the first codex binary in the manifest's
// home layouts, or "".
func codexBinaryPathUnderHome(home string) string {
	_, _, facts, ok := providers.ByRuntimeID(codexRuntimeID)
	if !ok || facts.Binary == nil {
		return ""
	}
	spec := facts.Binary
	for _, sub := range spec.HomeSubdirs {
		if p := filepath.Join(home, filepath.FromSlash(sub), spec.Name); isExistingFile(p) {
			return p
		}
	}
	for _, glob := range spec.HomeGlobs {
		matches, _ := filepath.Glob(filepath.Join(home, filepath.FromSlash(glob), spec.Name))
		for _, m := range matches {
			if isExistingFile(m) {
				return m
			}
		}
	}
	return ""
}

// handleCodexRecheck re-probes Codex on demand (the setup checklist and the
// post-install poll). LOCAL-ONLY: probing spawns a shell on the host.
func (a *apiServer) handleCodexRecheck(w http.ResponseWriter, r *http.Request) {
	if pairRequestOverRelay(r) {
		http.Error(w, "codex re-check is local-only", http.StatusForbidden)
		return
	}
	a.refreshCodexStatus()
	st := a.currentCodexStatus()
	writeJSON(w, 200, map[string]any{
		"codexChecked":   st.checked,
		"codexInstalled": st.installed,
		"codexVersion":   st.version,
		"codexAuthed":    st.authed,
	})
}

// codexInstallCommandSpec has the same shape as the Claude spec so the web
// reuses one confirm card.
type codexInstallCommandSpec = claudeInstallCommandSpec

const (
	codexInstallDisplayUnix    = "curl -fsSL https://chatgpt.com/codex/install.sh | sh"
	codexInstallDisplayWindows = `powershell -ExecutionPolicy ByPass -c "irm https://chatgpt.com/codex/install.ps1 | iex"`
)

// codexInstallCommandFor builds OpenAI's official standalone installer
// invocation (no Node, no admin). CODEX_NON_INTERACTIVE skips the installer's
// closing "Start Codex now? [y/N]" question: SoluTerminal offers sign-in as its
// own step, and a hidden y/N prompt would stall the pane. The pane echoes the
// command and its source first, so the pane itself is the disclosure.
func codexInstallCommandFor(goos string) codexInstallCommandSpec {
	if goos == "windows" {
		ps := "Write-Host 'SoluTerminal will run the official Codex installer from OpenAI:'; " +
			"Write-Host '  irm https://chatgpt.com/codex/install.ps1 | iex'; Write-Host ''; " +
			"$env:CODEX_NON_INTERACTIVE = '1'; " +
			"try { irm https://chatgpt.com/codex/install.ps1 | iex } " +
			"catch { Write-Host ('The install did not finish: ' + $_.Exception.Message) }; " +
			"$codex = Get-Command codex -ErrorAction SilentlyContinue; " +
			"$known = Join-Path $env:LOCALAPPDATA 'Programs\\OpenAI\\Codex\\bin\\codex.exe'; " +
			"if (-not $codex -and (Test-Path $known)) { $codex = Get-Item $known }; " +
			"Write-Host ''; " +
			"if ($codex) { Write-Host 'Codex is installed. You can close this pane. Open a new pane to use Codex.' } " +
			"else { Write-Host 'The install did not finish. Security software can block it. See the messages above, then try again.' }"
		return codexInstallCommandSpec{
			Supported: true,
			Cmd:       []string{"powershell.exe", "-NoExit", "-NoProfile", "-ExecutionPolicy", "Bypass", "-Command", ps},
			Display:   codexInstallDisplayWindows,
			Note:      "Runs OpenAI's official Codex installer in a new pane. No administrator rights needed.",
		}
	}
	script := loginShellInit() + "; " +
		"echo 'SoluTerminal will run the official Codex installer from OpenAI:'; " +
		"echo '  " + codexInstallDisplayUnix + "'; echo; " +
		"export CODEX_NON_INTERACTIVE=1; " +
		codexInstallDisplayUnix + "; echo; " +
		// Verify by resolving codex (PATH includes ~/.local/bin via the init),
		// not the pipe's exit: `curl … | sh` returns 0 even when curl failed.
		"if command -v codex >/dev/null 2>&1; then echo 'Codex is installed. You can close this pane. Open a new pane to use Codex.'; " +
		"else echo 'The install did not finish. See the messages above, then try again.'; fi; " +
		"exec bash -i"
	return codexInstallCommandSpec{
		Supported: true,
		Cmd:       []string{"bash", "-lc", script},
		Display:   codexInstallDisplayUnix,
		Note:      "Runs OpenAI's official Codex installer in a new pane. No administrator rights needed.",
	}
}

// handleCodexInstallCommand returns the installer command for the web to spawn
// in a pane. LOCAL-ONLY (403 over the relay): installing is a host action.
func (a *apiServer) handleCodexInstallCommand(w http.ResponseWriter, r *http.Request) {
	if pairRequestOverRelay(r) {
		http.Error(w, "codex install is local-only", http.StatusForbidden)
		return
	}
	writeJSON(w, 200, codexInstallCommandFor(runtime.GOOS))
}
