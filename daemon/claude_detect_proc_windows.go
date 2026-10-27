//go:build windows

package main

// claudeProcessAlive is a no-op on Windows: the Windows detect path
// (detectClaudeWindows) never emits a marker-based absent that needs process
// corroboration — path resolution there is already load-immune, and a resolved
// binary that fails to run is classified as a probe error, not absence. The
// self-update transient-absence class is instead covered cross-platform by the
// confirmClaudeAbsent hysteresis.
func claudeProcessAlive() bool {
	return false
}
