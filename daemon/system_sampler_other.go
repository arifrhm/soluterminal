//go:build !linux

package main

func readProcCPUTicks(int) (uint64, uint64, bool) { return 0, 0, false }
func readHostCPUTicks() (uint64, uint64, bool)    { return 0, 0, false }
func readLoadAvg1() (float64, bool)               { return 0, false }
func systemDiskUsage(string) (int64, int64, bool) { return 0, 0, false }
func systemReadRSS(int) (int, int, bool)          { return 0, 0, false }
