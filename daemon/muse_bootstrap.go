package main

// Muse Code CLI bootstrap: detect whether the `muse` binary exists in the
// environment panes actually run in, and whether it's authenticated, then
// surface that on GET /api/health so the web UI can guide install + auth
// instead of letting a Muse pane dead-end with "command not found".
//
// Deliberate parallel of claude_bootstrap.go (same three-way outcome, cached
// probe, absence hysteresis, login-shell detection, local-only recheck and
// install-command endpoints). Kept as a copy — not a generic abstraction —
// per the repo's per-provider convention (docs/team-creds/MUSE.md §1): the
// two differences from Claude are (1) no MCP/hook re-registration on a
// post-boot install (Muse has no MCP client or config hook surface), and
// (2) auth is the live auth.json envelope only — there is no macOS Keychain
// caveat because Muse keeps its login in a file on every platform.

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
)

// museStatus is the cached result of a Muse CLI probe. Unexported fields;
// the health handler reads it via apiServer.currentMuseStatus().
type museStatus struct {
	checked   bool   // false until the first DEFINITIVE probe completes (UI shows "checking…")
	installed bool   // a runnable `muse` resolved in the login-shell env
	version   string // first line of `muse --version`, "" if unknown
	authed    bool   // a valid live auth.json envelope exists, or META_API_KEY is set
}

// museProbeOutcome separates a probe's ANSWER from its FAILURE. A probe that
// times out or whose shell dies has produced no information — treating it as
// "not installed" is how a machine ended up with an "install Muse" banner
// after a loaded restart (the boot probe raced respawning panes and lost).
// Only a definitive outcome may overwrite the cached museStatus.
type museProbeOutcome int

const (
	museProbeFound  museProbeOutcome = iota // `command -v muse` resolved (version best-effort)
	museProbeAbsent                         // the probe shell COMPLETED and said "no muse"
	museProbeError                          // timeout / exec failure — no verdict, keep prior state
)

// Steady-state re-probe cadence. Same shape as Claude: brisk while missing
// (a fresh manual install heals in ~1 min), brisker while an absence is
// SUSPECTED but unconfirmed (hysteresis needs samples), relaxed once
// installed is confirmed (only a later uninstall needs catching).
const (
	museProbeIntervalMissing = 60 * time.Second
	museProbeIntervalSuspect = 30 * time.Second
	museProbeIntervalPresent = 5 * time.Minute
)

// Absence-confirmation hysteresis. A Muse self-update replaces the binary on
// disk, so for a window of seconds `command -v` AND the disk cross-check can
// both legitimately miss — a correctly observed, definitive-looking absence
// that is not an uninstall. A confirmed install flips to "not installed" only
// after this many consecutive definitive absents spanning at least this
// window (any found-probe resets both).
const (
	museAbsentConfirmProbes = 3
	museAbsentConfirmWindow = 2 * time.Minute
)

// refreshMuseStatus probes for the Muse CLI + auth and stores the result.
// Safe to call from any goroutine; serialized by museStatMu. Call at boot,
// after a guided install, on a slow ticker, and on explicit re-check.
func (a *apiServer) refreshMuseStatus() {
	// Coalesce concurrent probes onto one login shell, same as Claude.
	if !a.museProbeMu.TryLock() {
		a.museProbeMu.Lock()
		a.museProbeMu.Unlock()
		return
	}
	defer a.museProbeMu.Unlock()
	prev := a.currentMuseStatus()
	detect := a.detectMuse
	if detect == nil {
		detect = detectMuseCLI
	}
	outcome, version, hasAPIKey := detect()

	// Only a DEFINITIVE outcome may change the cached verdict.
	next := prev
	switch outcome {
	case museProbeFound:
		a.resetMuseAbsentHysteresis()
		next.checked, next.installed = true, true
		// Presence is proven by `command -v`; the version is best-effort —
		// keep the last known one rather than blanking the UI.
		if version != "" {
			next.version = version
		}
		if prev.checked && !prev.installed {
			slog.Info("muse-detect: muse CLI detected", "version", next.version)
		}
	case museProbeAbsent:
		if !a.confirmMuseAbsent(prev) {
			// Absence observed but not yet DURABLE — keep the prior verdict
			// and let the brisk suspect-cadence ticker converge.
			return
		}
		next.checked, next.installed, next.version = true, false, ""
	case museProbeError:
		// No verdict: keep prev wholesale (authed included — a failed probe
		// can't see the rc-file META_API_KEY, so recomputing here would flap
		// an API-key user's authed state).
		return
	}
	credPresent := a.museLogin != nil && a.museLogin.LiveAuthPresent()
	next.authed = resolveMuseAuthed(next.installed, credPresent, hasAPIKey)
	a.museStatMu.Lock()
	a.museStat = next
	a.museStatMu.Unlock()
}

