//go:build linux

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
)

func openTurnProviderSource(p *Pane) *turnProviderSource {
	if p.Pid <= 0 {
		return nil
	}
	pids := snapshotProcessTree().descendantPids(p.Pid)
	if source := openTurnProviderSourceAt("/proc", p.ID, p.Pid, pids); source != nil {
		return source
	}
	known := p.Snapshot()
	source := openClaudeTurnFallbackAt("/proc", p.ID, p.Pid, pids, known.ClaudeSessionID, known.ClaudeTranscriptPath)
	if source != nil {
		valid := source.valid
		source.valid = func() bool {
			live := p.Snapshot()
			return live.ClaudeSessionID == known.ClaudeSessionID && live.ClaudeTranscriptPath == known.ClaudeTranscriptPath && valid()
		}
	}
	return source
}

// Reuse established foreground/pane/rollout ownership discovery. PID start
// ticks and live ancestry additionally prevent a cached process-tree entry from
// binding a reused PID. Never discover by directory mtime or a pane's old ID.
func openTurnProviderSourceAt(procRoot, paneID string, root int, pids []int) *turnProviderSource {
	rootGeneration := turnProcessGeneration(filepath.Join(procRoot, strconv.Itoa(root)))
	if rootGeneration == "" {
		return nil
	}
	var codex codexIdentityProbe
	var museID string
	for _, pid := range pids {
		comm := providerProcessNameAt(procRoot, pid)
		if comm == "codex" {
			codex = discoverCodexIdentityAt(procRoot, paneID, pids)
			break
		}
	}
	for _, pid := range pids {
		if isMuseProcessName(strings.TrimSpace(readProcFile(filepath.Join(procRoot, strconv.Itoa(pid)), "comm"))) {
			museID, _ = discoverMuseIdentityAt(procRoot, paneID, pids)
			break
		}
	}
	var selected *turnProviderSource
	for _, pid := range pids {
		dir := filepath.Join(procRoot, strconv.Itoa(pid))
		comm := providerProcessNameAt(procRoot, pid)
		agent := ""
		switch {
		case comm == "codex" && codex.threadID != "":
			agent = agentCodex
		case comm == "claude":
			agent = agentClaude
		case isMuseProcessName(comm) && museID != "":
			agent = agentMuse
		}
		if agent == "" || !codexProcessIsForeground(dir) || !turnProcessDescends(procRoot, pid, root) {
			continue
		}
		env, ok := readProcEnvironAt(dir)
		generation := turnProcessGeneration(dir)
		if !ok || env["SOLU_PANE_ID"] != paneID || generation == "" {
			continue
		}
		descriptorSet := turnDescriptorSet(dir)
		if descriptorSet == "" {
			continue
		}
		command := readProcFile(dir, "cmdline")
		processCwd, _ := os.Readlink(filepath.Join(dir, "cwd"))
		entries, err := os.ReadDir(filepath.Join(dir, "fd"))
		if err != nil || len(entries) > codexProcessFDMax {
			continue
		}
		for _, entry := range entries {
			fdPath := filepath.Join(dir, "fd", entry.Name())
			target, err := os.Readlink(fdPath)
			if err != nil || strings.HasSuffix(target, " (deleted)") {
				continue
			}
			path, session := "", ""
			switch agent {
			case agentCodex:
				id, ok := rolloutFilenameThreadID(filepath.Base(target))
				if ok && id == codex.threadID {
					home := env["CODEX_HOME"]
					if home == "" && filepath.IsAbs(env["HOME"]) {
						home = filepath.Join(env["HOME"], ".codex")
					}
					root, rootErr := filepath.EvalSymlinks(filepath.Join(home, "sessions"))
					cwd, cwdErr := os.Readlink(filepath.Join(dir, "cwd"))
					resume, _ := codexResumeThreadAt(dir)
					validated, valid := validatedCodexRootMeta(fdPath, cwd, resume)
					if filepath.IsAbs(home) && rootErr == nil && cwdErr == nil && pathWithin(target, root) && valid && validated == id {
						path, session = fdPath, id
					}
				}
			case agentClaude:
				config := env["CLAUDE_CONFIG_DIR"]
				if config == "" && filepath.IsAbs(env["HOME"]) {
					config = filepath.Join(env["HOME"], ".claude")
				}
				if !filepath.IsAbs(config) {
					continue
				}
				config, _ = filepath.EvalSymlinks(config)
				rel, err := filepath.Rel(filepath.Join(config, "projects"), target)
				parts := strings.Split(filepath.ToSlash(rel), "/")
				id, ok := canonicalCodexThreadID(strings.TrimSuffix(filepath.Base(target), ".jsonl"))
				if err == nil && len(parts) == 2 && parts[0] != ".." && ok && filepath.Ext(target) == ".jsonl" {
					path, session = fdPath, id
				}
			case agentMuse:
				if filepath.Base(target) == ".session.lock" && filepath.Base(filepath.Dir(target)) == museID {
					path, session = filepath.Join(filepath.Dir(target), "session.jsonl"), museID
				}
			}
			if path == "" {
				continue
			}
			// O_NONBLOCK prevents a racing descriptor/path replacement with a FIFO
			// from blocking the tracker before the regular-file check.
			file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
			if err != nil {
				continue
			}
			info, err := file.Stat()
			if err != nil || !info.Mode().IsRegular() {
				file.Close()
				continue
			}
			sourcePath := path
			source := &turnProviderSource{pid: pid, file: file, agent: agent, session: session, key: fmt.Sprintf("%s:%s:%s:%s", rootGeneration, generation, agent, session)}
			source.valid = func() bool {
				if turnDescriptorSet(dir) != descriptorSet || readProcFile(dir, "cmdline") != command {
					return false
				}
				if cwd, _ := os.Readlink(filepath.Join(dir, "cwd")); cwd != processCwd {
					return false
				}
				if procRoot == "/proc" && !slices.Equal(snapshotProcessTree().descendantPids(root), pids) {
					return false
				}
				if strings.TrimSpace(readProcFile(dir, "comm")) != comm || turnProcessGeneration(dir) != generation || turnProcessGeneration(filepath.Join(procRoot, strconv.Itoa(root))) != rootGeneration || !turnProcessDescends(procRoot, pid, root) || !codexProcessIsForeground(dir) {
					return false
				}
				env, ok := readProcEnvironAt(dir)
				if !ok || env["SOLU_PANE_ID"] != paneID {
					return false
				}
				after, err := os.Stat(sourcePath)
				return err == nil && os.SameFile(info, after)
			}
			source.reopen = func() *turnProviderSource {
				if !source.valid() {
					return nil
				}
				reopened, err := os.OpenFile(sourcePath, os.O_RDONLY|syscall.O_NONBLOCK, 0)
				if err != nil {
					return nil
				}
				next := *source
				next.file = reopened
				return &next
			}
			if !source.valid() {
				file.Close()
				continue
			}
			if selected != nil {
				// Multiple distinct main sources are ambiguous. Duplicate FDs to the
				// same inode/session are harmless, as with a paired remote Codex server.
				prior, _ := selected.file.Stat()
				if prior != nil && selected.agent == source.agent && selected.session == source.session && os.SameFile(prior, info) {
					file.Close()
					continue
				}
				selected.file.Close()
				file.Close()
				return nil
			}
			selected = source
		}
	}
	return selected
}

