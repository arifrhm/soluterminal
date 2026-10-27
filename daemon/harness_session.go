package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// Managed terminal harnesses (the DeepSeek client today; future managed
// clients follow the same contract) do not expose their durable session id on
// argv or in a provider rollout the way Claude/Codex/Muse do. Instead the
// client writes a small per-pane sidecar keyed by the SOLU_PANE_ID the daemon
// exports into every pane. The daemon adopts that id so a harness pane resumes
// its conversation when its pane agent is replaced — the exact same
// durable-conversation contract the other providers already enjoy.
type harnessSessionDir struct {
	provider string
	dir      string
}

// harnessSessionDirs returns the per-provider sidecar directories under a
// daemon data dir. Keep this in lockstep with each client's recordPaneSession
// path: the DeepSeek terminal client and the managed Pi approval extension both
// implement the contract.
func harnessSessionDirs(dataDir string) []harnessSessionDir {
	dataDir = strings.TrimSpace(dataDir)
	if dataDir == "" {
		return nil
	}
	return []harnessSessionDir{
		{provider: "deepseek", dir: filepath.Join(dataDir, "harness-state", "deepseek", "pane-sessions")},
		{provider: "pi", dir: filepath.Join(dataDir, "harness-state", "pi", "pane-sessions")},
		{provider: "pi-glm", dir: filepath.Join(dataDir, "harness-state", "pi-glm", "pane-sessions")},
	}
}

