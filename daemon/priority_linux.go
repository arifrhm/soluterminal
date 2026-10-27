//go:build linux

package main

import (
	"log"
	"syscall"

	"golang.org/x/sys/unix"
)

func lowerProcessIOPriority(pid int) {
	const (
		ioprioWhoPgrp    = 2
		ioprioBestEffort = 2
		ioprioLevel      = 7
	)
	_, _, errno := syscall.Syscall(unix.SYS_IOPRIO_SET, ioprioWhoPgrp, uintptr(pid), uintptr(ioprioBestEffort<<13|ioprioLevel))
	if errno != 0 && errno != syscall.ESRCH {
		log.Printf("warning: lower IO priority for pgid %d: %v", pid, errno)
	}
}
