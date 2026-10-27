//go:build windows

package main

import (
	"syscall"
	"unsafe"
)

type processMemoryCountersEx struct {
	Cb                         uint32
	PageFaultCount             uint32
	PeakWorkingSetSize         uintptr
	WorkingSetSize             uintptr
	QuotaPeakPagedPoolUsage    uintptr
	QuotaPagedPoolUsage        uintptr
	QuotaPeakNonPagedPoolUsage uintptr
	QuotaNonPagedPoolUsage     uintptr
	PagefileUsage              uintptr
	PeakPagefileUsage          uintptr
	PrivateUsage               uintptr
}

var getProcessMemoryInfo = syscall.NewLazyDLL("psapi.dll").NewProc("GetProcessMemoryInfo")

func osSelfMemMB() uint64 {
	counters, ok := selfProcessMemoryCounters()
	if !ok {
		return 0
	}
	return processMemoryUsageMB(counters.PrivateUsage, counters.WorkingSetSize)
}

// Working set is resident memory; the guard intentionally also considers
// private committed bytes, which must not be labelled RSS in telemetry.
func osSelfRSSMB() uint64 {
	counters, ok := selfProcessMemoryCounters()
	if !ok {
		return 0
	}
	return uint64(counters.WorkingSetSize) / (1024 * 1024)
}

func selfProcessMemoryCounters() (processMemoryCountersEx, bool) {
	if err := getProcessMemoryInfo.Find(); err != nil {
		return processMemoryCountersEx{}, false
	}
	handle, err := syscall.GetCurrentProcess()
	if err != nil {
		return processMemoryCountersEx{}, false
	}
	var counters processMemoryCountersEx
	counters.Cb = uint32(unsafe.Sizeof(counters))
	ok, _, _ := getProcessMemoryInfo.Call(
		uintptr(handle),
		uintptr(unsafe.Pointer(&counters)),
		uintptr(counters.Cb),
	)
	if ok == 0 {
		return processMemoryCountersEx{}, false
	}
	return counters, true
}
