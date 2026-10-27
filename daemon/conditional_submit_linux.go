//go:build linux

package main

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

func conditionalSubmitSupported() bool { return true }

func probeConditionalSubmitProcess(ctx context.Context, root int) (conditionalSubmitProcess, error) {
	return probeConditionalSubmitProcessAt(ctx, "/proc", root)
}

type conditionalProcStat struct {
	parent, group, tty, foreground int
	start                          string
	state                          string
}

func readConditionalProcStat(dir string) (conditionalProcStat, error) {
	raw, err := os.ReadFile(filepath.Join(dir, "stat"))
	if err != nil {
		return conditionalProcStat{}, errConditionalProcessUnavailable
	}
	end := strings.LastIndexByte(string(raw), ')')
	if end < 0 {
		return conditionalProcStat{}, errConditionalProcessUnavailable
	}
	fields := strings.Fields(string(raw[end+1:]))
	if len(fields) < 20 || fields[0] == "Z" || fields[0] == "X" {
		return conditionalProcStat{}, errConditionalProcessUnavailable
	}
	values := make([]int, 4)
	for i, field := range []int{1, 2, 4, 5} {
		values[i], err = strconv.Atoi(fields[field])
		if err != nil {
			return conditionalProcStat{}, errConditionalProcessUnavailable
		}
	}
	ticks, err := strconv.ParseUint(fields[19], 10, 64)
	if err != nil || ticks == 0 {
		return conditionalProcStat{}, errConditionalProcessUnavailable
	}
	return conditionalProcStat{parent: values[0], group: values[1], tty: values[2], foreground: values[3],
		start: strconv.FormatUint(ticks, 10), state: fields[0]}, nil
}

func conditionalDescendsFrom(pid, root int, stats map[int]conditionalProcStat) bool {
	for steps := 0; steps < 128 && pid > 0; steps++ {
		stat, ok := stats[pid]
		if !ok {
			return false
		}
		if stat.parent == root {
			return true
		}
		if stat.parent == pid {
			return false
		}
		pid = stat.parent
	}
	return false
}

// Pure, uncached and credential-free. Read only process stat/comm/cmdline/cwd;
// never adopt session identity, inspect environ/transcripts, or fire callbacks.
func probeConditionalSubmitProcessAt(ctx context.Context, procRoot string, root int) (conditionalSubmitProcess, error) {
	if err := ctx.Err(); err != nil {
		return conditionalSubmitProcess{}, err
	}
	rootDir := filepath.Join(procRoot, strconv.Itoa(root))
	rootStat, err := readConditionalProcStat(rootDir)
	if err != nil {
		return conditionalSubmitProcess{}, errConditionalProcessUnavailable
	}
	entries, err := os.ReadDir(procRoot)
	if err != nil {
		return conditionalSubmitProcess{}, errConditionalProcessUnavailable
	}
	stats := make(map[int]conditionalProcStat)
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return conditionalSubmitProcess{}, err
		}
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || !entry.IsDir() {
			continue
		}
		if stat, err := readConditionalProcStat(filepath.Join(procRoot, entry.Name())); err == nil {
			stats[pid] = stat
		}
	}
	var found conditionalSubmitProcess
	for pid, stat := range stats {
		if err := ctx.Err(); err != nil {
			return conditionalSubmitProcess{}, err
		}
		if pid == root || !conditionalDescendsFrom(pid, root, stats) {
			continue
		}
		dir := filepath.Join(procRoot, strconv.Itoa(pid))
		comm, err := os.ReadFile(filepath.Join(dir, "comm"))
		if err != nil {
			return conditionalSubmitProcess{}, errConditionalProcessUnavailable
		}
		provider := strings.TrimSpace(string(comm))
		if (provider != "claude" && provider != "codex") || stat.tty == 0 || stat.group <= 0 || stat.group != stat.foreground {
			continue
		}
		argv, err := os.ReadFile(filepath.Join(dir, "cmdline"))
		if err != nil || len(argv) == 0 {
			return conditionalSubmitProcess{}, errConditionalProcessUnavailable
		}
		args := strings.Split(strings.TrimRight(string(argv), "\x00"), "\x00")
		if provider == "codex" && len(args) > 1 && args[1] == "app-server" {
			continue // a local remote-TUI server does not own the input composer
		}
		if provider == "claude" && isHeadlessClaudeCmd(args) {
			continue
		}
		if found.Pid != 0 {
			return conditionalSubmitProcess{}, errConditionalProcessUnavailable
		}
		cwd, err := filepath.EvalSymlinks(filepath.Join(dir, "cwd"))
		if err != nil {
			return conditionalSubmitProcess{}, errConditionalProcessUnavailable
		}
		info, err := os.Stat(cwd)
		if err != nil || !info.IsDir() {
			return conditionalSubmitProcess{}, errConditionalProcessUnavailable
		}
		found = conditionalSubmitProcess{Pid: pid, Provider: provider, StartTime: stat.start, Cwd: cwd}
	}
	if found.Pid == 0 {
		return found, errConditionalProcessUnavailable
	}
	// Revalidate the ancestry and generation after filesystem probes. We cannot
	// freeze exit/exec after this check, so transport_written is never receipt.
	for pid, steps := found.Pid, 0; pid != root; steps++ {
		if err := ctx.Err(); err != nil {
			return conditionalSubmitProcess{}, err
		}
		if steps >= 128 {
			return conditionalSubmitProcess{}, errConditionalProcessUnavailable
		}
		current, err := readConditionalProcStat(filepath.Join(procRoot, strconv.Itoa(pid)))
		prior, exists := stats[pid]
		if err != nil || !exists || current.parent != prior.parent || current.start != prior.start ||
			current.group != prior.group || current.foreground != prior.foreground || current.tty != prior.tty {
			return conditionalSubmitProcess{}, errConditionalProcessUnavailable
		}
		pid = current.parent
	}
	currentRoot, err := readConditionalProcStat(rootDir)
	if err != nil || currentRoot.start != rootStat.start {
		return conditionalSubmitProcess{}, errConditionalProcessUnavailable
	}
	return found, nil
}
