package main

import (
	"strings"
	"testing"
)

// A second SoluTerminal on another port (a side test daemon, another install
// sharing the home) turning its switch off must leave the entries that point
// at the main install alone; the main install removes its own.
func TestTurningOffRemovesOnlyThisInstallsEntries(t *testing.T) {
	home, dataDir, _ := isolateAgentHome(t)
	writeAgentFixtures(t, home)
	other := agentIntegrationOwner{
		MCPURL:  "http://127.0.0.1:47613/mcp",
		HookURL: "http://127.0.0.1:47613/api/hooks/tool-call",
		GOOS:    "linux",
	}
	for _, r := range removeAgentIntegrationFor(agentIntegrationBackupDir(dataDir), other) {
		if r.Error != "" || r.Result == "removed" {
			t.Fatalf("side daemon touched %s: %+v", r.What, r)
		}
	}
	if !strings.Contains(readTestFile(t, home+"/.claude.json"), `"soluterminal"`) ||
		!strings.Contains(readTestFile(t, home+"/.codex/config.toml"), "[mcp_servers.soluterminal]") ||
		!strings.Contains(readTestFile(t, home+"/.claude/settings.json"), "/api/hooks/tool-call") {
		t.Fatal("the main install's entries were removed by another install")
	}

	own := agentIntegrationOwner{
		MCPURL:  "http://127.0.0.1:8787/mcp",
		HookURL: "http://127.0.0.1:8787/api/hooks/tool-call",
		GOOS:    "linux",
	}
	for _, r := range removeAgentIntegrationFor(agentIntegrationBackupDir(dataDir), own) {
		if r.Error != "" || r.Result != "removed" {
			t.Fatalf("own removal %s: %+v", r.What, r)
		}
	}
	if strings.Contains(readTestFile(t, home+"/.codex/config.toml"), "soluterminal") {
		t.Fatal("own entries left behind")
	}
}

func TestOwnerOnWindowsCountsTheSiblingPort(t *testing.T) {
	o := agentIntegrationOwner{MCPURL: "http://127.0.0.1:" + dualWindowsDaemonPort + "/mcp", HookURL: "http://127.0.0.1:" + dualWindowsDaemonPort + "/api/hooks/tool-call", GOOS: "windows"}
	if !o.ownsMCPURL("http://127.0.0.1:8787/mcp") {
		t.Fatal("Windows backend moving between 8787 and its Dual port owns both")
	}
	if o.ownsMCPURL("http://127.0.0.1:47613/mcp") {
		t.Fatal("a non-canonical port belongs to someone else")
	}
	if !(agentIntegrationOwner{}).ownsMCPURL("http://127.0.0.1:1/mcp") {
		t.Fatal("uninstall (zero owner) removes everything")
	}
}
