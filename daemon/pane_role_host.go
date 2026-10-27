package main

import (
	"strings"
	"unicode/utf8"
)

// orchestratorHost is the provider-specific transport seam for the shared
// orchestrator role. Behavioral policy belongs in orchestrator_policy.go.
type orchestratorHost struct {
	agent                string
	label                string
	decorateTerminalArgv func(cmd []string) []string
	runsCmd              func(cmd []string) bool
	bareCmd              []string
}

var orchestratorHosts = map[string]orchestratorHost{
	agentClaude: {
		agent: agentClaude, label: "Claude", decorateTerminalArgv: withOrchestratorBrief,
		runsCmd: cmdRunsClaude, bareCmd: []string{"claude"},
	},
	agentCodex: {
		agent: agentCodex, label: "Codex", decorateTerminalArgv: withCodexOrchestratorBrief,
		runsCmd: cmdRunsCodex, bareCmd: []string{"codex"},
	},
}

func orchestratorHostFor(agent string) (orchestratorHost, bool) {
	key := strings.ToLower(strings.TrimSpace(agent))
	if key == "" {
		key = agentClaude
	}
	h, ok := orchestratorHosts[key]
	return h, ok
}

func orchestratorHostSupported(agent string) bool {
	_, ok := orchestratorHostFor(agent)
	return ok
}

func orchestratorHostUnsupportedReason(agent string) string {
	if strings.EqualFold(strings.TrimSpace(agent), agentMuse) {
		return "Muse Code has no MCP client, so it cannot reach soluterminal's delegation tools"
	}
	return "the orchestrator role needs a Claude or Codex session (a plain shell or unknown command cannot call soluterminal's tools)"
}

const codexDeveloperInstructionsKey = "developer_instructions="

// withCodexOrchestratorBrief injects the pane policy through the supported
// Codex config override. On codex-cli 0.149.1, `codex debug prompt-input -c
// developer_instructions=<text>` renders the text verbatim as the first
// developer message, and `codex resume <id> -c key=value` is accepted. This
// per-pane override replaces a user-level developer_instructions setting from
// ~/.codex/config.toml for orchestrator panes only; it never edits config.toml
// or any AGENTS.md. It survives tryLoginShellWrap because orchestratorBrief has
// no ASCII apostrophe (TestOrchestratorBriefTextConstraints). Values that fail
// TOML parsing are treated as literal strings, so codexOverrideValueIsLiteral
// and its tests pin the first-character transport constraint.
func withCodexOrchestratorBrief(cmd []string) []string {
	if !cmdStartsCodex(cmd) || (len(cmd) >= 2 && cmd[1] == "app-server") {
		return cmd
	}
	out := make([]string, 0, len(cmd)+2)
	for i := 0; i < len(cmd); i++ {
		if cmd[i] == "-c" && i+1 < len(cmd) &&
			strings.HasPrefix(cmd[i+1], codexDeveloperInstructionsKey) &&
			strings.Contains(cmd[i+1], orchestratorBriefMarker) {
			i++
			continue
		}
		out = append(out, cmd[i])
	}
	return append(out, "-c", codexDeveloperInstructionsKey+orchestratorBrief)
}

func codexOverrideValueIsLiteral(v string) bool {
	if v == "" {
		return false
	}
	r, _ := utf8.DecodeRuneInString(v)
	if strings.ContainsRune("\"'[{+-.", r) || (r >= '0' && r <= '9') {
		return false
	}
	return !strings.HasPrefix(v, "true") && !strings.HasPrefix(v, "false") &&
		!strings.HasPrefix(v, "inf") && !strings.HasPrefix(v, "nan")
}