// confirmMuseAbsent is the hysteresis gate for a definitive absent probe:
// only the installed:true → absent transition is damped; a first-ever
// verdict or an already-absent state applies immediately.
func (a *apiServer) confirmMuseAbsent(prev museStatus) bool {
	if !prev.checked || !prev.installed {
		return true
	}
	now := time.Now
	if a.nowFn != nil {
		now = a.nowFn
	}
	a.museStatMu.Lock()
	defer a.museStatMu.Unlock()
	if a.museAbsentStreak == 0 {
		a.museFirstAbsentAt = now()
	}
	a.museAbsentStreak++
	elapsed := now().Sub(a.museFirstAbsentAt)
	if a.museAbsentStreak < museAbsentConfirmProbes || elapsed < museAbsentConfirmWindow {
		slog.Info("muse-detect: absent probe on a confirmed install — keeping verdict until absence is durable",
			"streak", a.museAbsentStreak, "needProbes", museAbsentConfirmProbes,
			"elapsed", elapsed.Round(time.Second), "needWindow", museAbsentConfirmWindow)
		return false
	}
	slog.Warn("muse-detect: absence confirmed as durable — flipping installed→false",
		"streak", a.museAbsentStreak, "elapsed", elapsed.Round(time.Second))
	a.museAbsentStreak, a.museFirstAbsentAt = 0, time.Time{}
	return true
}

// resetMuseAbsentHysteresis ends a suspected-absent episode on a found probe.
func (a *apiServer) resetMuseAbsentHysteresis() {
	a.museStatMu.Lock()
	defer a.museStatMu.Unlock()
	if a.museAbsentStreak > 0 {
		slog.Info("muse-detect: found probe ended a suspected-absent episode (transient absence, likely a self-update window)",
			"streak", a.museAbsentStreak)
		a.museAbsentStreak, a.museFirstAbsentAt = 0, time.Time{}
	}
}

// museProbeInterval picks the self-heal ticker cadence from the cached verdict.
func (a *apiServer) museProbeInterval() time.Duration {
	a.museStatMu.Lock()
	installed := a.museStat.installed
	suspect := a.museAbsentStreak > 0
	a.museStatMu.Unlock()
	switch {
	case !installed:
		return museProbeIntervalMissing
	case suspect:
		return museProbeIntervalSuspect
	default:
		return museProbeIntervalPresent
	}
}

// currentMuseStatus returns the last cached probe result (zero value, with
// checked=false, before the first probe completes).
func (a *apiServer) currentMuseStatus() museStatus {
	a.museStatMu.Lock()
	defer a.museStatMu.Unlock()
	return a.museStat
}

// resolveMuseAuthed decides the "signed in" signal the UI's sign-in nudge
// rides on. Authed when a valid live auth.json envelope exists, and ALSO when
// META_API_KEY is present in the pane environment: an API-key user
// authenticates without ever writing a credential file (and the key outranks
// the stored login), so without this they'd be nudged to sign in forever.
// Unlike Claude there is no macOS Keychain caveat — Muse keeps its login in
// auth.json on every platform — so no darwin exception.
func resolveMuseAuthed(installed, credPresent, hasAPIKey bool) bool {
	if !installed {
		return false
	}
	return credPresent || hasAPIKey
}

