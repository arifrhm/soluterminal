package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestMCPSpawnPaneRoleValidationAndPassThrough(t *testing.T) {
	t.Run("schema exposes role", func(t *testing.T) {
		for _, tool := range mcpTools() {
			if tool.Name != "spawn_pane" {
				continue
			}
			var schema struct {
				Properties map[string]json.RawMessage `json:"properties"`
			}
			if err := json.Unmarshal(tool.InputSchema, &schema); err != nil {
				t.Fatalf("decode schema: %v", err)
			}
			if _, ok := schema.Properties["role"]; !ok {
				t.Fatal("spawn_pane schema is missing role")
			}
			return
		}
		t.Fatal("spawn_pane tool missing")
	})

	newAPI := func(t *testing.T) (*apiServer, *[]SpawnOpts) {
		t.Helper()
		captured := &[]SpawnOpts{}
		mgr := NewManager(t.TempDir())
		ws, err := NewWorkspaceStore(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		if err := ws.Put(&Workspace{Name: "Default"}); err != nil {
			t.Fatal(err)
		}
		api := &apiServer{
			mgr:   mgr,
			ws:    ws,
			panes: &failingPaneStore{},
			orchestratorHostProbe: func(agent string) orchestratorHostAvailability {
				return orchestratorHostAvailability{Agent: agent, Label: orchestratorHostLabel(agent), Supported: orchestratorHostSupported(agent), Installed: true, Available: true}
			},
			spawnPane: func(o SpawnOpts) (*Pane, error) {
				*captured = append(*captured, o)
				p := transactionTestPane("pane_mcp_role")
				p.Cmd = append([]string(nil), o.Cmd...)
				p.Role = o.Role
				mgr.panes[p.ID] = p
				return p, nil
			},
		}
		return api, captured
	}

	t.Run("rejects role on non-claude command", func(t *testing.T) {
		api, captured := newAPI(t)
		got := api.mcpToolSpawnPane(map[string]any{"cmd": "bash", "role": "orchestrator"}, "")
		if !strings.Contains(fmt.Sprint(got), "requires cmd") {
			t.Fatalf("result = %#v, want requires-cmd error", got)
		}
		if len(*captured) != 0 {
			t.Fatal("invalid role/command combination spawned a pane")
		}
	})

	t.Run("passes Codex orchestrator role and identity to spawn", func(t *testing.T) {
		api, captured := newAPI(t)
		got := api.mcpToolSpawnPane(map[string]any{"cmd": "codex", "role": "orchestrator"}, "")
		if result, ok := got.(map[string]any); !ok || result["isError"] == true {
			t.Fatalf("result = %#v, want success", got)
		}
		if len(*captured) != 1 || (*captured)[0].Role != paneRoleOrchestrator || (*captured)[0].Agent != agentCodex {
			t.Fatalf("captured = %#v, want Codex orchestrator", *captured)
		}
	})

	t.Run("reports unavailable Codex remediation", func(t *testing.T) {
		api, captured := newAPI(t)
		api.orchestratorHostProbe = func(agent string) orchestratorHostAvailability {
			return orchestratorHostAvailability{
				Agent: agent, Label: "Codex", Supported: true, Installed: true,
				Available: false, Reason: "not registered — run codex mcp add soluterminal",
			}
		}
		got := api.mcpToolSpawnPane(map[string]any{"cmd": "codex", "role": "orchestrator"}, "")
		if !strings.Contains(fmt.Sprint(got), "codex mcp add soluterminal") {
			t.Fatalf("result = %#v", got)
		}
		if len(*captured) != 0 {
			t.Fatal("unavailable Codex host spawned")
		}
	})

	t.Run("rejects Muse with MCP reason", func(t *testing.T) {
		api, captured := newAPI(t)
		got := api.mcpToolSpawnPane(map[string]any{"cmd": "muse", "role": "orchestrator"}, "")
		if body := fmt.Sprint(got); !strings.Contains(body, "Muse") || !strings.Contains(body, "MCP") {
			t.Fatalf("result = %#v", got)
		}
		if len(*captured) != 0 {
			t.Fatal("Muse role spawned")
		}
	})

	t.Run("rejects unknown role", func(t *testing.T) {
		api, captured := newAPI(t)
		got := api.mcpToolSpawnPane(map[string]any{"cmd": "claude", "role": "bogus"}, "")
		if !strings.Contains(fmt.Sprint(got), "unknown role") {
			t.Fatalf("result = %#v, want unknown-role error", got)
		}
		if len(*captured) != 0 {
			t.Fatal("unknown role spawned a pane")
		}
	})

	t.Run("passes orchestrator role to spawn", func(t *testing.T) {
		api, captured := newAPI(t)
		got := api.mcpToolSpawnPane(map[string]any{"cmd": "claude", "role": "orchestrator"}, "")
		if result, ok := got.(map[string]any); !ok || result["isError"] == true {
			t.Fatalf("result = %#v, want success", got)
		}
		if len(*captured) != 1 || (*captured)[0].Role != paneRoleOrchestrator {
			t.Fatalf("captured = %#v, want one orchestrator SpawnOpts", *captured)
		}
	})

	t.Run("omitted role preserves plain spawn", func(t *testing.T) {
		api, captured := newAPI(t)
		got := api.mcpToolSpawnPane(map[string]any{"cmd": "claude"}, "")
		if result, ok := got.(map[string]any); !ok || result["isError"] == true {
			t.Fatalf("result = %#v, want success", got)
		}
		if len(*captured) != 1 || (*captured)[0].Role != "" {
			t.Fatalf("captured = %#v, want empty role", *captured)
		}
	})
}

func TestMCPSpawnPaneDoesNotInheritWorkspaceContextPack(t *testing.T) {
	pack := makeContextPack(t, "AGENTS.md")
	store, err := NewWorkspaceStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Put(&Workspace{Name: "strategy", PaneIds: []string{}}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SetContextPack("strategy", pack); err != nil {
		t.Fatal(err)
	}

	var got SpawnOpts
	mgr := NewManager(t.TempDir())
	api := &apiServer{
		mgr: mgr,
		ws:  store,
		spawnPane: func(opts SpawnOpts) (*Pane, error) {
			got = opts
			p := transactionTestPane("pane-mcp-no-pack")
			p.Cmd = []string{"claude"}
			mgr.panes[p.ID] = p
			return p, nil
		},
	}
	result := api.mcpToolSpawnPane(map[string]any{"cmd": "claude", "workspace": "strategy"}, "")
	if value, ok := result.(map[string]any); !ok || value["isError"] == true {
		t.Fatalf("result = %#v, want success", result)
	}
	if got.ContextPack != "" {
		t.Fatalf("MCP SpawnOpts.ContextPack = %q, want empty because MCP inheritance is deferred", got.ContextPack)
	}
	ws, ok := store.Get("strategy")
	if !ok || len(ws.Panes) != 1 || ws.Panes[0].ContextPack != "" {
		t.Fatalf("workspace panes = %#v, want one unbriefed MCP pane", ws)
	}
}
