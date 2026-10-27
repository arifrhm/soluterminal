//go:build windows

package main

import (
	"os"
	"syscall"
)

// Windows has no process groups in the POSIX sense; the driver kills the
// direct child and relies on muse's own child cleanup. (Native Muse panes
// run where the muse CLI runs — today that is WSL/unix — so this path is a
// build-completeness fallback, not a supported configuration.)
func museTurnSysProcAttr() *syscall.SysProcAttr { return nil }

func museSignalTurn(p *os.Process, kill bool) {
	if p == nil {
		return
	}
	_ = p.Kill()
}
