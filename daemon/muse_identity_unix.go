//go:build !windows

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

func discoverMuseIdentity(paneID string, pids []int) (string, bool) {
	return discoverMuseIdentityAt("/proc", paneID, pids)
}

// Bind the session to one foreground descendant, its inherited pane ID, an
// open session lock, and Muse's open runtime record naming that exact PID.
// Merely finding a recent session or an old resume argv is not sufficient.
func discoverMuseIdentityAt(procRoot, paneID string, pids []int) (string, bool) {
	active := false
	activeCount := 0
	result := ""
	for _, pid := range pids {
		pidDir := filepath.Join(procRoot, strconv.Itoa(pid))
		if !isMuseProcessName(strings.TrimSpace(readProcFile(pidDir, "comm"))) || !codexProcessIsForeground(pidDir) {
			continue
		}
		active = true
		activeCount++
		if activeCount > 1 {
			return "", true
		}
		env, ok := readProcEnvironAt(pidDir)
		if !ok || env["SOLU_PANE_ID"] != paneID {
			return "", true
		}
		dataRoot := env["XDG_DATA_HOME"]
		if dataRoot == "" && filepath.IsAbs(env["HOME"]) {
			dataRoot = filepath.Join(env["HOME"], ".local", "share")
		}
		if !filepath.IsAbs(dataRoot) {
			return "", true
		}
		museRoot, err := filepath.EvalSymlinks(filepath.Join(dataRoot, "muse"))
		if err != nil {
			return "", true
		}
		entries, err := os.ReadDir(filepath.Join(pidDir, "fd"))
		if err != nil || len(entries) > 1024 {
			return "", true
		}
		locks := map[string]string{}
		runtimeIDs := map[string]bool{}
		for _, entry := range entries {
			target, err := os.Readlink(filepath.Join(pidDir, "fd", entry.Name()))
			if err != nil {
				return "", true
			}
			rel, err := filepath.Rel(museRoot, target)
			if err != nil || strings.HasPrefix(rel, "..") {
				continue
			}
			parts := strings.Split(filepath.ToSlash(rel), "/")
			if len(parts) == 6 && parts[0] == "sessions" && parts[5] == ".session.lock" {
				if id, ok := canonicalMuseSessionID(parts[4]); ok {
					locks[id] = filepath.Join(filepath.Dir(target), "session.jsonl")
				}
			}
			if len(parts) == 4 && parts[0] == "runtime" && parts[1] == "muse" && parts[2] == "sessions" {
				id, ok := canonicalMuseSessionID(strings.TrimSuffix(parts[3], ".json"))
				if !ok || parts[3] != id+".json" {
					continue
				}
				info, err := os.Stat(target)
				if err != nil || !info.Mode().IsRegular() || info.Size() > 65536 {
					return "", true
				}
				bytes, err := os.ReadFile(target)
				var record struct {
					ID         string `json:"session_id"`
					Generation string `json:"process_generation_hint"`
				}
				if err != nil || json.Unmarshal(bytes, &record) != nil || record.ID != id || record.Generation != "pid="+strconv.Itoa(pid) {
					return "", true
				}
				runtimeIDs[id] = true
			}
		}
		if len(locks) != 1 || len(runtimeIDs) != 1 {
			return "", true
		}
		for id, transcript := range locks {
			info, err := os.Stat(transcript)
			if !runtimeIDs[id] || err != nil || !info.Mode().IsRegular() || info.Size() == 0 {
				return "", true
			}
			result = id
		}
		// Re-check ownership after bounded filesystem inspection.
		after, ok := readProcEnvironAt(pidDir)
		if !ok || after["SOLU_PANE_ID"] != paneID || !codexProcessIsForeground(pidDir) {
			return "", true
		}
	}
	return result, active
}
