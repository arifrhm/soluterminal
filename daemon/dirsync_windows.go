//go:build windows

package main

import (
	"os"

	"golang.org/x/sys/windows"
)

// syncDir flushes a directory's metadata so a rename/create inside it is
// durable. os.Open(dir).Sync() always fails on Windows with ERROR_ACCESS_DENIED:
// Go opens directories GENERIC_READ-only and FlushFileBuffers requires write
// access. A GENERIC_WRITE directory handle (FILE_FLAG_BACKUP_SEMANTICS is what
// lets CreateFile open a directory at all) flushes successfully.
func syncDir(path string) error {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return &os.PathError{Op: "sync", Path: path, Err: err}
	}
	h, err := windows.CreateFile(p, windows.GENERIC_READ|windows.GENERIC_WRITE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		return &os.PathError{Op: "open", Path: path, Err: err}
	}
	defer windows.CloseHandle(h)
	if err := windows.FlushFileBuffers(h); err != nil {
		return &os.PathError{Op: "sync", Path: path, Err: err}
	}
	return nil
}
