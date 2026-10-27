package main

import (
	"path/filepath"

	"strings"
	"time"
)

func pathUnderRoot(path, root string) bool {
	if strings.TrimSpace(path) == "" || strings.TrimSpace(root) == "" {
		return false
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(absRoot, absPath)
	if err != nil || rel == "." || rel == ".." {
		return false
	}
	return !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

const (
	taskTmpWarnBytes    int64 = 10 << 30
	taskTmpAdjacentAge        = 24 * time.Hour
	taskTmpGrace              = 15 * time.Minute
	taskTmpMarkerName         = ".soluterminal-task-temp.json"
	taskTmpMarkerSchema       = 1
)