// detectMuseCLI reports whether a runnable `muse` exists (as a three-way
// outcome), its version string, and whether META_API_KEY is present in the
// environment panes run in (API-key auth never writes a credential file).
// Unix: a login shell with the shared init (matches pane spawn). Windows: the
// live registry PATH (matches pane spawn — findOnWindowsPath) plus the
// native installer's %USERPROFILE%\.local\bin.
func detectMuseCLI() (outcome museProbeOutcome, version string, hasAPIKey bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()

	if runtime.GOOS == "windows" {
		outcome, version = detectMuseWindows(ctx)
		// Panes inherit the daemon's env, so the daemon's own env is what a
		// pane's muse will see.
		return outcome, version, os.Getenv("META_API_KEY") != ""
	}

	bash, err := exec.LookPath("bash")
	if err != nil {
		// No bash → fall back to a bare lookup; better than a false negative
		// on an exotic *nix without bash. LookPath is a pure filesystem walk
		// — load-immune — so its miss counts as absent.
		hasAPIKey = os.Getenv("META_API_KEY") != ""
		if p, e := exec.LookPath("muse"); e == nil {
			v := ""
			if out, e2 := exec.CommandContext(ctx, p, "--version").Output(); e2 == nil {
				v = firstLine(strings.TrimSpace(string(out)))
			}
			return museProbeFound, v, hasAPIKey
		}
		return corroborateMuseAbsent(museProbeAbsent, museBinaryOnDisk, museProcessAlive), "", hasAPIKey
	}
	// The probe must make ABSENCE positively observable, not inferred from a
	// failure: `command -v` prints the absent marker in its else-branch, so
	// "no muse" is a marker in stdout from a shell that ran to completion.
	// The API-key probe emits its own marker BEFORE the version fence (a login
	// shell is where rc-file `export META_API_KEY=…` becomes visible — the
	// daemon's own env is a slim launch-time snapshot that may miss it).
	script := loginShellInit() +
		`; [ -n "$META_API_KEY" ] && printf '%s' '` + museAPIKeyMarker + `'` +
		"; if command -v muse >/dev/null 2>&1; then printf '%s' '" + museVersionMarker + "'; muse --version; else printf '%s' '" + museAbsentMarker + "'; fi"
	cmd := exec.CommandContext(ctx, bash, "-lc", script)
	cmd.WaitDelay = 2 * time.Second // close pipes / kill if it lingers past ctx cancel
	out, _ := cmd.Output()
	// Markers are parsed regardless of the exit code; union the key with the
	// daemon's own env for setups that inject it at launch (systemd
	// Environment=) without an rc-file export.
	hasAPIKey = strings.Contains(string(out), museAPIKeyMarker) || os.Getenv("META_API_KEY") != ""
	outcome, version = classifyMuseProbeOutput(string(out))
	// A login shell that RAN TO COMPLETION and reported "absent" is still not
	// trusted blind: cross-check the filesystem (load-immune) and, when the
	// disk ALSO misses, the live process table — and downgrade a contradicted
	// "absent" to a no-verdict ERROR so it can't overwrite a working install.
	outcome = corroborateMuseAbsent(outcome, museBinaryOnDisk, museProcessAlive)
	return outcome, version, hasAPIKey
}

// classifyMuseProbeOutput maps the probe shell's stdout to a three-way
// outcome. Version-marker → found (version best-effort); absent-marker → the
// shell completed and `command -v` said no; neither → the shell never
// reached the probe (killed/timed out) and there is NO verdict.
func classifyMuseProbeOutput(out string) (museProbeOutcome, string) {
	if strings.Contains(out, museVersionMarker) {
		return museProbeFound, versionAfterMarker(out, museVersionMarker)
	}
	if strings.Contains(out, museAbsentMarker) {
		return museProbeAbsent, ""
	}
	return museProbeError, ""
}

