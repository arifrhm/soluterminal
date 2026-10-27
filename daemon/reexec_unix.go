//go:build !windows

package main

import (
	"log"
	"os"
	"syscall"
)

// envCleanedSentinel marks an environment that has already been through the
// self-clean re-exec, so maybeReexecCleanEnv runs at most once per lineage.
const envCleanedSentinel = "SOLU_ENV_CLEANED"

// maybeReexecCleanEnv re-execs the daemon ONCE with the inherited Claude Code
// session env stripped, when the daemon was launched from inside a `claude`
// session (so its own environ carries CLAUDE_CODE_CHILD_SESSION / SESSION_ID).
//
// Why a re-exec rather than only fixing spawn sites: the dirty env would
// otherwise (1) be a foot-gun for any future code path that execs `claude`
// before the agent-level strip, and (2) propagate to every successor daemon
// across auto-updates (spawnSuccessor inherits os.Environ()). Re-exec'ing the
// current process clean kills both at the source.
//
// Safety: this runs in main() AFTER subcommand dispatch (so `solud agent` /
// `solud router`, which legitimately run under the daemon env and strip
// per-command, are never re-exec'd) and BEFORE any port bind or state load, so
// there is no listener handoff and no in-memory state to lose. Pane agents are
// detached (Setsid) and survive the exec, reattaching exactly as they do after
// an auto-update. syscall.Exec replaces the process image in place (same pid,
// same args). It only returns on failure, in which case we log and continue
// running with the dirty env (fail-open: a degraded daemon beats no daemon).
func maybeReexecCleanEnv() {
	// Nothing to clean, or we already cleaned this lineage.
	if os.Getenv(childSessionEnvKey) == "" && os.Getenv("CLAUDECODE") == "" {
		return
	}
	if os.Getenv(envCleanedSentinel) == "1" {
		return
	}
	self, err := os.Executable()
	if err != nil {
		log.Printf("self-clean: os.Executable failed, continuing with inherited env: %v", err)
		return
	}
	env := append(stripInheritedClaudeEnv(os.Environ()), envCleanedSentinel+"=1")
	log.Printf("self-clean: re-exec to drop inherited CLAUDE_CODE_* env (was launched inside a claude session)")
	if err := syscall.Exec(self, os.Args, env); err != nil {
		log.Printf("self-clean: re-exec failed, continuing with inherited env: %v", err)
	}
}
