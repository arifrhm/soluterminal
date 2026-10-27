//go:build windows

package main

// maybeReexecCleanEnv is a no-op on Windows. The Windows daemon is launched by
// the Tauri desktop app from a clean GUI session (no CLAUDE_CODE_* inherited),
// and there is no syscall.Exec equivalent to re-exec in place. The agent-level
// stripInheritedClaudeEnv still sanitizes every pane command's env.
func maybeReexecCleanEnv() {}
