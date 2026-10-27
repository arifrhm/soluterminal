//go:build !windows

package main

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

func updateListenerAddrInUse(err error) bool {
	return errors.Is(err, syscall.EADDRINUSE)
}

// execInPlace replaces the current process image with the binary at path,
// keeping the same PID — so under systemd the unit's MainPID is unchanged and
// systemd keeps tracking it. Used by the update boot-guard to re-launch the
// restored .old binary after a failed update. Only returns on error.
func execInPlace(path string, args, env []string) error {
	return syscall.Exec(path, args, env)
}

// swapBinary replaces the running binary at currentPath with the
// freshly-downloaded binary at newPath. The previous version is parked
// at currentPath + ".old"; older versions ride the rolling ring
// (.old.1, .old.2 — see updater_ring.go) so the operator has multiple
// rollback targets, not just the most recent.
//
// On Unix the kernel holds the running process's image via its open
// file handle, so even after rename(currentPath, .old) the running
// process continues fine; the .old name just gives us a directory
// entry to point future execve() at.
func swapBinary(currentPath, newPath string) error {
	if err := os.Chmod(newPath, 0o755); err != nil {
		return fmt.Errorf("chmod %s: %w", newPath, err)
	}
	rotateOldRingForward(currentPath)
	oldParked := currentPath + ".old"
	if err := os.Rename(currentPath, oldParked); err != nil {
		return fmt.Errorf("park current → %s: %w", oldParked, err)
	}
	if err := os.Rename(newPath, currentPath); err != nil {
		// Best-effort restore so we don't leave the user without a
		// runnable binary.
		_ = os.Rename(oldParked, currentPath)
		return fmt.Errorf("install new → %s: %w", currentPath, err)
	}
	return nil
}
