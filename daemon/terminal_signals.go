package main

import (
	"context"
	"strings"
	"time"
)

// Terminal-signal events for the Aria watcher. soluterminal already
// publishes "pane-state" transitions on the uiBus (see activity.go);
// these three event types give the aria-watcher (which consumes the
// /api/ui/events SSE) the raw material to build higher-level signals:
//
//   cmd.exit         — a pane's process exited.
//   cmd.long_running — a pane has been busy >30s without ever
//                      transitioning to awaiting-input.
//   build.failed     — a pane running a known build/test tool exited
//                      non-zero.
//
// cmd.exit / build.failed are emitted from pane.go's readLoop (see
// emitPaneExitEvents) right after ExitCode is finalized and the agent
// socket has closed. cmd.long_running is emitted from the long-running
// watcher loop below.

// buildToolPrefixes is the conservative allow-list of build/test tool
// command lines. build.failed only fires when a pane's command matches
// one of these AND the exit code is non-zero — we never scan PTY output
// for "error" strings, which is fragile across tools and locales.
//
// Matching is on the pane's joined argv, normalized to collapsed
// whitespace, after stripping a leading login-shell wrapper (panes
// spawned through tryLoginShellWrap run "bash -lc '... exec <cmd>'").
var buildToolPrefixes = []string{
	"go build",
	"go test",
	"npm run build",
	"npm test",
	"pnpm build",
	"pnpm test",
	"cargo build",
	"cargo test",
	"pytest",
	"make",
}

// paneCommandLine returns the pane's command as a single normalized
// string suitable for build-tool prefix matching. A login-shell
// wrapper ("bash -lc '<init>; exec <real cmd>'") is unwrapped to the
// real command so a wrapped `go build` still matches.
func paneCommandLine(cmd []string) string {
	if len(cmd) == 0 {
		return ""
	}
	// Unwrap the tryLoginShellWrap form: bash -lc "<init>; exec <cmd>".
	if len(cmd) == 3 && cmd[1] == "-lc" {
		if i := strings.LastIndex(cmd[2], "; exec "); i >= 0 {
			return strings.Join(strings.Fields(cmd[2][i+len("; exec "):]), " ")
		}
	}
	return strings.Join(strings.Fields(strings.Join(cmd, " ")), " ")
}

// isBuildToolCommand reports whether a pane command line is a known
// build/test invocation. Matching is prefix-based on the unwrapped
// command so `go test ./...` and `make all` both match.
func isBuildToolCommand(cmdLine string) bool {
	for _, prefix := range buildToolPrefixes {
		if cmdLine == prefix || strings.HasPrefix(cmdLine, prefix+" ") {
			return true
		}
	}
	return false
}

// emitPaneExitEvents publishes cmd.exit — and, when the pane ran a
// known build tool that failed, build.failed — onto the Manager's
// uiBus. Called from readLoop's exit path; the caller guarantees
// m.bus is non-nil.
func (m *Manager) emitPaneExitEvents(p *Pane) {
	snap := p.Snapshot()
	exitCode := 0
	if snap.ExitCode != nil {
		exitCode = *snap.ExitCode
	}
	cmdLine := paneCommandLine(snap.Cmd)
	durationMs := int64(0)
	if !snap.CreatedAt.IsZero() {
		durationMs = time.Since(snap.CreatedAt).Milliseconds()
	}

	m.bus.publish(uiEvent{
		Type:       "cmd.exit",
		PaneID:     snap.ID,
		Ts:         time.Now().UnixMilli(),
		Cmd:        cmdLine,
		Cwd:        snap.Cwd,
		ExitCode:   &exitCode,
		DurationMs: durationMs,
	})

	// build.failed is derived in the same hook: a build-tool command
	// that exited non-zero. Detection is command-name + exit-code only
	// (no PTY output scanning) — see buildToolPrefixes.
	if exitCode != 0 && isBuildToolCommand(cmdLine) {
		m.bus.publish(uiEvent{
			Type:       "build.failed",
			PaneID:     snap.ID,
			Ts:         time.Now().UnixMilli(),
			Cmd:        cmdLine,
			ExitCode:   &exitCode,
			StderrTail: tailOfPane(p, 20),
			DurationMs: durationMs,
		})
	}
}

