//go:build !windows

package main

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"syscall"
)

// turnReadInRoot reads a regular file inside root. os.Root refuses any path
// that resolves outside the repository; O_NONBLOCK keeps a FIFO from hanging
// the open, and the fstat check refuses FIFOs, devices and sockets.
func turnReadInRoot(root *os.Root, rel string, maxBytes int64) ([]byte, error) {
	f, err := root.OpenFile(filepath.FromSlash(rel), os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return readCappedRegular(f, maxBytes)
}

// turnReadNoFollow is turnReadInRoot for a file already resolved by the caller
// (absolute path); used where no repository root applies.
func turnReadNoFollow(path string, maxBytes int64) ([]byte, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return readCappedRegular(f, maxBytes)
}

func readCappedRegular(f *os.File, maxBytes int64) ([]byte, error) {
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > maxBytes {
		return nil, errors.New("not a regular file within the size cap")
	}
	data, err := io.ReadAll(io.LimitReader(f, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxBytes {
		return nil, errors.New("file grew past the size cap")
	}
	return data, nil
}
