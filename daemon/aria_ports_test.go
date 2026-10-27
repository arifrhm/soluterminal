package main

import (
	"encoding/json"
	"testing"
	"time"
)

func TestAriaListPortsIncludesPaneWorkspaceAndURL(t *testing.T) {
	t.Parallel()
	p := testPaneWithTail(t, "pane_1", "web", "ready")
	ws, err := NewWorkspaceStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewWorkspaceStore: %v", err)
	}
	if err := ws.Put(&Workspace{Name: "frontend", PaneIds: []string{p.ID}}); err != nil {
		t.Fatalf("WorkspaceStore.Put: %v", err)
	}
	pr := &PortRegistry{seen: map[string]*Port{
		"tcp|127.0.0.1|5173": {
			Port:    5173,
			Addr:    "127.0.0.1",
			Proto:   "tcp",
			Process: "node",
			Cmdline: "vite --host 127.0.0.1",
			Cwd:     p.Cwd,
			PaneID:  p.ID,
			FirstAt: time.Unix(10, 0),
		},
	}}
	a := &Aria{mgr: testManagerWithPane(p), ws: ws, ports: pr}

	got := a.toolListPorts("", false)
	var out struct {
		Ports []ariaPortSummary `json:"ports"`
	}
	if err := json.Unmarshal([]byte(got), &out); err != nil {
		t.Fatalf("toolListPorts returned invalid json %q: %v", got, err)
	}
	if len(out.Ports) != 1 {
		t.Fatalf("ports len = %d, want 1 (%s)", len(out.Ports), got)
	}
	port := out.Ports[0]
	if port.URL != "http://localhost:5173" {
		t.Fatalf("URL = %q, want localhost URL", port.URL)
	}
	if port.PaneTitle != "web" || port.Workspace != "frontend" {
		t.Fatalf("pane metadata = title %q workspace %q", port.PaneTitle, port.Workspace)
	}
}

func TestAriaListPortsFiltersPaneOwnedPorts(t *testing.T) {
	t.Parallel()
	pr := &PortRegistry{seen: map[string]*Port{
		"tcp|127.0.0.1|3000": {Port: 3000, Addr: "127.0.0.1", Proto: "tcp", Process: "node", FirstAt: time.Unix(10, 0), PaneID: "pane_1"},
		"tcp|127.0.0.1|5432": {Port: 5432, Addr: "127.0.0.1", Proto: "tcp", Process: "postgres", FirstAt: time.Unix(10, 0)},
	}}
	a := &Aria{mgr: NewManager(""), ports: pr}

	got := a.toolListPorts("", true)
	var out struct {
		Ports []ariaPortSummary `json:"ports"`
	}
	if err := json.Unmarshal([]byte(got), &out); err != nil {
		t.Fatalf("toolListPorts returned invalid json %q: %v", got, err)
	}
	if len(out.Ports) != 1 || out.Ports[0].Port != 3000 {
		t.Fatalf("ports = %#v, want only pane-owned port 3000", out.Ports)
	}
}
