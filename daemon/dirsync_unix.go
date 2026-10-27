//go:build !windows

package main

import "os"

// syncDir fsyncs a directory so a rename/create inside it survives a crash.
func syncDir(path string) error {
	d, err := os.Open(path)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
