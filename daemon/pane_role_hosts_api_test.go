package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOrchestratorHostsEndpoint(t *testing.T) {
	api := &apiServer{orchestratorHostProbe: func(agent string) orchestratorHostAvailability {
		available := orchestratorHostSupported(agent)
		result := orchestratorHostAvailability{
			Agent: agent, Label: orchestratorHostLabel(agent), Supported: available,
			Installed: available, Available: available,
		}
		if !available {
			result.Reason = orchestratorHostUnsupportedReason(agent)
		}
		return result
	}}
	rec := httptest.NewRecorder()
	api.handleOrchestratorHosts(rec, httptest.NewRequest(http.MethodGet, "/api/orchestrator-hosts", nil))
	var got orchestratorHostsDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.PolicyVersion != orchestratorPolicyVersion || len(got.Hosts) != 3 || got.Hosts[0].Agent != agentClaude || got.Hosts[1].Agent != agentCodex || got.Hosts[2].Agent != agentMuse {
		t.Fatalf("response = %+v", got)
	}
	if got.Hosts[2].Supported || !strings.Contains(got.Hosts[2].Reason, "MCP") {
		t.Fatalf("Muse availability = %+v", got.Hosts[2])
	}
}

func TestCodexHostAvailabilityRequiresMcpRegistration(t *testing.T) {
	home, bin := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", filepath.Join(home, ".codex"))
	t.Setenv("PATH", bin)
	if err := os.WriteFile(filepath.Join(bin, "codex"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	api := &apiServer{}
	missing := api.orchestratorHostAvailability(agentCodex)
	if missing.Available || !strings.Contains(missing.Reason, "codex mcp add soluterminal") {
		t.Fatalf("missing config availability = %+v", missing)
	}
	configDir := filepath.Join(home, ".codex")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := "[mcp_servers.soluterminal]\nurl = \"http://127.0.0.1:8787/mcp\"\n" + codexMcpPaneHeaderLine + "\n"
	if err := os.WriteFile(filepath.Join(configDir, "config.toml"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	available := api.orchestratorHostAvailability(agentCodex)
	if !available.Available || !available.Installed || !available.Supported {
		t.Fatalf("registered availability = %+v", available)
	}
}

func TestPaneDTOIdentityIsCoherentAfterHeal(t *testing.T) {
	const thread = "019f5697-154c-7a92-8b61-ad2f9b68476d"
	info, notes := healPersistedPane(PersistedPane{
		ID: "pane_corroborated", Agent: agentCodex, Role: paneRoleOrchestrator,
		Cmd:           []string{"/bin/bash", "-lc", "init; 'claude' '--append-system-prompt' 'old'; exec bash -i"},
		CodexThreadID: thread, ClaudeSessionID: "claude-session",
	})
	if len(notes) != 0 {
		t.Fatalf("corroborated identity produced heal notes: %v", notes)
	}
	mgr := NewManager(t.TempDir())
	p := mgr.SuspendExisting(&info)
	if p == nil {
		t.Fatal("healed resumable pane was not materialized")
	}
	dto := makePaneDTOWithProcessSnapshot(p, nil, nil, paneProcessSnapshot{})
	if dto.Agent != agentCodex || dto.CodexThreadID != thread || dto.ClaudeSessionID != "claude-session" || dto.Role != paneRoleOrchestrator {
		t.Fatalf("DTO identity = agent=%q codex=%q claude=%q role=%q", dto.Agent, dto.CodexThreadID, dto.ClaudeSessionID, dto.Role)
	}

	uncorroborated, notes := healPersistedPane(PersistedPane{
		ID: "pane_uncorroborated", Agent: agentCodex, Role: paneRoleOrchestrator,
		Cmd:             []string{"/bin/bash", "-lc", "init; 'claude' '--append-system-prompt' 'old'; exec bash -i"},
		ClaudeSessionID: "claude-session",
	})
	if len(notes) == 0 {
		t.Fatal("uncorroborated identity produced no heal notes")
	}
	p = mgr.SuspendExisting(&uncorroborated)
	if p == nil {
		t.Fatal("uncorroborated resumable pane was not materialized")
	}
	dto = makePaneDTOWithProcessSnapshot(p, nil, nil, paneProcessSnapshot{})
	if dto.Agent != "" || dto.CodexThreadID != "" || dto.ClaudeSessionID != "claude-session" || dto.Role != paneRoleOrchestrator {
		t.Fatalf("uncorroborated DTO identity = agent=%q codex=%q claude=%q role=%q", dto.Agent, dto.CodexThreadID, dto.ClaudeSessionID, dto.Role)
	}
}

func TestCodexHostRegistrationHintNamesThisDaemon(t *testing.T) {
	home, bin := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", filepath.Join(home, ".codex"))
	t.Setenv("PATH", bin)
	if err := os.WriteFile(filepath.Join(bin, "codex"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	got := (&apiServer{addr: "0.0.0.0:9911"}).orchestratorHostAvailability(agentCodex)
	if !strings.Contains(got.Reason, "restart the daemon on 127.0.0.1:9911") ||
		!strings.Contains(got.Reason, "--url http://127.0.0.1:9911/mcp") || strings.Contains(got.Reason, "8787") {
		t.Fatalf("registration hint = %q", got.Reason)
	}
}
