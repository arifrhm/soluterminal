//go:build !windows

package main

import "os"

func osSelfRSSMB() uint64 { return osSelfMemMB() }

func osSelfMemMB() uint64 {
	rssKB, _, ok := readVmRSSHWMkb(os.Getpid())
	if !ok || rssKB <= 0 {
		return 0
	}
	return uint64(rssKB / 1024)
}
