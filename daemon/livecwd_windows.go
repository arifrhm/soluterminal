//go:build windows

package main

// Windows stub for "what directory is the pane's shell currently in?".
// Unix uses /proc/<pid>/cwd; the Windows equivalent is
// NtQueryInformationProcess → ProcessBasicInformation → PEB →
// ProcessParameters.CurrentDirectory. That's undocumented-ish but
// widely used; ported in a later pass.
//
// Returning empty is handled by callers, which fall back to the
// spawn-time cwd of the pane — correct but stale after `cd`.

func liveCwdOfPane(_ *Pane) string { return "" }

func liveCwdOfPids(_ int, _ []int) string { return "" }