func turnProcessGeneration(dir string) string {
	raw := readProcFile(dir, "stat")
	end := strings.LastIndex(raw, ")")
	if end < 0 {
		return ""
	}
	fields := strings.Fields(raw[end+1:])
	if len(fields) < 20 {
		return ""
	}
	return filepath.Base(dir) + ":" + fields[19]
}
func turnProcessDescends(procRoot string, pid, root int) bool {
	for i := 0; i < 128 && pid > 0; i++ {
		if pid == root {
			return true
		}
		raw := readProcFile(filepath.Join(procRoot, strconv.Itoa(pid)), "stat")
		end := strings.LastIndex(raw, ")")
		if end < 0 {
			return false
		}
		fields := strings.Fields(raw[end+1:])
		if len(fields) < 2 {
			return false
		}
		parent, err := strconv.Atoi(fields[1])
		if err != nil || parent == pid {
			return false
		}
		pid = parent
	}
	return false
}

// Stable descriptors let subsequent ticks reuse an already proved source,
// while any FD/process-tree/argv/foreground change forces full discovery.
func turnDescriptorSet(dir string) string {
	entries, err := os.ReadDir(filepath.Join(dir, "fd"))
	if err != nil || len(entries) > codexProcessFDMax {
		return ""
	}
	var out strings.Builder
	for _, entry := range entries {
		target, err := os.Readlink(filepath.Join(dir, "fd", entry.Name()))
		if err != nil {
			return ""
		}
		out.WriteString(entry.Name())
		out.WriteByte(0)
		out.WriteString(target)
		out.WriteByte(0)
		if out.Len() > 65536 {
			return ""
		}
	}
	return out.String()
}

