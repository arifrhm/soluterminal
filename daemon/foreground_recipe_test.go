package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync"
	"testing"
)

func TestForegroundClaudeHandoffDoesNotResumePriorProvider(t *testing.T) {
	const museID = "019f543c-aaaa-7701-bc5e-6a73a50a9084"
	const codexID = "019f543c-3f11-7701-bc5e-6a73a50a9083"
	for _, scenario := range []struct {
		name, priorAgent, priorCodex string
		cmd                          []string
	}{
		{"Muse with original Muse recipe", agentMuse, "", []string{"muse", "resume", museID, "--dangerously-skip-permissions"}},
		{"Muse with older Codex recipe", agentMuse, "", []string{"codex", "resume", codexID, "--dangerously-bypass-approvals-and-sandbox"}},
		{"Codex with original Codex recipe", agentCodex, codexID, []string{"codex", "resume", codexID, "--dangerously-bypass-approvals-and-sandbox"}},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			p := deferredTestPane("foreground-claude", "")
			p.Agent, p.Cmd, p.CodexThreadID = scenario.priorAgent, scenario.cmd, scenario.priorCodex
			p.MuseSessionID = museID
			p.ClaudeSessionID = "old-claude"
			p.ClaudeTranscriptPath = writeTempTranscript(t)
			adoptForegroundClaude(p)
			beforeHook := fleetRespawnEligibility(p)
			if effectiveAgentKind(beforeHook.Snapshot.Agent) != agentClaude || beforeHook.Blocker == "" {
				t.Errorf("foreground Claude before its hook selected prior conversation: agent=%q blocker=%q", beforeHook.Snapshot.Agent, beforeHook.Blocker)
			}
			if beforeHook.Snapshot.ClaudeSessionID != "" || beforeHook.Snapshot.ClaudeTranscriptPath != "" {
				t.Error("new Claude process inherited the old Claude hook identity")
			}
			if beforeHook.Snapshot.MuseSessionID != museID {
				t.Error("handoff erased historical Muse conversation")
			}
			p.SetClaudeSessionID("current-claude")
			p.SetClaudeTranscriptPath(writeTempTranscript(t))
			afterHook := fleetRespawnEligibility(p)
			if effectiveAgentKind(afterHook.Snapshot.Agent) != agentClaude || afterHook.Blocker != "" {
				t.Errorf("fresh Claude hook did not own fleet eligibility: agent=%q blocker=%q", afterHook.Snapshot.Agent, afterHook.Blocker)
			}
			assertClaude := func(snap PaneSnapshot) {
				t.Helper()
				opts := resumeSpawnOpts(snap, 80, 24)
				want := []string{"claude", "--resume", "current-claude"}
				if effectiveAgentKind(opts.Agent) != agentClaude || !reflect.DeepEqual(opts.Cmd, want) {
					t.Errorf("resume replayed obsolete provider, conversation or permission flags: agent=%q argv=%q", opts.Agent, opts.Cmd)
				}
				if opts.MuseSessionID != museID {
					t.Error("resume erased historical Muse conversation")
				}
			}
			assertClaude(p.Snapshot())
			encoded, err := json.Marshal(persistedFromSnapshot(p.Snapshot()))
			if err != nil {
				t.Fatal(err)
			}
			var retained PersistedPane
			if err := json.Unmarshal(encoded, &retained); err != nil {
				t.Fatal(err)
			}
			retained, _ = healPersistedPane(retained)
			restored := NewManager(t.TempDir()).SuspendExisting(&retained)
			if restored == nil {
				t.Fatal("durable current Claude conversation was lost on reload")
			}
			assertClaude(restored.Snapshot())
		})
	}
}

