package main

import (
	"fmt"
	"strings"
)

const (
	agentClaude = "claude"
	agentCodex  = "codex"
	agentMuse   = "muse"
)

// normalizeAgentKind keeps Claude as the zero value for backwards-compatible
// JSON/persistence while making non-Claude agents durable. An omitted agent is
// inferred when the pane's direct command is unambiguously Codex, or when a
// durable Codex thread id is present: every writer that sets CodexThreadID
// stamps Agent=codex in the same mutation, and handing the pane back to Claude
// clears the thread id (SetForegroundClaudeIdentity) — so an empty agent
// paired with a canonical thread id can only mean the agent field was lost
// (the same field-loss class healLostNativeKind repairs for Kind). Without
// this heal such a pane would resume as Claude via its retained stale
// ClaudeSessionID — the wrong conversation on the wrong provider. The web
// mirror (effectivePaneAgent, web/src/lib/agentKind.ts) already treats
// codexThreadId as a Codex signal; this keeps the daemon in agreement. An
// EXPLICIT raw="claude" still wins: only the omitted-agent case is healed.
func normalizeAgentKind(raw string, cmd []string, codexThreadID string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", agentClaude:
		if strings.TrimSpace(raw) == "" {
			if cmdStartsCodex(cmd) {
				return agentCodex, true
			}
			if cmdStartsMuse(cmd) {
				return agentMuse, true
			}
			if _, ok := canonicalCodexThreadID(codexThreadID); ok && !cmdRunsClaude(cmd) && !cmdStartsMuse(cmd) {
				return agentCodex, true
			}
		}
		return "", true
	case agentCodex:
		return agentCodex, true
	case agentMuse:
		return agentMuse, true
	default:
		return "", false
	}
}

func effectiveAgentKind(agent string) string {
	if strings.TrimSpace(agent) == agentCodex {
		return agentCodex
	}
	if strings.TrimSpace(agent) == agentMuse {
		return agentMuse
	}
	return agentClaude
}

// conversationIDForAgent returns the durable conversation identity for the
// selected agent without ever crossing the two providers' persistence domains.
// Claude's zero-value agent keeps the legacy session id; Codex uses its app-
// server thread id.
func conversationIDForAgent(agent, claudeSessionID, codexThreadID, museSessionID string) string {
	switch effectiveAgentKind(agent) {
	case agentCodex:
		return strings.TrimSpace(codexThreadID)
	case agentMuse:
		return strings.TrimSpace(museSessionID)
	}
	return strings.TrimSpace(claudeSessionID)
}

// canonicalMuseSessionID validates a Muse session id. Muse ids are plain
// UUIDs (the daemon mints them for native panes via `muse exec
// --session-id`), so the Codex UUID validator is the same shape check.
func canonicalMuseSessionID(raw string) (string, bool) {
	return canonicalCodexThreadID(raw)
}

