package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// Exercise TestMain in a fresh process: t.Setenv in the parent would miss the
// inherited environment that release/workflow verification actually supplies.
// The endpoint is always a synthetic server, never the user's browser.
func TestDaemonPackageBrowserIsolation(t *testing.T) {
	for _, live := range []bool{false, true} {
		name := "ordinary tests cannot discover or clean the live browser"
		if live {
			name = "explicit live test retains endpoint discovery"
		}
		t.Run(name, func(t *testing.T) {
			var probes, mutations atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var request struct {
					ID     any    `json:"id"`
					Method string `json:"method"`
					Params struct {
						Name string `json:"name"`
					} `json:"params"`
				}
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Error(err)
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				var result any = map[string]any{"protocolVersion": "2025-03-26", "serverInfo": map[string]any{"version": "test"}}
				if request.Method == "initialize" {
					probes.Add(1)
				} else {
					text := `{"ok":true}`
					if request.Params.Name == "list_modes" {
						text = `[{"name":"solu-accept-regression"}]`
					} else if request.Params.Name == "list_windows" {
						text = `{"windows":[]}`
					} else {
						mutations.Add(1)
					}
					result = map[string]any{"content": []map[string]string{{"type": "text", "text": text}}}
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": result})
			}))
			defer server.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestDaemonPackageBrowserIsolationHelper$", "-test.timeout=15s")
			for _, entry := range os.Environ() {
				key, _, _ := strings.Cut(entry, "=")
				switch key {
				case "SOLU_BROWSER_ACCEPT", "SOLU_BROWSER_ACCEPT_LIVE", "SOLU_SOLUBROWSE_MCP_URL", "SOLU_SOLUBROWSE_MCP_TOKEN", "SOLU_TEST_BROWSER_ISOLATION_HELPER":
					continue
				}
				cmd.Env = append(cmd.Env, entry)
			}
			mode := "ordinary"
			if live {
				mode = "live"
				cmd.Env = append(cmd.Env, "SOLU_BROWSER_ACCEPT_LIVE=1")
			}
			cmd.Env = append(cmd.Env, "SOLU_TEST_BROWSER_ISOLATION_HELPER="+mode,
				"SOLU_BROWSER_ACCEPT=1", "SOLU_SOLUBROWSE_MCP_URL="+server.URL,
				"SOLU_SOLUBROWSE_MCP_TOKEN=synthetic-browser-token")
			output, err := cmd.CombinedOutput()
			if err != nil {
				t.Errorf("test subprocess: %v\n%s", err, output)
			}
			if mutations.Load() != 0 {
				t.Errorf("test subprocess issued %d live-browser mutations", mutations.Load())
			}
			wantProbes := int32(0)

			if probes.Load() != wantProbes {
				t.Errorf("browser probes = %d, want %d", probes.Load(), wantProbes)
			}
		})
	}
}
