package main

import (
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
)

// shellInfo is the on-disk shape of ~/.solu/shell-info.json. Written by
// the Tauri shell on app startup; read by the daemon as a fallback when
// it wasn't launched with --shell-version (e.g. systemd-managed solud).
type shellInfo struct {
	Version  string `json:"version"`
	Platform string `json:"platform"`
}

// readShellInfo reads ~/.solu/shell-info.json and returns its version /
// platform fields. Missing file is normal (returns empty strings, no
// error) — solud may have been launched standalone, or the Tauri shell
// hasn't run yet on this machine. Malformed JSON is logged but not
// fatal: an empty return keeps the rest of startup running.
func readShellInfo(homeDir string) (version, platform string, err error) {
	if homeDir == "" {
		return "", "", nil
	}
	path := filepath.Join(homeDir, ".solu", "shell-info.json")
	b, readErr := os.ReadFile(path)
	if readErr != nil {
		if errors.Is(readErr, fs.ErrNotExist) {
			return "", "", nil
		}
		return "", "", readErr
	}
	var info shellInfo
	if jsonErr := json.Unmarshal(b, &info); jsonErr != nil {
		slog.Warn("ignoring malformed shell-info.json", "path", path, "err", jsonErr)
		return "", "", nil
	}
	return info.Version, info.Platform, nil
}
