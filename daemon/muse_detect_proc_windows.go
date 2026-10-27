//go:build windows

package main

// museProcessAlive is a no-op on Windows: the Windows detect path resolves
// (but never runs) the binary, so a resolved-but-unrun binary is already a
// probe failure rather than absence — there is no self-update window to
// corroborate. Mirrors claudeProcessAlive's Windows stub.
func museProcessAlive() bool {
	return false
}