// corroborateMuseAbsent second-guesses ONLY a museProbeAbsent, and only with
// a hard contradiction, downgrading it to museProbeError (no verdict, keep
// prior) instead of flipping a working install to "not installed".
// Deliberately asymmetric: a contradiction can only SUPPRESS a false absent,
// never manufacture a false "present".
func corroborateMuseAbsent(outcome museProbeOutcome, onDisk, processAlive func() bool) museProbeOutcome {
	if outcome != museProbeAbsent {
		return outcome
	}
	if onDisk() {
		slog.Info("muse-detect: completed probe said absent but a muse binary is on disk — treating as probe failure, keeping prior verdict")
		return museProbeError
	}
	if processAlive() {
		slog.Info("muse-detect: completed probe said absent with no binary on disk, but a muse process is running (binary likely mid-self-update) — treating as probe failure, keeping prior verdict")
		return museProbeError
	}
	return museProbeAbsent
}

// museBinaryOnDisk reports whether a `muse` executable exists on disk in any
// well-known install location, using ONLY load-immune filesystem lookups (a
// PATH walk plus stat — no shell, no fork). Used ONLY to contradict an absent
// verdict, never to assert presence. Muse is not node-managed, so unlike
// Claude there are no nvm/asdf-installs globs — just the manifest's
// HomeSubdirs.
func museBinaryOnDisk() bool {
	if _, err := exec.LookPath("muse"); err == nil {
		return true
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return false
	}
	return museBinaryUnderHome(home)
}

// museBinaryUnderHome is the home-relative half of museBinaryOnDisk, split
// out so it is unit-testable against a synthetic $HOME.
func museBinaryUnderHome(home string) bool {
	if isExistingFile(filepath.Join(home, ".local", "bin", "muse")) {
		return true
	}
	if isExistingFile(filepath.Join(home, ".asdf", "shims", "muse")) {
		return true
	}
	return false
}

// museVersionMarker fences `muse --version` output from any login-shell
// banner printed before it on stdout.
const museVersionMarker = "<<<SOLU_MUSE_VERSION>>>"

// museAPIKeyMarker is emitted by the probe script when META_API_KEY is set in
// the login-shell environment (the value itself never leaves the shell).
const museAPIKeyMarker = "<<<SOLU_MUSE_APIKEY>>>"

// museAbsentMarker is emitted when `command -v muse` finds nothing — making
// definitive absence distinguishable from a probe shell that died before
// answering (which must NOT count as "not installed").
const museAbsentMarker = "<<<SOLU_MUSE_ABSENT>>>"

// detectMuseWindows probes the host-native Windows daemon: the live registry
// PATH (PATHEXT resolves muse.exe/.cmd) then the installer's default dir.
// A RESOLVED path whose `--version` errors is a probe FAILURE, not absence.
func detectMuseWindows(ctx context.Context) (museProbeOutcome, string) {
	candidates := []string{"muse"}
	if home, _ := os.UserHomeDir(); home != "" {
		candidates = append(candidates, filepath.Join(home, ".local", "bin", "muse.exe"))
	}
	resolvedAny := false
	for _, c := range candidates {
		path := c
		if strings.ContainsAny(c, `\/`) {
			if _, err := os.Stat(c); err != nil {
				continue
			}
		} else {
			lp, ok := findOnWindowsPath(c)
			if !ok {
				var err error
				lp, err = exec.LookPath(c)
				if err != nil {
					continue
				}
			}
			path = lp
		}
		resolvedAny = true
		cmd := exec.CommandContext(ctx, path, "--version")
		applyNoWindow(cmd)
		out, err := cmd.Output()
		if err != nil {
			continue
		}
		return museProbeFound, firstLine(strings.TrimSpace(string(out)))
	}
	if resolvedAny {
		return museProbeError, ""
	}
	return museProbeAbsent, ""
}

