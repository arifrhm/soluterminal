//go:build windows

package main

import (
	"errors"
	"fmt"
	"os"
	"syscall"
	"time"
)

func updateListenerAddrInUse(err error) bool {
	// net.Listen wraps Winsock WSAEADDRINUSE, not Go's application-defined
	// syscall.EADDRINUSE. Errno.Is does not translate between the two.
	const wsaEADDRINUSE syscall.Errno = 10048
	return errors.Is(err, wsaEADDRINUSE) || errors.Is(err, syscall.EADDRINUSE)
}

const windowsSwapRetryWindow = 12 * time.Second

// execInPlace is a no-op on Windows: there's no execve, and the systemd
// boot-guard that uses it never runs here (the marker it keys off is only
// written on the systemd Apply path, which is Linux-only). Present so the
// cross-platform boot-guard compiles.
func execInPlace(path string, args, env []string) error {
	return errors.New("exec-in-place not supported on windows")
}

// swapBinary replaces the running .exe on Windows with the freshly-
// downloaded one. Windows refuses to delete or overwrite a running
// .exe (ERROR_ACCESS_DENIED), but it WILL let you rename one — the
// kernel keeps the file's contents loaded as long as the process
// holds its image handle, regardless of where the directory entry
// points. So the swap is two renames:
//
//	solud.exe        → solud.exe.old   (parks the running file)
//	solud.exe.new    → solud.exe       (puts the new file in place)
//
// Older versions ride the rotating ring (.old.1, .old.2 — see
// updater_ring.go) so the operator has multiple rollback targets.
//
// rotateOldRingForward also handles the historical "previous .old
// can't be removed because AV is scanning it" case: a failed Remove
// just leaves the slot in place, the rename below would fail, and
// we surface to the caller. The .old.delete fallback the prior code
// used relied on single-slot semantics; with the ring, ring rotation
// would naturally absorb a stuck file on the next update without any
// special name.
func swapBinary(currentPath, newPath string) error {
	rotateOldRingForward(currentPath)
	oldParked := currentPath + ".old"
	if _, err := os.Stat(oldParked); err == nil {
		// Ring rotation should have cleared this; if not (e.g. .old.1
		// was locked and rotation skipped that slot), try briefly to clear
		// it. If Windows/AV still has it locked, park the current exe to a
		// unique delete bucket so a stale .old.delete from a prior attempt
		// cannot block this update.
		if rmErr := removeWithRetry(oldParked, windowsSwapRetryWindow); rmErr != nil {
			oldParked = fmt.Sprintf("%s.old.delete.%d", currentPath, time.Now().UnixNano())
		}
	}
	if err := renameWithRetry(currentPath, oldParked, windowsSwapRetryWindow); err != nil {
		return fmt.Errorf("park current → %s: %w", oldParked, err)
	}
	if err := renameWithRetry(newPath, currentPath, windowsSwapRetryWindow); err != nil {
		// Best-effort restore so we don't leave the user without a
		// runnable solud.exe.
		_ = renameWithRetry(oldParked, currentPath, 2*time.Second)
		return fmt.Errorf("install new → %s: %w", currentPath, err)
	}
	return nil
}

func renameWithRetry(src, dst string, maxWait time.Duration) error {
	deadline := time.Now().Add(maxWait)
	var lastErr error
	for {
		err := os.Rename(src, dst)
		if err == nil {
			return nil
		}
		lastErr = err
		if time.Now().After(deadline) {
			return lastErr
		}
		time.Sleep(250 * time.Millisecond)
	}
}

func removeWithRetry(path string, maxWait time.Duration) error {
	deadline := time.Now().Add(maxWait)
	var lastErr error
	for {
		err := os.Remove(path)
		if err == nil || os.IsNotExist(err) {
			return nil
		}
		lastErr = err
		if time.Now().After(deadline) {
			return lastErr
		}
		time.Sleep(250 * time.Millisecond)
	}
}
