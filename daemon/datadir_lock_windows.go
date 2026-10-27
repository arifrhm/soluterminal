//go:build windows

package main

import "os"

// The Windows-native daemon is started and stopped only by the desktop
// shell, which already keeps one per data directory. The duplicate-daemon
// failures this lock prevents were all WSL-side, so Windows reports the lock
// as taken without holding anything.
func tryExclusiveFileLock(f *os.File) (bool, error) {
	return true, nil
}

func unlockFile(f *os.File) {}
