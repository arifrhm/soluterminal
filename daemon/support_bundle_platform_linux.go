//go:build linux

package main

import (
	"os"
	"path/filepath"
	"strings"
)

func supportBundlePlatformInfo() string {
	kernel, note := supportBundleReadSmallFile("/proc/version")
	home, _ := os.UserHomeDir()
	unit := filepath.Join(home, ".config", "systemd", "user", "soluterminal.service")
	_, unitErr := os.Stat(unit)
	return supportBundleKV("system", map[string]any{
		"kernel": strings.TrimSpace(string(kernel)), "kernelNote": note,
		"wsl":             strings.Contains(strings.ToLower(string(kernel)), "microsoft"),
		"systemdUserUnit": unit, "systemdUserUnitPresent": unitErr == nil,
		"launch": "See daemon.log and successor.log; unit contents and environment are omitted.",
	})
}