// museInstallCommandSpec is what GET /api/setup/muse/install-command returns:
// the OS-correct argv to spawn in a pane, plus a human display string for
// up-front disclosure (we never run a remote installer without showing it).
// supported=false on platforms with no scripted installer.
type museInstallCommandSpec struct {
	Supported bool     `json:"supported"`
	Cmd       []string `json:"cmd"`
	Display   string   `json:"display"`
	Note      string   `json:"note"`
}

// museInstallCommandForOS builds the official Muse Code installer invocation
// for the daemon's runtime (https://dev.meta.ai/docs/quickstart.md).
// Unix (WSL/Linux/macOS): `curl -fsSL https://dev.meta.ai/install.sh | sh` —
// lands in ~/.local/bin; we source loginShellInit first so the post-install
// shell and future panes resolve the new binary. Windows host-native: Muse
// publishes a macOS/Linux installer only, so scripted install is unsupported
// there — the UI falls back to manual guidance (WSL or manual install).
// The command ECHOES itself + its source before running, so the pane is the
// disclosure.
func museInstallCommandForOS() museInstallCommandSpec {
	if runtime.GOOS == "windows" {
		return museInstallCommandSpec{
			Supported: false,
			Display:   "curl -fsSL https://dev.meta.ai/install.sh | sh",
			Note:      "Automatic install isn't available on Windows — run the installer from WSL, or install Muse Code manually, then re-check.",
		}
	}
	display := "curl -fsSL https://dev.meta.ai/install.sh | sh"
	script := loginShellInit() + "; " +
		"echo 'soluterminal will run the official Muse Code installer:'; " +
		"echo '  " + display + "'; " +
		"echo '  (source: https://dev.meta.ai/install.sh)'; echo; " +
		display + "; echo; " +
		// Verify by actually RESOLVING muse (PATH has ~/.local/bin via the
		// sourced init), NOT the pipe's exit: `curl … | sh` returns 0 even
		// when curl failed (no pipefail), which would print a false "installed".
		"if command -v muse >/dev/null 2>&1; then echo '[soluterminal] Muse Code installed — you can close this pane (or run: muse).'; " +
		"else echo '[soluterminal] Install did not complete — see the output above, then retry.'; fi; " +
		// Drop to an interactive shell so output stays visible. Completion is
		// detected by an INDEPENDENT recheck poll, not this shell's exit.
		"exec bash -i"
	return museInstallCommandSpec{
		Supported: true,
		Cmd:       []string{"bash", "-lc", script},
		Display:   display,
		Note:      "Runs the official installer in a new pane (no sudo; installs to ~/.local/bin).",
	}
}

// handleMuseInstallCommand returns the OS-correct installer command for the
// web to spawn in a pane. LOCAL-ONLY (403 over the relay): installing is a
// host action, so only the local UI may request it.
func (a *apiServer) handleMuseInstallCommand(w http.ResponseWriter, r *http.Request) {
	if pairRequestOverRelay(r) {
		http.Error(w, "muse install is local-only", http.StatusForbidden)
		return
	}
	writeJSON(w, 200, museInstallCommandForOS())
}

// handleMuseRecheck re-probes Muse status on demand (the setup checklist
// forces a fresh probe each time it opens) and returns the fresh status.
// LOCAL-ONLY: a relay-forwarded request is rejected 403 — probing spawns a
// shell on the host, so only the local UI may trigger it.
func (a *apiServer) handleMuseRecheck(w http.ResponseWriter, r *http.Request) {
	if pairRequestOverRelay(r) {
		http.Error(w, "muse re-check is local-only", http.StatusForbidden)
		return
	}
	a.refreshMuseStatus()
	st := a.currentMuseStatus()
	writeJSON(w, 200, map[string]any{
		"museChecked":   st.checked,
		"museInstalled": st.installed,
		"museVersion":   st.version,
		"museAuthed":    st.authed,
	})
}
