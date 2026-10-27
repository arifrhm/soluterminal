//go:build linux

package main

import (
	"errors"
	"fmt"
	"syscall"

	"golang.org/x/sys/unix"
)

// Legacy panes have no persisted scope. Bind a pidfd before checking the
// exact agent socket, so a recycled PID never authorizes killing another pane.
func killLegacyPaneAgent(pid int, socket string) error {
	fd, err := unix.PidfdOpen(pid, 0)
	if errors.Is(err, unix.ESRCH) {
		return nil
	}
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	if !agentPidOwnsSocket(pid, socket) {
		return fmt.Errorf("pane agent identity cannot be confirmed; refusing PID-only shutdown")
	}
	err = unix.PidfdSendSignal(fd, unix.Signal(syscall.SIGKILL), nil, 0)
	if errors.Is(err, unix.ESRCH) {
		return nil
	}
	return err
}