// harnessSessionRecordName mirrors deepseek-terminal.mjs's paneSessionFile()
// sanitizer byte-for-byte for the ASCII pane ids the daemon mints.
func harnessSessionRecordName(paneID string) string {
	var b strings.Builder
	for _, r := range paneID {
		switch {
		case r >= 'A' && r <= 'Z', r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	name := b.String()
	if len(name) > 80 {
		name = name[:80]
	}
	return name
}

// SetHarnessSession records the durable session id a managed harness client
// adopted for this pane. Returns true when it changed, mirroring
// SetMuseSessionID, so the caller can persist exactly once.
func (p *Pane) SetHarnessSession(provider, sessionID string) bool {
	provider = strings.TrimSpace(provider)
	sessionID = strings.TrimSpace(sessionID)
	if provider == "" || sessionID == "" {
		return false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.HarnessProvider == provider && p.HarnessSessionID == sessionID {
		return false
	}
	p.HarnessProvider = provider
	p.HarnessSessionID = sessionID
	return true
}

// readHarnessSessionRecord reads the durable sidecar a managed client wrote for
// paneID. A record whose provider does not match the directory it was found in
// is ignored rather than trusted. Split from discoverHarnessSession so the
// boot-time resume path (which holds a PersistedPane, not a live *Pane) can
// adopt the same identity.
func readHarnessSessionRecord(dirs []harnessSessionDir, paneID string) (provider, sessionID string, ok bool) {
	name := harnessSessionRecordName(paneID)
	if name == "" {
		return "", "", false
	}
	for _, hd := range dirs {
		raw, err := os.ReadFile(filepath.Join(hd.dir, name+".json"))
		if err != nil {
			continue
		}
		var rec struct {
			Provider  string `json:"provider"`
			SessionID string `json:"sessionId"`
		}
		if json.Unmarshal(raw, &rec) != nil {
			continue
		}
		if strings.TrimSpace(rec.Provider) != hd.provider {
			continue
		}
		if strings.TrimSpace(rec.SessionID) == "" {
			continue
		}
		return hd.provider, strings.TrimSpace(rec.SessionID), true
	}
	return "", "", false
}

// discoverHarnessSession adopts this pane's recorded harness session, if a
// managed client has written one. It runs from the process-scan poll (see
// snapshotPaneProcessesFromTree), the same place Muse/Codex identity is
// discovered, so a freshly launched harness becomes resumable within one poll.
func (p *Pane) discoverHarnessSession() bool {
	if p == nil || len(p.harnessSessionDirs) == 0 {
		return false
	}
	provider, sessionID, ok := readHarnessSessionRecord(p.harnessSessionDirs, p.ID)
	if !ok {
		return false
	}
	return p.SetHarnessSession(provider, sessionID)
}

// refreshHarnessSession adopts a sidecar written since the last process poll
// and persists the pane when it changed. Call it before judging eligibility or
// building a resume recipe: the process poll is driven by client DTO requests,
// so a restart that races it would otherwise still see only the pane's stale
// ClaudeSessionID and could relaunch Claude over the live DeepSeek
// conversation.
func (p *Pane) refreshHarnessSession() bool {
	if !p.discoverHarnessSession() {
		return false
	}
	if p.onPromptsCommit != nil {
		p.onPromptsCommit(p)
	}
	return true
}

// managedHarnessLaunchCmd rebuilds the managed launcher argv for a provider.
// The daemon wires it at boot (main.go) so resume paths that only hold a
// snapshot can relaunch a hand-launched harness — the user typed `deepseek` in
// a plain shell, so there is no managed argv to replay. It is nil in pure unit
// tests, which keeps resumeSpawnOpts free of filesystem access; the recorded
// shell is replayed instead (never a stale Claude id).
var managedHarnessLaunchCmd func(provider string) ([]string, bool)

// harnessProviderFromCmd returns the managed provider token from a launch.mjs
// argv (`node …/launch.mjs deepseek …`), or "" when the command is not a
// managed harness launch. It lets a resume recover the provider before the
// client has written its sidecar, so a reused shell cannot fall through to the
// stale Claude id it still carries.
func harnessProviderFromCmd(cmd []string) string {
	for i := 0; i+1 < len(cmd); i++ {
		if !strings.Contains(cmd[i], "launch.mjs") {
			continue
		}
		switch provider := strings.TrimSpace(cmd[i+1]); provider {
		case "deepseek", "pi", "pi-glm":
			return provider
		}
	}
	return ""
}

// cmdLaunchesManagedHarness reports whether re-running a pane's command would
// relaunch a managed harness client that understands
// SOLU_HARNESS_RESUME_SESSION. A pane that merely has a harness session
// recorded but whose command is a plain shell (the user typed `deepseek` in a
// shell) is NOT resumable unattended: re-running the shell cannot replay the
// conversation. That distinction is what keeps unattended restarts honest.
func cmdLaunchesManagedHarness(cmd []string) bool {
	joined := strings.Join(cmd, " ")
	if strings.Contains(joined, "deepseek-terminal.mjs") {
		return true
	}
	// The managed shortcut is `node <launcher>/launch.mjs <provider> …`; the
	// provider token immediately follows the launcher path. Every managed
	// provider that honours SOLU_HARNESS_RESUME_SESSION belongs here.
	return harnessProviderFromCmd(cmd) != ""
}

// managedHarnessBadgeProvider reports which managed harness a pane belongs to
// for capability-badge purposes, preferring the adopted sidecar session and
// falling back to the spawn argv. The live /proc scan stays the primary
// signal, but it misses whenever a client argv is unreadable (the Windows
// stub, a foreign uid, a non-node wrapper) or simply hasn't appeared yet —
// while the daemon already knows this pane's harness identity. Without this
// fallback the auto-approve button never appears on such panes.
func managedHarnessBadgeProvider(cmd []string, adopted string) string {
	if provider := strings.TrimSpace(adopted); provider != "" {
		return provider
	}
	if provider := harnessProviderFromCmd(cmd); provider != "" {
		return provider
	}
	joined := strings.Join(cmd, " ")
	switch {
	case strings.Contains(joined, "deepseek-terminal.mjs"):
		return "deepseek"
	case strings.Contains(joined, "pi-approval.mjs"):
		return "pi"
	default:
		return ""
	}
}

// harnessResumeEnv is the child environment that tells a managed harness client
// which durable session to resume. One variable for every harness keeps the
// daemon provider-agnostic.
func harnessResumeEnv(sessionID string) []string {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil
	}
	return []string{"SOLU_HARNESS_RESUME_SESSION=" + sessionID}
}

// persistedPaneConversationID is the boot-time resume identity of a persisted
// pane: a managed harness session when the client recorded one, otherwise the
// provider-specific agent id. Mirrors Pane.Resumable so the restart loop and
// the UI agree on what "resumable" means.
func persistedPaneConversationID(info PersistedPane) string {
	if id := strings.TrimSpace(info.HarnessSessionID); id != "" {
		return id
	}
	return conversationIDForAgent(info.Agent, info.ClaudeSessionID, info.CodexThreadID, info.MuseSessionID)
}

// paneSnapshotConversationID is the PaneSnapshot twin of
// persistedPaneConversationID (used by the in-place resume handler).
func paneSnapshotConversationID(snap PaneSnapshot) string {
	if id := strings.TrimSpace(snap.HarnessSessionID); id != "" {
		return id
	}
	return conversationIDForAgent(snap.Agent, snap.ClaudeSessionID, snap.CodexThreadID, snap.MuseSessionID)
}
