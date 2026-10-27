//go:build !windows

package main

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

// detachedSysProcAttr returns the SysProcAttr to apply to a child
// agent so it outlives this daemon — Setsid makes it a new session
// leader, detached from our controlling TTY and process group, so
// Ctrl-C / SIGTERM to the daemon doesn't propagate down.
func detachedSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setsid: true}
}

// successorSysProcAttr returns SysProcAttr for the self-update
// successor process. We Setsid here (creating a new session and
// process group) for two reasons:
//  1. The WSL daemon is launched by `wsl.exe -e bash start-wsl.sh`,
//     which exec'd into our process. We're in the bash session.
//     When we exit, the kernel can deliver SIGHUP to the process
//     group of any controlling terminal that disappears with us.
//     Without Setsid, the successor inherits our PG and may get
//     caught in that signal cascade — the observed symptom is the
//     successor dying immediately after spawn with no log output.
//  2. On WSL specifically, the wsl.exe Windows-side process going
//     away can trigger session-leader cleanup that propagates to
//     anything still in the original session. New session = isolated.
//
// The Tauri shell on Windows has its own Job Object that owns the
// wsl.exe child; that mechanism handles "kill everything when Tauri
// quits" independently of process groups, so detaching here doesn't
// risk orphan daemons in the normal-quit case.
func successorSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setsid: true}
}

func shellInstallerSysProcAttr() *syscall.SysProcAttr {
	return successorSysProcAttr()
}

// terminatePid sends SIGTERM to the process. Used for graceful
// shutdown — the child is expected to cleanup and exit.
func terminatePid(pid int) error {
	if pid <= 0 {
		return nil
	}
	return syscall.Kill(pid, syscall.SIGTERM)
}

// hardKillPid sends SIGKILL — unconditional, for "it didn't shut
// down in time" cases.
func hardKillPid(pid int) error {
	if pid <= 0 {
		return nil
	}
	return syscall.Kill(pid, syscall.SIGKILL)
}

// processAlive returns true iff pid refers to a running process we
// can signal. Uses signal 0 (the "does this process exist" probe)
// on Unix.
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return proc.Signal(syscall.Signal(0)) == nil
}

// applyDetached sets SysProcAttr for a cmd we want to outlive us.
// Convenience around detachedSysProcAttr so callers don't import
// syscall just to build the struct.
func applyDetached(cmd *exec.Cmd) {
	cmd.SysProcAttr = detachedSysProcAttr()
}

func applyNoWindow(cmd *exec.Cmd) {}

// applyDetachedNoBreakaway mirrors the Windows fallback's signature. Unix
// has no Job Object breakaway concept, so it's identical to applyDetached
// (Setsid). It exists only so the cross-platform spawn-retry in
// pane_spawn.go compiles on every target.
func applyDetachedNoBreakaway(cmd *exec.Cmd) {
	cmd.SysProcAttr = detachedSysProcAttr()
}

// isJobBreakawayDenied is always false on Unix — the breakaway /
// ERROR_ACCESS_DENIED failure mode is specific to Windows Job Objects.
func isJobBreakawayDenied(error) bool {
	return false
}

// pipeChildSysProcAttr puts a --no-pty (pipe-mode) agent child into its
// OWN process group, so terminateChildTree's group signal (kill -pgid)
// hits just the child's subtree and not the agent. A PTY child gets this
// for free from the pty's setsid; a pipe child must request it.
func pipeChildSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setpgid: true}
}

// initAgentProcess is called once from runAgent() on startup. On
// Unix there's nothing to do — the daemon already Setsid'd us via
// applyDetached, so we're our own session leader and will outlive
// daemon death. On Windows this function creates a Job Object to
// provide equivalent semantics (agent owns its own process tree,
// children die with agent, agent itself is NOT in the daemon's job).
func initAgentProcess() error {
	return nil
}

// terminateChildTree asks the PTY child's process tree to shut down
// cleanly — SIGTERM on Unix, no-op-for-tree on Windows (the agent's
// Job Object handles the cascade when the agent itself dies, but
// there's no graceful "ask nicely" signal equivalent on Windows
// without ConsoleCtrlEvent which requires more setup).
func terminateChildTree(proc *os.Process) error {
	if proc == nil {
		return nil
	}
	if handled, err := stopScopedProcess(proc, false); handled {
		return err
	}
	// Negative pid = signal the process group (see kill(2)).
	err := syscall.Kill(-proc.Pid, syscall.SIGTERM)
	if errors.Is(err, syscall.ESRCH) {
		return nil
	}
	return err
}

// killChildTree force-terminates the PTY child's process tree.
// SIGKILL on Unix; best-effort proc.Kill() on Windows (the Job
// Object cleanup gets grandchildren).
func killChildTree(proc *os.Process) error {
	if proc == nil {
		return nil
	}
	if handled, err := stopScopedProcess(proc, true); handled {
		return err
	}
	err := syscall.Kill(-proc.Pid, syscall.SIGKILL)
	if errors.Is(err, syscall.ESRCH) {
		return nil
	}
	return err
}

// platformDefaultShell: Unix convention is $SHELL with a bash
// fallback. /bin/bash on most distros, /usr/bin/bash on some; the
// env var is set correctly by login shells.
func platformDefaultShell() string {
	if s := os.Getenv("SHELL"); s != "" {
		return s
	}
	return "/bin/bash"
}
