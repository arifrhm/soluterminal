package main

import (
	"runtime"
	"sync"
)

// paneEnvironmentFor names where this daemon's panes (and so its agents) run,
// for the setup checklist's "Where agents run" row in a browser tab, where
// the desktop shell's backend global is absent.
func paneEnvironmentFor(goos string, insideWSL bool) string {
	switch goos {
	case "windows":
		return "windows"
	case "darwin":
		return "macos"
	case "linux":
		if insideWSL {
			return "wsl"
		}
		return "linux"
	}
	return goos
}

var paneEnvironment = sync.OnceValue(func() string {
	return paneEnvironmentFor(runtime.GOOS, runtime.GOOS == "linux" && isInsideWSL())
})
