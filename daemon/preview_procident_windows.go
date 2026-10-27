//go:build windows

package main

import (
	"fmt"

	"golang.org/x/sys/windows"
)

func previewProcessStartIdentity(pid int) string {
	if pid <= 0 {
		return ""
	}
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return ""
	}
	defer windows.CloseHandle(handle)
	var created, exited, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(handle, &created, &exited, &kernel, &user); err != nil {
		return ""
	}
	return fmt.Sprintf("windows:%d", created.Nanoseconds())
}
