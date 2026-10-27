//go:build !linux

package main

import "runtime"

func readPSI(string) (pSISample, bool)                            { return pSISample{}, false }
func readVmstatCounters() (uint64, uint64, bool)                  { return 0, 0, false }
func readProcCPUTimes(int) (uint64, uint64, uint64, uint64, bool) { return 0, 0, 0, 0, false }
func defaultPressureProbes() pressureProbes {
	return pressureProbes{
		psi: readPSI, vmstat: readVmstatCounters, loadAvg: func() (float64, bool) { return 0, false },
		nproc: runtime.NumCPU, memAvailKB: memAvailableKB, memTotalKB: readMemTotalKB,
		wsl: func() bool { return false },
	}
}
