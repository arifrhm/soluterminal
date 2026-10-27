//go:build !windows

package main

import (
	"os"
	"syscall"
)

// tryExclusiveFileLock takes a non-blocking exclusive flock. flock locks
// belong to the open file description, and Go opens files close-on-exec, so
// pane agents and update successors never inherit it: the lock ends exactly
// when this daemon process ends.
func tryExclusiveFileLock(f *os.File) (bool, error) {
	err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if err == nil {
		return true, nil
	}
	if err == syscall.EWOULDBLOCK || err == syscall.EAGAIN {
		return false, nil
	}
	return false, err
}

func unlockFile(f *os.File) {
	_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
}
