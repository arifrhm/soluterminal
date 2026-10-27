//go:build !windows

package main

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const (
	codexSessionMetaMaxBytes = 64 * 1024
	codexProcessFDMax        = 1024
	codexSessionFDMax        = 16
	codexForegroundProcMax   = 4
)

type codexSessionMeta struct {
	Type    string `json:"type"`
	Payload struct {
		ID             string          `json:"id"`
		SessionID      string          `json:"session_id"`
		Cwd            string          `json:"cwd"`
		Originator     string          `json:"originator"`
		Source         json.RawMessage `json:"source"`
		ParentThreadID *string         `json:"parent_thread_id"`
		ForkedFromID   *string         `json:"forked_from_id"`
		ThreadSource   string          `json:"thread_source"`
	} `json:"payload"`
}

// discoverCodexThreadID returns the one exact root thread owned by the
// foreground Codex process in this pane. active distinguishes "no foreground
// Codex" from an active Codex whose identity is not yet available or is
// ambiguous; callers use the latter to suppress a stale previous thread.
func discoverCodexThreadID(paneID string, pids []int) (threadID string, active bool) {
	probe := discoverCodexIdentityAt("/proc", paneID, pids)
	return probe.threadID, probe.active
}

func discoverCodexIdentity(paneID string, pids []int) codexIdentityProbe {
	return discoverCodexIdentityAt("/proc", paneID, pids)
}

// discoverCodexThreadIDAt is the testable implementation. Ownership is bound
// by all of: pane inheritance, foreground process group, exact native comm,
// and either an explicit resume argv or an open, validated root rollout file.
func discoverCodexThreadIDAt(procRoot, paneID string, pids []int) (threadID string, active bool) {
	probe := discoverCodexIdentityAt(procRoot, paneID, pids)
	return probe.threadID, probe.active
}

// discoverCodexIdentityAt preserves the important distinction hidden by the
// legacy (threadID, active) API: an empty id can mean either a verified virgin
// Codex (safe to move to Focus with thread/start), an explicit resume whose
// rollout is still opening (safe after exact ownership checks), or an
// unreadable/ambiguous process (unsafe). Callers use fresh/resumeThreadID only
// after every ownership and FD check below succeeds.
func discoverCodexIdentityAt(procRoot, paneID string, pids []int) (probe codexIdentityProbe) {
	if live, handled := discoverCodexRemoteIdentityAt(procRoot, paneID, pids); handled {
		return live
	}
	candidates := map[string]struct{}{}
	activePidDirs := make([]string, 0, 1)
	verifiedFresh := false
	provisionalResumeID := ""
	unsafe := false
	for _, pid := range pids {
		pidDir := filepath.Join(procRoot, strconv.Itoa(pid))
		// pids are already descendants of the pane root. Foreground presence is
		// enough to invalidate a stale previous thread even when environ is
		// unreadable; environ is still required before accepting a new identity.
		if providerProcessNameAt(procRoot, pid) != "codex" || !codexProcessIsForeground(pidDir) {
			continue
		}
		probe.active = true
		activePidDirs = append(activePidDirs, pidDir)
		if len(activePidDirs) > codexForegroundProcMax {
			probe.blocked = true
			return probe
		}
		env, ok := readProcEnvironAt(pidDir)
		if !ok || env["SOLU_PANE_ID"] != paneID {
			unsafe = true
			continue
		}
		resumeID, explicitResume := codexResumeThreadAt(pidDir)
		openIDs, inspected, hasRollout := codexOpenRootThreadsAt(pidDir, env, resumeID)
		if !inspected {
			unsafe = true
			continue
		}
		if explicitResume {
			// Once the matching rollout is open it is fully proven. Before that,
			// expose the explicit id as provisional to the pane-level caller. The
			// caller has already verified pane ownership and foreground identity;
			// the resume/restart operation performs the durable rollout validation.
			if resumeID == "" && len(openIDs) == 1 {
				// `resume --last` and picker forms are not identifiable from
				// argv, but one validated open root rollout still proves them.
				candidates[openIDs[0]] = struct{}{}
			} else if resumeID != "" && len(openIDs) == 1 && openIDs[0] == resumeID {
				candidates[resumeID] = struct{}{}
			} else if resumeID != "" && len(openIDs) == 0 && !hasRollout {
				provisionalResumeID = resumeID
			} else {
				unsafe = true
			}
			continue
		}
		if len(openIDs) == 0 && !hasRollout {
			verifiedFresh = true
			continue
		}
		if len(openIDs) == 0 {
			unsafe = true
			continue
		}
		for _, id := range openIDs {
			candidates[id] = struct{}{}
		}
	}
	// The bounded FD reads above are the slow portion. Revalidate ownership at
	// commit time so a Codex that exited or moved to the background while we
	// scanned cannot overwrite the identity of a newly foregrounded provider.
	for _, pidDir := range activePidDirs {
		if !codexProcessIsForeground(pidDir) {
			return codexIdentityProbe{}
		}
		env, ok := readProcEnvironAt(pidDir)
		if !ok || env["SOLU_PANE_ID"] != paneID {
			probe.blocked = true
			return probe
		}
	}
	if unsafe || len(activePidDirs) != 1 {
		probe.blocked = probe.active
		return probe
	}
	if len(candidates) == 1 && !verifiedFresh && provisionalResumeID == "" {
		for id := range candidates {
			probe.threadID = id
			return probe
		}
	}
	if len(candidates) == 0 && verifiedFresh && provisionalResumeID == "" {
		probe.fresh = true
		return probe
	}
	if len(candidates) == 0 && !verifiedFresh && provisionalResumeID != "" {
		probe.resumeThreadID = provisionalResumeID
		return probe
	}
	probe.blocked = probe.active
	return probe
}

