//go:build linux

package main

import "syscall"

// storageStatIdentity reads Linux block accounting for a path. ok is false when
// the path cannot be stat'd; blocks may legitimately be zero for an empty file.
func storageStatIdentity(path string) (dev, ino uint64, blocks int64, ok bool) {
	var st syscall.Stat_t
	if err := syscall.Stat(path, &st); err != nil {
		return 0, 0, 0, false
	}
	return uint64(st.Dev), uint64(st.Ino), int64(st.Blocks), true
}

// storageRootDevice reports the device backing a directory so the walker can
// refuse to cross into a descendant mount.
func storageRootDevice(path string) (uint64, bool) {
	var st syscall.Stat_t
	if err := syscall.Stat(path, &st); err != nil {
		return 0, false
	}
	return uint64(st.Dev), true
}

func storageAllocatedSupported() bool { return true }