func cmdStartsCodex(cmd []string) bool {
	if len(cmd) == 0 {
		return false
	}
	name := strings.TrimSpace(cmd[0])
	name = strings.ReplaceAll(name, `\`, "/")
	if i := strings.LastIndexByte(name, '/'); i >= 0 {
		name = name[i+1:]
	}
	name = strings.ToLower(name)
	return name == "codex" || name == "codex.exe" || name == "codex.cmd" || name == "codex.bat"
}

func cmdStartsClaude(cmd []string) bool {
	if len(cmd) == 0 {
		return false
	}
	name := strings.TrimSpace(cmd[0])
	name = strings.ReplaceAll(name, `\`, "/")
	if i := strings.LastIndexByte(name, '/'); i >= 0 {
		name = name[i+1:]
	}
	name = strings.ToLower(name)
	return name == "claude" || name == "claude.exe" || name == "claude.cmd"
}

// cmdRunsClaude is the LENIENT twin of cmdStartsClaude: it also accepts the
// login-shell-wrapped form tryLoginShellWrap emits (`bash -lc "…; 'claude' …"`,
// where the binary is a single-quoted token inside one element) — mirroring how
// stripCodexResumeArgv recognizes `'codex'`. Used by the HTTP-create and
// MCP-spawn role validation, so a workspace def-restore replaying a wrapped
// terminal-claude cmd with role=orchestrator is accepted instead of 400ing.
func cmdRunsClaude(cmd []string) bool {
	if cmdStartsClaude(cmd) {
		return true
	}
	for _, el := range cmd {
		if strings.Contains(el, "'claude'") {
			return true
		}
	}
	return false
}

// cmdRunsCodex is the lenient twin of cmdStartsCodex. It recognizes the
// login-shell-wrapped argv emitted by tryLoginShellWrap.
func cmdRunsCodex(cmd []string) bool {
	if cmdStartsCodex(cmd) {
		return true
	}
	for _, el := range cmd {
		if strings.Contains(el, "'codex'") {
			return true
		}
	}
	return false
}

func cmdStartsMuse(cmd []string) bool {
	if len(cmd) == 0 {
		return false
	}
	name := strings.TrimSpace(cmd[0])
	name = strings.ReplaceAll(name, `\`, "/")
	if i := strings.LastIndexByte(name, '/'); i >= 0 {
		name = name[i+1:]
	}
	name = strings.ToLower(name)
	return name == "muse" || name == "muse.exe"
}

func agentFromCmd(cmd []string) string {
	if cmdRunsClaude(cmd) {
		return agentClaude
	}
	if cmdRunsCodex(cmd) {
		return agentCodex
	}
	if cmdStartsMuse(cmd) {
		return agentMuse
	}
	for _, el := range cmd {
		if strings.Contains(el, "'muse'") {
			return agentMuse
		}
	}
	return ""
}

type paneIdentity struct {
	Agent                string
	Role                 string
	Kind                 string
	Cmd                  []string
	ClaudeSessionID      string
	ClaudeTranscriptPath string
	CodexThreadID        string
	MuseSessionID        string
	HarnessSessionID     string
	HarnessProvider      string
	CodexTurnInFlight    bool
}

// healPaneIdentity reconciles persisted identity using corroborated
// conversation metadata when an explicit agent disagrees with its spawn
// recipe. A corroborated explicit identity wins because foreground discovery
// or a resume writer may have stamped it after argv was recorded; otherwise
// the unambiguous command wins. Conversation pointers are never dropped, so a
// repair cannot destroy a resumable conversation for another provider.
func healPaneIdentity(in paneIdentity) (paneIdentity, []string) {
	out := in
	explicit := strings.ToLower(strings.TrimSpace(in.Agent))
	fromCmd := agentFromCmd(in.Cmd)
	corroborated := func(agent string) bool {
		switch agent {
		case agentCodex:
			_, ok := canonicalCodexThreadID(in.CodexThreadID)
			return ok
		case agentMuse:
			_, ok := canonicalMuseSessionID(in.MuseSessionID)
			return ok
		case agentClaude:
			return strings.TrimSpace(in.ClaudeSessionID) != ""
		default:
			return false
		}
	}

	agent := explicit
	var notes []string
	switch explicit {
	case "":
		agent = fromCmd
		if agent == "" {
			agent, _ = normalizeAgentKind("", in.Cmd, in.CodexThreadID)
		}
		if agent != "" && agent != agentClaude {
			notes = append(notes, fmt.Sprintf("agent was omitted — inferred %q", agent))
		}
	case agentClaude, agentCodex, agentMuse:
		if fromCmd != "" && fromCmd != explicit && !corroborated(explicit) {
			agent = fromCmd
			notes = append(notes, fmt.Sprintf("agent %q disagreed with command %q and has no conversation of its own — command wins", explicit, in.Cmd))
		}
	default:
		notes = append(notes, fmt.Sprintf("unknown agent %q was preserved", explicit))
	}

	if strings.TrimSpace(in.Role) != "" && !orchestratorHostSupported(agent) {
		notes = append(notes, fmt.Sprintf("cleared role %q: %s", in.Role, orchestratorHostUnsupportedReason(agent)))
		out.Role = ""
	}
	if agent != agentCodex && out.CodexTurnInFlight {
		notes = append(notes, fmt.Sprintf("cleared Codex turn-in-flight flag for %s pane", orchestratorHostLabel(agent)))
		out.CodexTurnInFlight = false
	}
	if agent == agentClaude {
		agent = ""
	}
	out.Agent = agent
	return out, notes
}

func healPersistedPane(info PersistedPane) (PersistedPane, []string) {
	h, notes := healPaneIdentity(paneIdentity{
		Agent: info.Agent, Role: info.Role, Kind: info.Kind, Cmd: info.Cmd,
		ClaudeSessionID: info.ClaudeSessionID, ClaudeTranscriptPath: info.ClaudeTranscriptPath,
		CodexThreadID: info.CodexThreadID, MuseSessionID: info.MuseSessionID,
		HarnessSessionID: info.HarnessSessionID, HarnessProvider: info.HarnessProvider,
		CodexTurnInFlight: info.CodexTurnInFlight,
	})
	info.Agent, info.Role, info.Kind, info.Cmd = h.Agent, h.Role, h.Kind, h.Cmd
	info.ClaudeSessionID, info.ClaudeTranscriptPath = h.ClaudeSessionID, h.ClaudeTranscriptPath
	info.CodexThreadID, info.MuseSessionID = h.CodexThreadID, h.MuseSessionID
	info.HarnessSessionID, info.HarnessProvider = h.HarnessSessionID, h.HarnessProvider
	info.CodexTurnInFlight = h.CodexTurnInFlight
	return info, notes
}

func healSpawnOpts(opts SpawnOpts) (SpawnOpts, []string) {
	h, notes := healPaneIdentity(paneIdentity{
		Agent: opts.Agent, Role: opts.Role, Kind: opts.Kind, Cmd: opts.Cmd,
		ClaudeSessionID: opts.ClaudeSessionID, ClaudeTranscriptPath: opts.ClaudeTranscriptPath,
		CodexThreadID: opts.CodexThreadID, MuseSessionID: opts.MuseSessionID,
		HarnessSessionID: opts.HarnessSessionID, HarnessProvider: opts.HarnessProvider,
		CodexTurnInFlight: opts.CodexTurnInFlight,
	})
	opts.Agent, opts.Role, opts.Kind, opts.Cmd = h.Agent, h.Role, h.Kind, h.Cmd
	opts.ClaudeSessionID, opts.ClaudeTranscriptPath = h.ClaudeSessionID, h.ClaudeTranscriptPath
	opts.CodexThreadID, opts.MuseSessionID = h.CodexThreadID, h.MuseSessionID
	opts.HarnessSessionID, opts.HarnessProvider = h.HarnessSessionID, h.HarnessProvider
	opts.CodexTurnInFlight = h.CodexTurnInFlight
	return opts, notes
}

func healPaneSnapshot(snap PaneSnapshot) (PaneSnapshot, []string) {
	h, notes := healPaneIdentity(paneIdentity{
		Agent: snap.Agent, Role: snap.Role, Kind: snap.Kind, Cmd: snap.Cmd,
		ClaudeSessionID: snap.ClaudeSessionID, ClaudeTranscriptPath: snap.ClaudeTranscriptPath,
		CodexThreadID: snap.CodexThreadID, MuseSessionID: snap.MuseSessionID,
		HarnessSessionID: snap.HarnessSessionID, HarnessProvider: snap.HarnessProvider,
		CodexTurnInFlight: snap.CodexTurnInFlight,
	})
	snap.Agent, snap.Role, snap.Kind, snap.Cmd = h.Agent, h.Role, h.Kind, h.Cmd
	snap.ClaudeSessionID, snap.ClaudeTranscriptPath = h.ClaudeSessionID, h.ClaudeTranscriptPath
	snap.CodexThreadID, snap.MuseSessionID = h.CodexThreadID, h.MuseSessionID
	snap.HarnessSessionID, snap.HarnessProvider = h.HarnessSessionID, h.HarnessProvider
	snap.CodexTurnInFlight = h.CodexTurnInFlight
	return snap, notes
}
