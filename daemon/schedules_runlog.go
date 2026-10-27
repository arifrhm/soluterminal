package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"time"
)

// maxRunLogs bounds the per-agent runs/ ring buffer. 10 matches what the
// run-history UI can usefully show (Runs itself caps at 20, and only agent
// runs carry logs).
const maxRunLogs = 10

var runLogNameRe = regexp.MustCompile(`^run-(\d+)\.log$`)

// writeRunLog persists one run's captured output into `dir` and prunes it to
// the newest maxRunLogs files. Returns the written basename, or "" on any
// failure — the run record then simply carries no log link (never fatal: the
// log is a nicety, the run result is the truth). Agent runs pass
// <agentHome>/runs; task runs pass <dataDir>/schedule-runs/<scheduleID>.
func writeRunLog(dir string, start time.Time, data []byte) string {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return ""
	}
	name := fmt.Sprintf("run-%d.log", start.UnixMilli())
	if err := os.WriteFile(filepath.Join(dir, name), data, 0o600); err != nil {
		return ""
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return name
	}
	var stamps []int64
	for _, e := range entries {
		if m := runLogNameRe.FindStringSubmatch(e.Name()); m != nil {
			if ts, err := strconv.ParseInt(m[1], 10, 64); err == nil {
				stamps = append(stamps, ts)
			}
		}
	}
	if len(stamps) > maxRunLogs {
		sort.Slice(stamps, func(i, j int) bool { return stamps[i] > stamps[j] }) // newest first
		for _, ts := range stamps[maxRunLogs:] {
			_ = os.Remove(filepath.Join(dir, fmt.Sprintf("run-%d.log", ts)))
		}
	}
	return name
}
