//go:build windows

package main

import "os"

// Windows ACLs are not mode bits; the directory lives in the user's profile.
func fileOwnedByCurrentUser(os.FileInfo) bool { return true }

func openLocalBuildNoFollow(path string) (*os.File, error) { return os.Open(path) }