// A closed transcript can be read only when the exact live CLI argv and the
// pane's current session agree. A remembered hook/session or cwd alone cannot
// select a file. Unknown/picker/continue argv remains explicitly unavailable.
func openClaudeTurnFallbackAt(procRoot, paneID string, root int, pids []int, knownID, knownPath string) *turnProviderSource {
	session, ok := canonicalCodexThreadID(knownID)
	if !ok {
		return nil
	}
	rootGeneration := turnProcessGeneration(filepath.Join(procRoot, strconv.Itoa(root)))
	if rootGeneration == "" {
		return nil
	}
	candidate := ""
	pid := 0
	for _, id := range pids {
		dir := filepath.Join(procRoot, strconv.Itoa(id))
		if providerProcessNameAt(procRoot, id) != "claude" || !codexProcessIsForeground(dir) {
			continue
		}
		if candidate != "" {
			return nil
		}
		candidate, pid = dir, id
	}
	if candidate == "" || !turnProcessDescends(procRoot, pid, root) {
		return nil
	}
	env, ok := readProcEnvironAt(candidate)
	if !ok || env["SOLU_PANE_ID"] != paneID {
		return nil
	}
	command := readProcFile(candidate, "cmdline")
	argv := strings.Split(strings.TrimRight(command, "\x00"), "\x00")
	argID := ""
	for i, arg := range argv {
		if (arg == "--resume" || arg == "--session-id" || arg == "-r") && i+1 < len(argv) {
			id, valid := canonicalCodexThreadID(argv[i+1])
			if !valid || argID != "" && argID != id {
				return nil
			}
			argID = id
		}
		if strings.HasPrefix(arg, "--resume=") || strings.HasPrefix(arg, "--session-id=") {
			_, raw, _ := strings.Cut(arg, "=")
			id, valid := canonicalCodexThreadID(raw)
			if !valid || argID != "" && argID != id {
				return nil
			}
			argID = id
		}
	}
	if argID != session {
		return nil
	}
	generation := turnProcessGeneration(candidate)
	config := env["CLAUDE_CONFIG_DIR"]
	if config == "" && filepath.IsAbs(env["HOME"]) {
		config = filepath.Join(env["HOME"], ".claude")
	}
	if generation == "" || !filepath.IsAbs(config) {
		return nil
	}
	projects, err := filepath.EvalSymlinks(filepath.Join(config, "projects"))
	if err != nil {
		return nil
	}
	cwd, err := os.Readlink(filepath.Join(candidate, "cwd"))
	if err != nil {
		return nil
	}
	path := knownPath
	if path == "" {
		path = filepath.Join(projects, claudeProjectDirName(cwd), session+".jsonl")
	}
	path, err = filepath.EvalSymlinks(path)
	if err != nil {
		return nil
	}
	rel, err := filepath.Rel(projects, path)
	if err != nil || len(strings.Split(filepath.ToSlash(rel), "/")) != 2 || !pathWithin(path, projects) || filepath.Base(path) != session+".jsonl" {
		return nil
	}
	// Refuse a conflicting open main transcript; argv can outlive /clear.
	descriptorSet := turnDescriptorSet(candidate)
	if descriptorSet == "" {
		return nil
	}
	entries, err := os.ReadDir(filepath.Join(candidate, "fd"))
	if err != nil {
		return nil
	}
	for _, entry := range entries {
		target, _ := os.Readlink(filepath.Join(candidate, "fd", entry.Name()))
		if pathWithin(target, projects) && isMainTranscriptPath(target) && target != path {
			return nil
		}
	}
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		file.Close()
		return nil
	}
	source := &turnProviderSource{pid: pid, file: file, key: fmt.Sprintf("%s:%s:claude:%s", rootGeneration, generation, session), agent: agentClaude, session: session}
	source.valid = func() bool {
		if turnProcessGeneration(candidate) != generation || turnProcessGeneration(filepath.Join(procRoot, strconv.Itoa(root))) != rootGeneration || !turnProcessDescends(procRoot, pid, root) || !codexProcessIsForeground(candidate) || strings.TrimSpace(readProcFile(candidate, "comm")) != "claude" || readProcFile(candidate, "cmdline") != command || turnDescriptorSet(candidate) != descriptorSet {
			return false
		}
		if procRoot == "/proc" && !slices.Equal(snapshotProcessTree().descendantPids(root), pids) {
			return false
		}
		current, ok := readProcEnvironAt(candidate)
		if !ok || current["SOLU_PANE_ID"] != paneID || current["HOME"] != env["HOME"] || current["CLAUDE_CONFIG_DIR"] != env["CLAUDE_CONFIG_DIR"] {
			return false
		}
		if currentCwd, _ := os.Readlink(filepath.Join(candidate, "cwd")); currentCwd != cwd {
			return false
		}
		currentInfo, statErr := os.Stat(path)
		return statErr == nil && os.SameFile(info, currentInfo)
	}
	source.reopen = func() *turnProviderSource {
		if !source.valid() {
			return nil
		}
		file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
		if err != nil {
			return nil
		}
		next := *source
		next.file = file
		return &next
	}
	if !source.valid() {
		file.Close()
		return nil
	}
	return source
}

func turnFileIdentity(info os.FileInfo) string {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return ""
	}
	return fmt.Sprintf("%d:%d", stat.Dev, stat.Ino)
}
