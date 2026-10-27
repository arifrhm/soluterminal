//go:build linux

package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

func previewProcessStartIdentity(pid int) string {
	if pid <= 0 {
		return ""
	}
	raw, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return ""
	}
	// comm is parenthesized and may contain spaces. Field 22 (starttime) is
	// the 20th token after the closing parenthesis.
	close := strings.LastIndexByte(string(raw), ')')
	if close < 0 {
		return ""
	}
	fields := strings.Fields(string(raw[close+1:]))
	if len(fields) < 20 {
		return ""
	}
	return fmt.Sprintf("linux:%s", fields[19])
}