// A local remote TUI and its app-server are two processes for ONE conversation.
// The TUI carries the resume argv; the server owns the rollout file descriptors.
// Treating them as two unrelated terminals hides the durable identity forever.
// Only combine a unique, foreground, same-pane pair naming the exact same Unix
// socket. A server alone (including the startup interval) is never a fresh TUI.
func discoverCodexRemoteIdentityAt(procRoot, paneID string, pids []int) (codexIdentityProbe, bool) {
	type process struct {
		dir  string
		argv []string
		env  map[string]string
	}
	var foreground []process
	hasServer := false
	for _, pid := range pids {
		dir := filepath.Join(procRoot, strconv.Itoa(pid))
		if providerProcessNameAt(procRoot, pid) != "codex" || !codexProcessIsForeground(dir) {
			continue
		}
		argv := strings.Split(strings.TrimRight(readProcFile(dir, "cmdline"), "\x00"), "\x00")
		if len(argv) > 1 && argv[1] == "app-server" {
			hasServer = true
		}
		env, _ := readProcEnvironAt(dir)
		foreground = append(foreground, process{dir: dir, argv: argv, env: env})
		if len(foreground) > codexForegroundProcMax {
			return codexIdentityProbe{active: true, blocked: true}, true
		}
	}
	if !hasServer {
		return codexIdentityProbe{}, false
	}
	blocked := codexIdentityProbe{active: true, blocked: true}
	if len(foreground) != 2 {
		return blocked, true
	}
	server, client := foreground[0], foreground[1]
	if len(server.argv) < 2 || server.argv[1] != "app-server" {
		server, client = client, server
	}
	if len(client.argv) > 1 && client.argv[1] == "app-server" {
		return blocked, true
	}
	foregroundOwner := func(dir string) string {
		stat := readProcFile(dir, "stat")
		end := strings.LastIndex(stat, ")")
		if end < 0 {
			return ""
		}
		fields := strings.Fields(stat[end+1:])
		if len(fields) < 6 {
			return ""
		}
		return strings.Join(fields[2:6], ":") // process group, session, tty, foreground group
	}
	if owner := foregroundOwner(server.dir); owner == "" || owner != foregroundOwner(client.dir) {
		return blocked, true
	}
	endpoint := func(args []string, flag string) string {
		value := ""
		for i, arg := range args {
			if arg == flag {
				if value != "" || i+1 >= len(args) {
					return ""
				}
				value = args[i+1]
			} else if strings.HasPrefix(arg, flag+"=") {
				if value != "" {
					return ""
				}
				value = strings.TrimPrefix(arg, flag+"=")
			}
		}
		return value
	}
	listen, remote := endpoint(server.argv, "--listen"), endpoint(client.argv, "--remote")
	relayDir := server.env["SOLU_CODEX_RELAY_DIR"]
	relayPair := filepath.IsAbs(relayDir) && filepath.Clean(relayDir) == relayDir &&
		strings.HasPrefix(filepath.Base(relayDir), "solu-codex-live-") &&
		client.env["SOLU_CODEX_RELAY_DIR"] == relayDir &&
		listen == "unix://"+filepath.Join(relayDir, "app.sock") &&
		remote == "unix://"+filepath.Join(relayDir, "terminal.sock")
	if (listen != remote && !relayPair) || !strings.HasPrefix(listen, "unix:///") {
		return blocked, true
	}
	for _, process := range foreground {
		if process.env["SOLU_PANE_ID"] != paneID {
			return blocked, true
		}
	}
	if server.env["CODEX_HOME"] != client.env["CODEX_HOME"] || server.env["HOME"] != client.env["HOME"] {
		return blocked, true
	}
	resumeID, explicitResume := codexResumeThreadAt(client.dir)
	ids, inspected, hasRollout := codexOpenRootThreadsAt(server.dir, server.env, resumeID)
	if !inspected {
		return blocked, true
	}
	// Recheck both process owners after the filesystem scan, just as the
	// single-process discovery does. Never trust a stale status-file identity.
	for _, process := range foreground {
		env, ok := readProcEnvironAt(process.dir)
		argv := strings.Split(strings.TrimRight(readProcFile(process.dir, "cmdline"), "\x00"), "\x00")
		if !ok || env["SOLU_PANE_ID"] != paneID || env["CODEX_HOME"] != process.env["CODEX_HOME"] || env["HOME"] != process.env["HOME"] || env["SOLU_CODEX_RELAY_DIR"] != process.env["SOLU_CODEX_RELAY_DIR"] || !codexProcessIsForeground(process.dir) || strings.Join(argv, "\x00") != strings.Join(process.argv, "\x00") {
			return blocked, true
		}
	}
	if len(ids) == 1 && (!explicitResume || resumeID == "" || ids[0] == resumeID) {
		return codexIdentityProbe{active: true, threadID: ids[0]}, true
	}
	if len(ids) == 0 && !hasRollout {
		if explicitResume && resumeID != "" {
			return codexIdentityProbe{active: true, resumeThreadID: resumeID}, true
		}
		if !explicitResume {
			return codexIdentityProbe{active: true, fresh: true}, true
		}
	}
	return blocked, true
}

