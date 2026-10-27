package main

import (
	"testing"
)

func TestPaneCredentialPersistedSnapshotOwnsNonsecretCopy(t *testing.T) {
	p := &Pane{Credential: &paneCredentialInfo{Provider: "codex", Account: "work", Source: "launch"}}
	snap := p.Snapshot()
	persisted := persistedFromSnapshot(snap)
	persisted.Credential.Account = "changed"
	if p.Credential.Account != "work" || snap.Credential.Account != "work" {
		t.Fatal("snapshot/persistence aliased live launch metadata")
	}
}

func TestPaneCredentialSelectionWiresRestoredPanes(t *testing.T) {
	restored := &Pane{Credential: &paneCredentialInfo{Provider: "codex", Account: "before-restart", Source: "launch"}}
	m := &Manager{panes: map[string]*Pane{"restored": restored}}
	// A concurrent spawn may be constructed before the manager installs its
	// callback, but its method reference must still resolve the latest one.
	spawning := &Pane{credentialSelection: m.selectedCredentialAccount}
	m.SetCredentialSelection(func(string) string { return "current" })
	got := paneCredentialProjection(restored.Snapshot().Credential, restored.credentialSelectionCallback())
	if got.ActiveAccount != "current" || !got.RestartRequired || got.Account != "before-restart" {
		t.Fatalf("restored pane lost current selection: %+v", got)
	}
	if got := spawning.credentialSelectionCallback()("codex"); got != "current" {
		t.Fatalf("spawn retained stale callback: %q", got)
	}
	m.SetCredentialSelection(func(string) string { return "new-current" })
	if got := restored.credentialSelectionCallback()("codex"); got != "new-current" {
		t.Fatalf("restored callback not replaced: %q", got)
	}
}
