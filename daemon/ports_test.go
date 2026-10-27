package main

import "testing"

func TestPortRegistryHasRequiresPaneOwnedPort(t *testing.T) {
	t.Parallel()
	pr := &PortRegistry{
		seen: map[string]*Port{
			"tcp|127.0.0.1|3000": {
				Port: 3000,
				Addr: "127.0.0.1",
			},
			"tcp|127.0.0.1|5173": {
				Port:   5173,
				Addr:   "127.0.0.1",
				PaneID: "pane-1",
			},
		},
	}

	if pr.Has(3000) {
		t.Fatal("expected non-pane port to be rejected")
	}
	if !pr.Has(5173) {
		t.Fatal("expected pane-owned port to be allowed")
	}
	if pr.Has(8080) {
		t.Fatal("expected unknown port to be rejected")
	}
}