// longRunningThreshold is how long a pane must stay continuously busy
// with a real foreground child process before cmd.long_running fires.
const longRunningThreshold = 30 * time.Second

// foregroundProbe is the OS hook the watcher uses to ask "what command
// is currently the foreground process of this pane?". paneForegroundCmd
// (procfs_unix.go) is the production implementation; tests inject a
// fake to avoid touching /proc.
type foregroundProbe func(rootPid int) (fgPid int, cmd string, ok bool)

// startLongRunningWatcher publishes a single cmd.long_running event per
// long-running foreground command. A pane is considered "busy with a
// real command" when:
//
//  1. computeState reports StateBusy (PTY has recent output), AND
//  2. the kernel reports a foreground process group whose leader is NOT
//     the pane's root pid — i.e. the shell has spawned a child command
//     (`tcgetpgrp(fd) != bash.pid`), or a TUI has shelled out via job
//     control. A pane whose root IS the foreground (bash sitting at its
//     prompt, claude in its event loop, vim idle) does not count as
//     busy regardless of how chatty the terminal is. Without this
//     gate, any shell with a constantly-redrawing prompt (starship
//     clock, OSC title updates, login-script noise) fires
//     cmd.long_running for the shell wrapper itself within 30s of
//     opening the pane — confirmed 2026-05-23 with /bin/bash panes
//     showing up in the Aria feed as "long-running" with elapsed=30s.
//
// fire-once semantics survive within a single foreground identity: when
// the foreground pid changes (command finishes, another starts) the
// timer restarts so the next command gets its own 30s window.
func startLongRunningWatcher(ctx context.Context, mgr *Manager, bus *uiBus) {
	runLongRunningWatcher(ctx, mgr, bus, paneForegroundCmd, 2*time.Second, longRunningThreshold)
}

// runLongRunningWatcher is the testable core: takes the probe, tick
// interval, and threshold so tests can drive it without /proc and
// without waiting real seconds.
func runLongRunningWatcher(
	ctx context.Context,
	mgr *Manager,
	bus *uiBus,
	probe foregroundProbe,
	tickInterval time.Duration,
	threshold time.Duration,
) {
	tick := time.NewTicker(tickInterval)
	defer tick.Stop()
	runLongRunningWatcherTicks(ctx, mgr, bus, probe, tick.C, threshold)
}

// runLongRunningWatcherTicks contains the watcher state machine separately
// from its wall-clock ticker so tests can supply exact tick timestamps. A
// time.Ticker is intentionally allowed to drop ticks under scheduler load,
// which makes call-count-based timing assertions nondeterministic.
func runLongRunningWatcherTicks(
	ctx context.Context,
	mgr *Manager,
	bus *uiBus,
	probe foregroundProbe,
	ticks <-chan time.Time,
	threshold time.Duration,
) {
	type fgRun struct {
		since time.Time
		fgPid int
		cmd   string
		fired bool
	}
	runs := map[string]fgRun{}

	for {
		select {
		case <-ctx.Done():
			return
		case now, ok := <-ticks:
			if !ok {
				return
			}
			for _, pane := range mgr.List() {
				if pane.computeStateCached() != StateBusy {
					delete(runs, pane.ID)
					continue
				}
				fgPid, fgCmd, hasFg := probe(pane.Pid)
				if !hasFg {
					// Root is its own foreground — no command is
					// actually running. Drop any stale timer.
					delete(runs, pane.ID)
					continue
				}
				r, seen := runs[pane.ID]
				if !seen || r.fgPid != fgPid {
					runs[pane.ID] = fgRun{since: now, fgPid: fgPid, cmd: fgCmd}
					continue
				}
				if r.fired {
					continue
				}
				if now.Sub(r.since) >= threshold {
					r.fired = true
					runs[pane.ID] = r
					snap := pane.Snapshot()
					bus.publish(uiEvent{
						Type:      "cmd.long_running",
						PaneID:    snap.ID,
						Ts:        now.UnixMilli(),
						Cmd:       fgCmd,
						ElapsedMs: now.Sub(r.since).Milliseconds(),
					})
				}
			}
			for id := range runs {
				if _, ok := mgr.Get(id); !ok {
					delete(runs, id)
				}
			}
		}
	}
}
