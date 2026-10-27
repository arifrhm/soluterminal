package main

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

// The setting is tri-state on the wire and on disk. These tests pin the two
// halves that a bool would have silently collapsed: "never chose" is not
// "chose false", and an explicit choice survives every respawn path.

func TestAnnounceDelegationEffective(t *testing.T) {
	t.Parallel()
	yes, no := true, false
	tests := []struct {
		name     string
		role     string
		override *bool
		want     bool
	}{
		{"orchestrator default", paneRoleOrchestrator, nil, true},
		{"plain default", "", nil, false},
		{"plain opted in", "", &yes, true},
		{"orchestrator opted out", paneRoleOrchestrator, &no, false},
		{"plain explicitly off", "", &no, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			snap := PaneSnapshot{Role: tc.role, AnnounceDelegationEvents: tc.override}
			if got := snap.announceDelegationEffective(); got != tc.want {
				t.Fatalf("effective = %v, want %v", got, tc.want)
			}
			p := &Pane{Role: tc.role, AnnounceDelegationEvents: tc.override, subs: map[int64]*subEntry{}}
			if got := p.AnnounceDelegationEffective(); got != tc.want {
				t.Fatalf("pane effective = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestAnnounceDelegationPersistRoundTrip(t *testing.T) {
	t.Parallel()
	yes, no := true, false
	for _, override := range []*bool{nil, &yes, &no} {
		entry := persistedFromSnapshot(PaneSnapshot{ID: "pane_1", AnnounceDelegationEvents: override})
		b, err := json.Marshal(entry)
		if err != nil {
			t.Fatal(err)
		}
		if override == nil && strings.Contains(string(b), "announceDelegationEvents") {
			t.Fatalf("an unset override wrote a key, so legacy panes.json is not byte-identical: %s", b)
		}
		var back PersistedPane
		if err := json.Unmarshal(b, &back); err != nil {
			t.Fatal(err)
		}
		switch {
		case override == nil && back.AnnounceDelegationEvents != nil:
			t.Fatalf("unset override came back as %v", *back.AnnounceDelegationEvents)
		case override != nil && (back.AnnounceDelegationEvents == nil || *back.AnnounceDelegationEvents != *override):
			t.Fatalf("override %v came back as %v", *override, back.AnnounceDelegationEvents)
		}
	}

	// A panes.json written before this field existed loads as "follow the role
	// default", not as "the user turned it off".
	var legacy PersistedPane
	if err := json.Unmarshal([]byte(`{"id":"pane_old","role":"orchestrator"}`), &legacy); err != nil {
		t.Fatal(err)
	}
	if legacy.AnnounceDelegationEvents != nil {
		t.Fatalf("legacy entry decoded an override: %v", *legacy.AnnounceDelegationEvents)
	}
}

// A resume, a Terminal↔Focus switch and a suspend/attach cycle all rebuild the
// Pane struct; each must carry the override or the toggle silently resets.
func TestAnnounceDelegationSurvivesRespawnPaths(t *testing.T) {
	t.Parallel()
	no := false

	resumed := transactionTestPane("pane_resume")
	restoreResumedState(resumed, PaneSnapshot{Role: paneRoleOrchestrator, AnnounceDelegationEvents: &no})
	if resumed.AnnounceDelegationEvents == nil || *resumed.AnnounceDelegationEvents {
		t.Fatalf("restoreResumedState dropped the override: %v", resumed.AnnounceDelegationEvents)
	}

	// The persist → PersistedPane → Pane hop the attach/suspend literals make.
	entry := persistedFromSnapshot(PaneSnapshot{ID: "pane_x", Role: paneRoleOrchestrator, AnnounceDelegationEvents: &no})
	rebuilt := &Pane{ID: entry.ID, Role: entry.Role, AnnounceDelegationEvents: entry.AnnounceDelegationEvents}
	if rebuilt.AnnounceDelegationEffective() {
		t.Fatal("an orchestrator pane that opted out came back announcing")
	}
}

func TestSetPaneAnnounceDelegationEndpoint(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	store, err := NewPaneStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	mgr := NewManager(dir)
	p := transactionTestPane("pane_announce")
	mgr.panes[p.ID] = p
	a := &apiServer{mgr: mgr, panes: store}

	post := func(body string) (int, paneDTO) {
		t.Helper()
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/api/panes/"+p.ID+"/announce-delegation", strings.NewReader(body))
		req.SetPathValue("id", p.ID)
		a.setPaneAnnounceDelegation(rec, req)
		var dto paneDTO
		if rec.Code == 200 {
			if err := json.Unmarshal(rec.Body.Bytes(), &dto); err != nil {
				t.Fatalf("decode DTO: %v (%s)", err, rec.Body.String())
			}
		}
		return rec.Code, dto
	}

	code, dto := post(`{"announce":true}`)
	if code != 200 {
		t.Fatalf("enable = %d", code)
	}
	if !dto.AnnounceDelegation {
		t.Fatalf("DTO effective value = %v, want true", dto.AnnounceDelegation)
	}
	if dto.AnnounceDelegationEvents == nil || !*dto.AnnounceDelegationEvents {
		t.Fatalf("DTO override = %v, want an explicit true", dto.AnnounceDelegationEvents)
	}
	persisted := store.List()
	if len(persisted) != 1 || persisted[0].AnnounceDelegationEvents == nil || !*persisted[0].AnnounceDelegationEvents {
		t.Fatalf("the choice did not reach disk: %+v", persisted)
	}

	// null clears the override back to the role default (this pane is plain).
	code, dto = post(`{"announce":null}`)
	if code != 200 {
		t.Fatalf("clear = %d", code)
	}
	if dto.AnnounceDelegationEvents != nil {
		t.Fatalf("clearing left an override: %v", *dto.AnnounceDelegationEvents)
	}
	if dto.AnnounceDelegation {
		t.Fatal("a plain pane with no override reports announcing")
	}

	// Codex is a first-class orchestrator host, so its toggle is accepted.
	codex := transactionTestPane("pane_codex")
	codex.Agent = agentCodex
	mgr.panes[codex.ID] = codex
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/panes/"+codex.ID+"/announce-delegation", strings.NewReader(`{"announce":true}`))
	req.SetPathValue("id", codex.ID)
	a.setPaneAnnounceDelegation(rec, req)
	if rec.Code != 200 {
		t.Fatalf("codex enable = %d, want 200", rec.Code)
	}
	if got := codex.Snapshot().AnnounceDelegationEvents; got == nil || !*got {
		t.Fatalf("codex override = %v, want true", got)
	}

	// Turning it OFF on a supported pane stays legal — a user clearing a
	// setting must never be blocked by the pane's agent.
	rec = httptest.NewRecorder()
	req = httptest.NewRequest("POST", "/api/panes/"+codex.ID+"/announce-delegation", strings.NewReader(`{"announce":false}`))
	req.SetPathValue("id", codex.ID)
	a.setPaneAnnounceDelegation(rec, req)
	if rec.Code != 200 {
		t.Fatalf("codex disable = %d, want 200", rec.Code)
	}

	// Muse has no MCP client and cannot act on the nudge vocabulary.
	muse := transactionTestPane("pane_muse")
	muse.Agent = agentMuse
	mgr.panes[muse.ID] = muse
	rec = httptest.NewRecorder()
	req = httptest.NewRequest("POST", "/api/panes/"+muse.ID+"/announce-delegation", strings.NewReader(`{"announce":true}`))
	req.SetPathValue("id", muse.ID)
	a.setPaneAnnounceDelegation(rec, req)
	if rec.Code != 400 {
		t.Fatalf("muse enable = %d, want 400", rec.Code)
	}

	// A missing pane is a 404, not a panic.
	rec = httptest.NewRecorder()
	req = httptest.NewRequest("POST", "/api/panes/nope/announce-delegation", strings.NewReader(`{"announce":true}`))
	req.SetPathValue("id", "nope")
	a.setPaneAnnounceDelegation(rec, req)
	if rec.Code != 404 {
		t.Fatalf("missing pane = %d, want 404", rec.Code)
	}
}
