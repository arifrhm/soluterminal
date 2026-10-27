package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestContextPackCopySitesStayPairedWithRole(t *testing.T) {
	for _, tc := range []struct {
		file     string
		typeName string
	}{
		{"pane_resume.go", "SpawnOpts"},
		{"pane_refresh.go", "SpawnOpts"},
		{"pane_effort.go", "SpawnOpts"},
		{"pane_model.go", "SpawnOpts"},
		{"native_switch.go", "SpawnOpts"},
		{"api_migrate.go", "SpawnOpts"},
		{"api_migrate.go", "PaneDef"},
		{"mcp.go", "PaneDef"},
	} {
		t.Run(tc.file+"/"+tc.typeName, func(t *testing.T) {
			parsed, err := parser.ParseFile(token.NewFileSet(), tc.file, nil, 0)
			if err != nil {
				t.Fatal(err)
			}
			paired := 0
			ast.Inspect(parsed, func(node ast.Node) bool {
				lit, ok := node.(*ast.CompositeLit)
				if !ok {
					return true
				}
				ident, ok := lit.Type.(*ast.Ident)
				if !ok || ident.Name != tc.typeName {
					return true
				}
				fields := map[string]bool{}
				for _, elt := range lit.Elts {
					if kv, ok := elt.(*ast.KeyValueExpr); ok {
						if key, ok := kv.Key.(*ast.Ident); ok {
							fields[key.Name] = true
						}
					}
				}
				if !fields["Role"] {
					return true
				}
				paired++
				if !fields["ContextPack"] {
					t.Errorf("%s literal carrying Role does not carry ContextPack", tc.typeName)
				}
				return true
			})
			if paired == 0 {
				t.Fatalf("found no %s literal carrying Role", tc.typeName)
			}
		})
	}
}

func TestContextPackPreservedAcrossRespawnPaths(t *testing.T) {
	const pack = "/packs/strategy"

	t.Run("pure resume and switch builders", func(t *testing.T) {
		snap := PaneSnapshot{
			ID:              "pane-builders",
			Cwd:             "/tmp",
			Kind:            "native",
			ClaudeSessionID: "session-builders",
			ContextPack:     pack,
		}
		if got := resumeSpawnOpts(snap, 0, 0).ContextPack; got != pack {
			t.Fatalf("resumeSpawnOpts ContextPack = %q, want %q", got, pack)
		}
		for _, target := range []string{"", "native"} {
			if got := switchSpawnOpts(snap, target, 0, 0).ContextPack; got != pack {
				t.Fatalf("switchSpawnOpts(%q) ContextPack = %q, want %q", target, got, pack)
			}
		}
	})

	t.Run("refresh", func(t *testing.T) {
		p := switchTestPane("pane-refresh-pack", "native", nil, "session-refresh-pack")
		p.ClaudeTranscriptPath = writeTempTranscript(t)
		p.ContextPack = pack
		api, spawned, _ := resumeTestServer(t, p)
		rec := postRefresh(api, p.ID)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d; body=%q", rec.Code, rec.Body.String())
		}
		if len(*spawned) != 1 || (*spawned)[0].ContextPack != pack {
			t.Fatalf("refresh spawn opts = %#v, want ContextPack %q", *spawned, pack)
		}
	})

	for _, tc := range []struct {
		name string
		body string
		call func(*apiServer, http.ResponseWriter, *http.Request)
	}{
		{"effort", `{"effort":"high"}`, (*apiServer).handleSetEffort},
		{"model", `{"model":"claude-fable-5"}`, (*apiServer).handleSetModel},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := switchTestPane("pane-"+tc.name+"-pack", "native", nil, "session-"+tc.name+"-pack")
			p.ClaudeTranscriptPath = writeTempTranscript(t)
			p.ContextPack = pack
			api, spawned, _ := resumeTestServer(t, p)
			req := httptest.NewRequest(http.MethodPost, "/api/panes/"+p.ID+"/"+tc.name, strings.NewReader(tc.body))
			req.SetPathValue("id", p.ID)
			rec := httptest.NewRecorder()
			tc.call(api, rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d; body=%q", rec.Code, rec.Body.String())
			}
			if len(*spawned) != 1 || (*spawned)[0].ContextPack != pack {
				t.Fatalf("%s spawn opts = %#v, want ContextPack %q", tc.name, *spawned, pack)
			}
		})
	}

	t.Run("native switch rollback", func(t *testing.T) {
		var got SpawnOpts
		api := &apiServer{spawnPane: func(opts SpawnOpts) (*Pane, error) {
			got = opts
			return transactionTestPane("pane-rollback-pack"), nil
		}}
		snap := PaneSnapshot{ID: "pane-rollback-pack", Cwd: "/tmp", Cmd: []string{"claude"}, ContextPack: pack}
		if p := api.rollbackSwitchMode(snap); p == nil {
			t.Fatal("rollback returned nil pane")
		}
		if got.ContextPack != pack {
			t.Fatalf("rollback ContextPack = %q, want %q", got.ContextPack, pack)
		}
	})

	t.Run("clean migration", func(t *testing.T) {
		var got SpawnOpts
		api := &apiServer{spawnPane: func(opts SpawnOpts) (*Pane, error) {
			got = opts
			return transactionTestPane("pane-migrated-pack"), nil
		}}
		p := transactionTestPane("pane-migrate-pack")
		p.Cmd = []string{"claude"}
		p.ContextPack = pack
		if _, err := api.doMigrateSibling(p); err != nil {
			t.Fatal(err)
		}
		if got.ContextPack != pack {
			t.Fatalf("migration ContextPack = %q, want %q", got.ContextPack, pack)
		}
	})
}

func TestCreatePaneSpecialWorkspacePackInheritanceIsExplicitlyAbsent(t *testing.T) {
	pack := makeContextPack(t, "AGENTS.md")
	store, err := NewWorkspaceStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"strategy", "_current"} {
		if err := store.Put(&Workspace{Name: name, PaneIds: []string{}}); err != nil {
			t.Fatal(err)
		}
		if _, err := store.SetContextPack(name, pack); err != nil {
			t.Fatal(err)
		}
	}

	for _, tc := range []struct {
		name string
		body string
	}{
		{"omitted workspace", `{"cmd":["claude"]}`},
		{"autosave scratchpad", `{"cmd":["claude"],"workspace":"_current"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var got SpawnOpts
			api := &apiServer{ws: store, spawnPane: func(opts SpawnOpts) (*Pane, error) {
				got = opts
				return transactionTestPane("pane-special-workspace"), nil
			}}
			rec := httptest.NewRecorder()
			api.createPane(rec, httptest.NewRequest(http.MethodPost, "/api/panes", strings.NewReader(tc.body)))
			if rec.Code != http.StatusCreated {
				t.Fatalf("status = %d; body=%q", rec.Code, rec.Body.String())
			}
			if got.ContextPack != "" {
				t.Fatalf("ContextPack = %q, want empty without a named workspace target", got.ContextPack)
			}
		})
	}
}
