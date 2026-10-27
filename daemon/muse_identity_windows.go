//go:build windows

package main

func discoverMuseIdentity(_ string, _ []int) (string, bool) { return "", false }
