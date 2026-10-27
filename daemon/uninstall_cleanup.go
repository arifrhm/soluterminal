package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Text-level removers for the Codex files. `solud uninstall-cleanup` and
// turning the agent integration off use them through
// agent_integration_remove.go. Everything outside our entries is left
// byte-for-byte alone.

// codexAgentsRemove drops our marker-delimited block (and the blank line we
// put before it). An ambiguous marker shape is left untouched.
func codexAgentsRemove(existing string) (updated string, changed bool, ok bool) {
	beginCount := strings.Count(existing, codexAgentsBeginMarker)
	endCount := strings.Count(existing, codexAgentsEndMarker)
	if beginCount == 0 && endCount == 0 {
		return existing, false, true
	}
	if beginCount != 1 || endCount != 1 {
		return existing, false, false
	}
	begin := strings.Index(existing, codexAgentsBeginMarker)
	end := strings.Index(existing, codexAgentsEndMarker)
	if begin > end {
		return existing, false, false
	}
	end += len(codexAgentsEndMarker)
	if end < len(existing) && existing[end] == '\n' {
		end++
	}
	head := existing[:begin]
	if strings.HasSuffix(head, "\n\n") {
		head = head[:len(head)-1]
	}
	return head + existing[end:], true, true
}

// codexConfigRemoveServer drops the [mcp_servers.<name>] table and its
// sub-tables ([mcp_servers.<name>.*]) from a config.toml body.
func codexConfigRemoveServer(body, name string) (string, bool) {
	lines := strings.Split(body, "\n")
	out := make([]string, 0, len(lines))
	skipping, changed := false, false
	for _, line := range lines {
		if m := tomlTableRe.FindStringSubmatch(line); m != nil {
			h := strings.TrimSpace(m[1])
			ours := tomlTableIs(h, name) ||
				strings.HasPrefix(h, "mcp_servers."+name+".") ||
				strings.HasPrefix(h, `mcp_servers."`+name+`".`)
			skipping = ours
			if ours {
				changed = true
				continue
			}
		}
		if skipping {
			continue
		}
		out = append(out, line)
	}
	if !changed {
		return body, false
	}
	return strings.Join(out, "\n"), true
}

// runUninstallCleanupCommand removes all four global entries (see
// agent_integration_remove.go) and records the uninstall in the data dir, so
// a daemon still running never re-adds them and a later reinstall asks again.
// The Windows uninstaller runs it for the Windows-native and the WSL side.
func runUninstallCleanupCommand(args []string) int {
	dataDir := defaultDataDir()
	asJSON := false
	for i := 0; i < len(args); i++ {
		switch a := args[i]; {
		case a == "-h" || a == "--help":
			fmt.Println("usage: solud uninstall-cleanup [--data-dir DIR] [--json]\n\n" +
				"Removes SoluTerminal's entries from Claude Code (the MCP server in ~/.claude.json and the hooks in\n" +
				"~/.claude/settings.json) and Codex (~/.codex/config.toml and the marked block in ~/.codex/AGENTS.md).\n" +
				"Backs each changed file up into the data dir first. Deletes no SoluTerminal data.")
			return 0
		case a == "--json":
			asJSON = true
		case a == "--data-dir" && i+1 < len(args):
			i++
			dataDir = args[i]
		case strings.HasPrefix(a, "--data-dir="):
			dataDir = strings.TrimPrefix(a, "--data-dir=")
		}
	}
	backupDir := agentIntegrationBackupDir(dataDir)
	results := removeAgentIntegration(backupDir)
	if dataDir != "" {
		store := newAgentIntegrationStore(dataDir)
		if _, err := store.update(markAgentIntegrationUninstalled); err != nil {
			results = append(results, agentCleanupResult{What: "SoluTerminal choice", Error: err.Error()})
		}
	}
	failed := false
	for _, r := range results {
		if r.Error != "" {
			failed = true
		}
	}
	if asJSON {
		b, _ := json.Marshal(map[string]any{"results": results, "backupDir": backupDir})
		fmt.Println(string(b))
	} else {
		for _, r := range results {
			if r.Error != "" {
				fmt.Printf("%-26s error: %s\n", r.What, r.Error)
				continue
			}
			fmt.Printf("%-26s %s\n", r.What, r.Result)
		}
	}
	if failed {
		return 1
	}
	return 0
}

// markAgentIntegrationUninstalled turns the integration off with source
// "uninstall". A user's own "off" is kept as theirs.
func markAgentIntegrationUninstalled(r *agentIntegrationRecord) {
	if r.State == agentIntegrationOff && r.Source == agentIntegrationSourceUser {
		return
	}
	r.State = agentIntegrationOff
	r.Source = agentIntegrationSourceUninstall
	r.DecidedAtMs = time.Now().UnixMilli()
	r.NoticePending = false
}