func paneDescendantIsForeground(pid int) bool {
	return codexProcessIsForeground(filepath.Join("/proc", strconv.Itoa(pid)))
}

func readProcFile(pidDir, name string) string {
	b, err := os.ReadFile(filepath.Join(pidDir, name))
	if err != nil {
		return ""
	}
	return string(b)
}

func readProcEnvironAt(pidDir string) (map[string]string, bool) {
	b, err := os.ReadFile(filepath.Join(pidDir, "environ"))
	if err != nil {
		return nil, false
	}
	out := make(map[string]string, 5)
	for _, entry := range strings.Split(string(b), "\x00") {
		if i := strings.IndexByte(entry, '='); i > 0 {
			key := entry[:i]
			if key == "SOLU_PANE_ID" || key == "CODEX_HOME" || key == "CLAUDE_CONFIG_DIR" || key == "HOME" || key == "XDG_DATA_HOME" || key == "SOLU_CODEX_RELAY_DIR" {
				out[key] = entry[i+1:]
			}
		}
	}
	return out, true
}

func codexProcessIsForeground(pidDir string) bool {
	b, err := os.ReadFile(filepath.Join(pidDir, "stat"))
	if err != nil {
		return false
	}
	s := string(b)
	rp := strings.LastIndex(s, ")")
	if rp < 0 || rp+1 >= len(s) {
		return false
	}
	f := strings.Fields(s[rp+1:])
	if len(f) < 6 {
		return false
	}
	pgrp, errPgrp := strconv.Atoi(f[2])
	tty, errTTY := strconv.Atoi(f[4])
	tpgid, errTpgid := strconv.Atoi(f[5])
	return errPgrp == nil && errTTY == nil && errTpgid == nil && tty != 0 && pgrp > 0 && pgrp == tpgid
}

func codexResumeThreadAt(pidDir string) (string, bool) {
	b, err := os.ReadFile(filepath.Join(pidDir, "cmdline"))
	if err != nil {
		return "", false
	}
	args := strings.Split(strings.TrimRight(string(b), "\x00"), "\x00")
	resumeAt := -1
	for i, arg := range args {
		if arg == "resume" {
			resumeAt = i
			break
		}
	}
	if resumeAt < 0 {
		return "", false
	}
	// Only the immediate positional shape is unambiguous. --last, picker,
	// option-bearing and UUID-shaped prompt/value forms are resolved from the
	// validated open rollout instead of guessed from argv.
	if resumeAt+1 >= len(args) {
		return "", true
	}
	id, ok := canonicalCodexThreadID(args[resumeAt+1])
	if !ok {
		return "", true
	}
	return id, true
}

