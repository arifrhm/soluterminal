//go:build !windows

package main

import (
	"os"
	"syscall"
)

// museTurnSysProcAttr puts each muse exec child in its own process group so
// an interrupt can signal the whole tree (the muse launcher script execs the
// real binary, and tools it runs may have children of their own).
func museTurnSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setpgid: true}
}

func museSignalTurn(p *os.Process, kill bool) {
	if p == nil {
		return
	}
	sig := syscall.SIGTERM
	if kill {
		sig = syscall.SIGKILL
	}
	_ = syscall.Kill(-p.Pid, sig)
}
