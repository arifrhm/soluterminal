//go:build linux

package main

import "syscall"

// diskFreeBytes fails open like memAvailableKB: a missing platform signal
// delays nothing and denies nothing.
func diskFreeBytes(path string) (int64, bool) {
	if path == "" {
		return 0, false
	}
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, false
	}
	return int64(st.Bavail) * int64(st.Bsize), true
}

func diskUsedBytes(path string) (int64, bool) {
	if path == "" {
		return 0, false
	}
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, false
	}
	usedBlocks := st.Blocks - st.Bfree
	return int64(usedBlocks) * int64(st.Bsize), true
}