func TestForegroundCodexWithoutIdentityDoesNotResumePriorProvider(t *testing.T) {
	const museID = "019f543c-aaaa-7701-bc5e-6a73a50a9084"
	for _, priorAgent := range []string{agentMuse, agentClaude} {
		for _, fresh := range []bool{false, true} {
			t.Run(priorAgent+map[bool]string{false: "/unknown", true: "/fresh"}[fresh], func(t *testing.T) {
				p := deferredTestPane("foreground-codex", "")
				p.Agent = priorAgent
				p.Cmd = []string{priorAgent, "--dangerously-skip-permissions"}
				p.MuseSessionID = museID
				p.ClaudeSessionID = "prior-claude"
				p.ClaudeTranscriptPath = writeTempTranscript(t)
				if fresh {
					adoptFreshCodexIdentity(p)
				} else {
					adoptDiscoveredCodexThread(p, "")
				}
				eligible := fleetRespawnEligibility(p)
				if effectiveAgentKind(eligible.Snapshot.Agent) != agentCodex || eligible.Blocker == "" {
					t.Errorf("unbound foreground Codex offered an old conversation: agent=%q blocker=%q", eligible.Snapshot.Agent, eligible.Blocker)
				}
				opts := resumeSpawnOpts(p.Snapshot(), 80, 24)
				if effectiveAgentKind(opts.Agent) != agentCodex || !reflect.DeepEqual(opts.Cmd, []string{"codex", "resume", ""}) {
					t.Errorf("unbound Codex recipe selected old provider or permission flags: agent=%q argv=%q", opts.Agent, opts.Cmd)
				}
				if opts.MuseSessionID != museID || opts.ClaudeSessionID != "prior-claude" {
					t.Error("unbound handoff erased other providers' historical identities")
				}
			})
		}
	}
}

func TestForegroundProviderKeepsMatchingRecipe(t *testing.T) {
	const id = "019f543c-3f11-7701-bc5e-6a73a50a9083"
	for _, scenario := range []struct {
		name, agent string
		adopt       func(*Pane)
	}{
		{"known Codex", agentCodex, func(p *Pane) { adoptDiscoveredCodexThread(p, id) }},
		{"unknown Codex", agentCodex, func(p *Pane) { adoptDiscoveredCodexThread(p, "") }},
		{"fresh Codex", agentCodex, func(p *Pane) { adoptFreshCodexIdentity(p) }},
		{"resuming Codex", agentCodex, func(p *Pane) { adoptResumingCodexIdentity(p, id) }},
		{"Muse", agentMuse, func(p *Pane) { adoptForegroundMuse(p, id) }},
		{"Claude", agentClaude, func(p *Pane) { adoptForegroundClaude(p) }},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			p := deferredTestPane("same-provider", "")
			p.Agent = scenario.agent
			p.Cmd = []string{scenario.agent, "--model", "selected-model"}
			before := append([]string(nil), p.Cmd...)
			scenario.adopt(p)
			if got := p.Snapshot().Cmd; !reflect.DeepEqual(got, before) {
				t.Fatalf("same-provider command changed: got %q want %q", got, before)
			}
		})
	}
}

func TestForegroundRecipeUpdatesWhilePaneMetadataIsRead(t *testing.T) {
	const museID = "019f543c-aaaa-7701-bc5e-6a73a50a9084"
	const codexID = "019f543c-3f11-7701-bc5e-6a73a50a9083"
	p := deferredTestPane("foreground-readers", "")
	p.Agent = agentMuse
	p.MuseSessionID = museID
	p.Cmd = []string{"muse", "resume", museID}
	p.buffer.Write([]byte("durable matching transcript\n"))
	before := p.Snapshot()
	mgr := NewManager(t.TempDir())
	mgr.panes[p.ID] = p
	api := &apiServer{mgr: mgr}
	var work sync.WaitGroup
	work.Add(4)
	go func() {
		defer work.Done()
		for i := 0; i < 200; i++ {
			adoptForegroundClaude(p)
			adoptDiscoveredCodexThread(p, codexID)
			adoptForegroundMuse(p, museID)
		}
	}()
	go func() {
		defer work.Done()
		for i := 0; i < 200; i++ {
			p.Rename("")
		}
	}()
	go func() {
		defer work.Done()
		for i := 0; i < 100; i++ {
			req := httptest.NewRequest(http.MethodGet, "/api/panes/"+p.ID+"/export", nil)
			req.SetPathValue("id", p.ID)
			rec := httptest.NewRecorder()
			api.exportPane(rec, req)
			if rec.Code != http.StatusOK {
				t.Errorf("export status=%d", rec.Code)
				return
			}
		}
	}()
	go func() {
		defer work.Done()
		for i := 0; i < 100; i++ {
			rec := httptest.NewRecorder()
			api.searchPanes(rec, httptest.NewRequest(http.MethodGet, "/api/panes/search?q=durable", nil))
			var result paneSearchResponse
			if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &result) != nil || result.TotalMatches != 1 {
				t.Errorf("search lost the stable transcript: status=%d body=%s", rec.Code, rec.Body.String())
				return
			}
		}
	}()
	work.Wait()
	if !reflect.DeepEqual(before.Cmd, []string{"muse", "resume", museID}) {
		t.Fatal("foreground update mutated an already-captured command slice")
	}
}
