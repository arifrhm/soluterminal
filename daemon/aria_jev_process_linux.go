//go:build linux

package main

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

func probeJevProcessIdentity(ctx context.Context, root int) (jevProcessIdentity, error) {
	return probeJevProcessIdentityAt(ctx, "/proc", root)
}

func probeJevProcessIdentityAt(ctx context.Context, procRoot string, root int) (jevProcessIdentity, error) {
	return probeJevProcessIdentityWithSnapshots(ctx, root, jevProcessLinuxSource, func(ctx context.Context, _ int) (jevProcessSnapshot, error) {
		return readJevLinuxProcessSnapshot(ctx, procRoot)
	})
}

// Only /proc stat metadata is read. No provider-name filter, argv, environment,
// credential, cwd or session discovery is needed to identify incarnations.
func readJevLinuxProcessSnapshot(ctx context.Context, procRoot string) (jevProcessSnapshot, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	dir, err := os.Open(procRoot)
	if err != nil {
		return nil, errJevProcessUnavailable
	}
	defer dir.Close()
	entries, err := dir.ReadDir(jevProcessMaxEntries + 1)
	if err != nil && !errors.Is(err, io.EOF) || len(entries) > jevProcessMaxEntries {
		return nil, errJevProcessUnavailable
	}
	snapshot := make(jevProcessSnapshot)
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || pid <= 0 || !entry.IsDir() {
			continue
		}
		path := filepath.Join(procRoot, entry.Name())
		stat, err := readConditionalProcStat(path)
		if err != nil {
			// The existing parser deliberately rejects dead processes. Ignore
			// those and vanished entries, but never hide other unreadable or
			// malformed metadata behind a successful partial snapshot.
			raw, readErr := os.ReadFile(filepath.Join(path, "stat"))
			if errors.Is(readErr, os.ErrNotExist) {
				continue
			}
			if readErr == nil {
				end := strings.LastIndexByte(string(raw), ')')
				if end >= 0 {
					fields := strings.Fields(string(raw[end+1:]))
					if len(fields) > 0 && (fields[0] == "Z" || fields[0] == "X") {
						continue
					}
				}
			}
			return nil, errJevProcessUnavailable
		}
		start, err := strconv.ParseUint(stat.start, 10, 64)
		if err != nil || start == 0 {
			return nil, errJevProcessUnavailable
		}
		snapshot[pid] = jevProcessRecord{PID: pid, Parent: stat.parent, Group: stat.group, TTY: stat.tty,
			Foreground: stat.foreground, Start: "linux:" + stat.start, StartOrder: start,
			Stopped: stat.state == "T" || stat.state == "t"}
	}
	return snapshot, nil
}
