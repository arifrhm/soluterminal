//go:build !windows

package main

// On non-Windows hosts the registry-PATH reconstruction is irrelevant: Unix panes
// resolve commands through the bash -lc login wrap (loginShellInit), which already
// re-sources the user's profile and rebuilds PATH. These no-ops let the spawn path
// call the Windows helpers unconditionally.

func mergeRegistryPath(env []string) []string { return env }

func findOnWindowsPath(string) (string, bool) { return "", false }
