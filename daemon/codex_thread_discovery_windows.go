//go:build windows

package main

// Windows has no /proc ownership/foreground evidence. Keep discovery
// fail-closed there until Codex exposes a validated lifecycle hook or equivalent
// process-bound identity channel.
func discoverCodexThreadID(_ string, _ []int) (string, bool) { return "", false }

func discoverCodexIdentity(_ string, _ []int) codexIdentityProbe { return codexIdentityProbe{} }

func paneDescendantIsForeground(_ int) bool { return false }
