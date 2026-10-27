//go:build !linux && !windows

package main

func previewProcessStartIdentity(int) string { return "" }
