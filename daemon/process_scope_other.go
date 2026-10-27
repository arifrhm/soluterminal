//go:build !linux

package main

import (
	"fmt"
	"os"
	"os/exec"
)

func checkProcessScopeDirectory(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("invalid process scope directory")
	}
	return nil
}
func syncProcessScopeDirectory(string) error { return nil }
func lockProcessScope(string, string) (func(), error) {
	return nil, fmt.Errorf("process scope locking is unsupported")
}
func processScopeBirth(int) string                                            { return "" }
func preparePlatformProcessScope(*exec.Cmd, bool) (*ownedProcessScope, error) { return nil, nil }
func processScopeHasContainment(processScopeIdentity) bool                    { return false }
func recoverPlatformProcessScope(int) (*ownedProcessScope, error)             { return nil, nil }
func closeEmptyProcessScope(*ownedProcessScope) (bool, error) {
	return false, fmt.Errorf("process scope proof is unsupported")
}
func stopOwnedProcessScope(*ownedProcessScope, bool) (bool, error) {
	return false, fmt.Errorf("process scope proof is unsupported")
}
