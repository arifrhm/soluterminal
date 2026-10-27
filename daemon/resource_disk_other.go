//go:build !linux

package main

func diskFreeBytes(string) (int64, bool) { return 0, false }
func diskUsedBytes(string) (int64, bool) { return 0, false }
