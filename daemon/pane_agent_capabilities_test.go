package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCodexFocusRejectsClaudeOnlyConfigurationControls(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		body string
		call func(*apiServer, http.ResponseWriter, *http.Request)
	}{
		{"effort", `{"effort":"high"}`, (*apiServer).handleSetEffort},
		{"model", `{"model":"gpt-5.4"}`, (*apiServer).handleSetModel},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := switchTestPane("codex_controls", "native", nil, "")
			p.Agent = agentCodex
			p.CodexThreadID = "thread-1"
			api, spawnOpts, killed := resumeTestServer(t, p)
			req := httptest.NewRequest(http.MethodPost, "/api/panes/codex_controls/"+tt.name, strings.NewReader(tt.body))
			req.SetPathValue("id", "codex_controls")
			rec := httptest.NewRecorder()
			tt.call(api, rec, req)
			if rec.Code != http.StatusConflict {
				t.Fatalf("status = %d, want 409; body=%q", rec.Code, rec.Body.String())
			}
			if len(*spawnOpts) != 0 || len(*killed) != 0 {
				t.Fatalf("Claude-only control changed Codex pane: spawns=%d kills=%d", len(*spawnOpts), len(*killed))
			}
		})
	}
}

func TestMuseFocusRejectsOrchestratorRole(t *testing.T) {
	p := switchTestPane("muse_role", "native", nil, "")
	p.Agent = agentMuse
	api, spawnOpts, killed := resumeTestServer(t, p)
	req := httptest.NewRequest(http.MethodPost, "/api/panes/muse_role/role", strings.NewReader(`{"role":"orchestrator"}`))
	req.SetPathValue("id", p.ID)
	rec := httptest.NewRecorder()
	api.handleSetRole(rec, req)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "Muse") {
		t.Fatalf("status=%d body=%q", rec.Code, rec.Body.String())
	}
	if len(*spawnOpts) != 0 || len(*killed) != 0 {
		t.Fatal("Muse role request mutated pane")
	}
}
