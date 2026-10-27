//go:build !linux

package main

// Non-Linux hosts report apparent bytes only. Allocated bytes stay unknown and
// inode/device identity is disabled so the walker never fabricates Linux block
// accounting or dedupes unrelated paths.
func storageStatIdentity(string) (dev, ino uint64, blocks int64, ok bool) {
	return 0, 0, 0, false
}

func storageRootDevice(string) (uint64, bool) { return 0, false }

func storageAllocatedSupported() bool { return false }