func codexOpenRootThreadsAt(pidDir string, env map[string]string, resumeID string) ([]string, bool, bool) {
	codexHome := strings.TrimSpace(env["CODEX_HOME"])
	if codexHome == "" {
		home := strings.TrimSpace(env["HOME"])
		if home == "" {
			return nil, false, false
		}
		codexHome = filepath.Join(home, ".codex")
	}
	if !filepath.IsAbs(codexHome) {
		return nil, false, false
	}
	sessionsRoot := filepath.Join(filepath.Clean(codexHome), "sessions")
	resolvedSessionsRoot, err := filepath.EvalSymlinks(sessionsRoot)
	if err == nil {
		sessionsRoot = resolvedSessionsRoot
	} else if !os.IsNotExist(err) {
		return nil, false, false
	}
	processCwd, err := os.Readlink(filepath.Join(pidDir, "cwd"))
	if err != nil || !filepath.IsAbs(processCwd) {
		return nil, false, false
	}
	entries, err := os.ReadDir(filepath.Join(pidDir, "fd"))
	if err != nil || len(entries) > codexProcessFDMax {
		return nil, false, false
	}
	ids := map[string]struct{}{}
	sessionFDs := 0
	hasRollout := false
	for _, entry := range entries {
		fdPath := filepath.Join(pidDir, "fd", entry.Name())
		target, err := os.Readlink(fdPath)
		if err != nil || !pathWithin(target, sessionsRoot) {
			continue
		}
		sessionFDs++
		if sessionFDs > codexSessionFDMax {
			return nil, false, false
		}
		filenameID, ok := rolloutFilenameThreadID(filepath.Base(target))
		if !ok {
			continue
		}
		hasRollout = true
		info, err := os.Stat(fdPath)
		if err != nil || !info.Mode().IsRegular() {
			return nil, false, hasRollout
		}
		id, ok := validatedCodexRootMeta(fdPath, processCwd, resumeID)
		if !ok || id != filenameID {
			continue
		}
		ids[id] = struct{}{}
	}
	out := make([]string, 0, len(ids))
	for id := range ids {
		out = append(out, id)
	}
	return out, true, hasRollout
}

func pathWithin(path, root string) bool {
	if !filepath.IsAbs(path) || strings.HasSuffix(path, " (deleted)") {
		return false
	}
	rel, err := filepath.Rel(filepath.Clean(root), filepath.Clean(path))
	return err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func validatedCodexRootMeta(fdPath, processCwd, resumeID string) (string, bool) {
	f, err := os.Open(fdPath)
	if err != nil {
		return "", false
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 4096), codexSessionMetaMaxBytes)
	if !scanner.Scan() {
		return "", false
	}
	var meta codexSessionMeta
	if err := json.Unmarshal(scanner.Bytes(), &meta); err != nil || meta.Type != "session_meta" {
		return "", false
	}
	id, ok := canonicalCodexThreadID(meta.Payload.ID)
	sessionID, sessionOK := canonicalCodexThreadID(meta.Payload.SessionID)
	if !ok || !sessionOK || sessionID != id || !filepath.IsAbs(meta.Payload.Cwd) {
		return "", false
	}
	// An explicit `codex resume <id>` can keep the launcher's OS cwd while
	// restoring a conversation from another project. The exact argv id and
	// open root rollout prove ownership even when those directories differ.
	// Fresh/picker/--last discovery still requires the cwd match; every path
	// retains pane inheritance, foreground, filename/id and provenance checks.
	if filepath.Clean(meta.Payload.Cwd) != filepath.Clean(processCwd) && resumeID != id {
		return "", false
	}
	if meta.Payload.ParentThreadID != nil {
		return "", false
	}
	var source string
	if err := json.Unmarshal(meta.Payload.Source, &source); err != nil {
		return "", false
	}
	// Codex TUI roots and SOLU Focus app-server roots use different, stable
	// provenance. A Focus-created thread is later opened by `codex resume` when
	// switching to Terminal; rejecting its soluterminal/vscode metadata made the
	// scanner block the exact thread SOLU had just created. Ownership is still
	// bound by pane env, foreground pgrp, CODEX_HOME path, cwd, and canonical id.
	trustedTUIRoot := meta.Payload.Originator == "codex-tui" && source == "cli" && meta.Payload.ThreadSource == "user"
	trustedSoluRoot := meta.Payload.Originator == "soluterminal" && source == "vscode" && (meta.Payload.ThreadSource == "" || meta.Payload.ThreadSource == "user")
	// runCodexLiveTerminal initializes its paired app-server with this exact
	// client name. Codex persists that name as originator, even though the
	// foreground remote TUI owns the input. Unlike legacy Focus roots, require
	// an explicit user thread; all process/FD/socket ownership checks still apply.
	trustedLiveRoot := meta.Payload.Originator == "soluterminal_live_auth" && source == "vscode" && meta.Payload.ThreadSource == "user"
	if !trustedTUIRoot && !trustedSoluRoot && !trustedLiveRoot {
		return "", false
	}
	return id, true
}
