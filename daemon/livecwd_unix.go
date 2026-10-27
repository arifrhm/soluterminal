//go:build !windows

package main

import (
	"os"
	"path/filepath"
	"strconv"
)

// liveCwdOfPane returns the current working directory of the shell (or
// whatever process) running inside the pane. Walks the agent's descendants
// and picks the deepest one with a readable /proc/<pid>/cwd symlink — that's
// typically the interactive shell the user has cd'd around in.
func liveCwdOfPane(p *Pane) string {
	return liveCwdOfPids(p.Pid, p.ChildPids())
}

func liveCwdOfPids(root int, pids []int) string {
	if len(pids) == 0 {
		return ""
	}
	// Prefer the deepest (last pushed) descendant; fall back to earlier ones.
	for i := len(pids) - 1; i >= 0; i-- {
		pid := pids[i]
		if pid == root {
			// agent's own cwd is set to the spawn cwd; that's not useful here.
			continue
		}
		link := filepath.Join("/proc", strconv.Itoa(pid), "cwd")
		target, err := os.Readlink(link)
		if err != nil {
			continue
		}
		// Must exist and be a directory.
		if st, serr := os.Stat(target); serr != nil || !st.IsDir() {
			continue
		}
		return target
	}
	return ""
}
