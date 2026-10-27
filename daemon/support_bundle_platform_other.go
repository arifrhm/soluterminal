//go:build !linux

package main

import "runtime"

func supportBundlePlatformInfo() string {
	return supportBundleKV("system", map[string]string{
		"os": runtime.GOOS, "arch": runtime.GOARCH,
		"launch": "See launch.log, daemon.log and successor.log for desktop startup errors.",
	})
}
