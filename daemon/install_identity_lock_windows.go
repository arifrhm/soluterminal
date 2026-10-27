//go:build windows

package main

import (
	"context"
	"errors"
	"os"
	"time"

	"golang.org/x/sys/windows"
)

var errBrowserWindowLockBusy = errors.New("browser window ownership lock is busy")

func acquireBrowserWindowLock(ctx context.Context, path string, nonblocking bool) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	var overlapped windows.Overlapped
	for {
		flags := uint32(windows.LOCKFILE_EXCLUSIVE_LOCK | windows.LOCKFILE_FAIL_IMMEDIATELY)
		err = windows.LockFileEx(windows.Handle(f.Fd()), flags, 0, 1, 0, &overlapped)
		if err == nil {
			return f, nil
		}
		if err != windows.ERROR_LOCK_VIOLATION && err != windows.ERROR_IO_PENDING {
			_ = f.Close()
			return nil, err
		}
		if nonblocking {
			_ = f.Close()
			return nil, errBrowserWindowLockBusy
		}
		select {
		case <-ctx.Done():
			_ = f.Close()
			return nil, ctx.Err()
		case <-time.After(25 * time.Millisecond):
		}
	}
}

func releaseBrowserWindowLock(f *os.File) {
	if f == nil {
		return
	}
	var overlapped windows.Overlapped
	_ = windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 1, 0, &overlapped)
	_ = f.Close()
}
