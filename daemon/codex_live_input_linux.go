//go:build linux

package main

import (
	"errors"
	"os"
	"syscall"
	"time"
)

func lockCodexLiveInput(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	deadline := time.Now().Add(250 * time.Millisecond)
	for {
		err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return f, nil
		}
		if (err != syscall.EWOULDBLOCK && err != syscall.EAGAIN) || time.Now().After(deadline) {
			f.Close()
			return nil, errors.New("terminal input coordination busy")
		}
		time.Sleep(5 * time.Millisecond)
	}
}
func unlockCodexLiveInput(f *os.File) { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN); _ = f.Close() }
