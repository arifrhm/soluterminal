package main

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func readCoordinationEvents(t *testing.T, dataDir string) []coordinationEvent {
	t.Helper()
	f, err := os.Open(filepath.Join(dataDir, "coordination", coordinationLogFile))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var out []coordinationEvent
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var ev coordinationEvent
		if err := json.Unmarshal(sc.Bytes(), &ev); err != nil {
			t.Fatalf("bad line %q: %v", sc.Text(), err)
		}
		out = append(out, ev)
	}
	return out
}

func withCoordinationLog(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	prev := coordLog
	coordLog = &coordinationLog{now: func() time.Time { return time.UnixMilli(1_000) }}
	coordLog.setDir(dir)
	t.Cleanup(func() { coordLog = prev })
	return dir
}

func TestCoordinationLogRecordsSpawnsAndCloses(t *testing.T) {
	dir := withCoordinationLog(t)
	worker := &Pane{ID: "pane-w", Agent: agentCodex, buffer: NewRingBuffer(64)}
	origin := &PaneOrigin{Source: paneOriginAgent, ByPaneID: "pane-lead", LeadPaneID: "pane-lead", At: time.Now().Add(-90 * time.Second).UnixMilli()}
	(&apiServer{}).announcePaneSpawn(worker, origin, "Docs sweep", "Personal", 3)
	recordPaneClose("pane-w", origin, "pane-lead", true)
	recordPaneClose("pane-old", nil, "app", false)
	evs := readCoordinationEvents(t, dir)
	if len(evs) != 3 {
		t.Fatalf("events = %+v", evs)
	}
	if evs[0].Kind != "spawn" || evs[0].By != "pane-lead" || evs[0].Agent != agentCodex || evs[0].Started != paneOriginAgent {
		t.Fatalf("spawn = %+v", evs[0])
	}
	if evs[1].Kind != "close" || !evs[1].Own || evs[1].Started != paneOriginAgent || evs[1].AgeSec < 89 {
		t.Fatalf("own close = %+v", evs[1])
	}
	if evs[2].Started != "" || evs[2].By != "app" {
		t.Fatalf("legacy close = %+v", evs[2])
	}
}

func TestCoordinationLogIsOffWithoutADataDirAndRotates(t *testing.T) {
	(&coordinationLog{}).record(coordinationEvent{Kind: "spawn"}) // no dir: no-op, no panic
	dir := withCoordinationLog(t)
	path := filepath.Join(dir, "coordination", coordinationLogFile)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strings.Repeat("x", coordinationLogMaxBytes+1)), 0o600); err != nil {
		t.Fatal(err)
	}
	coordLog.record(coordinationEvent{Kind: "deliver", Pane: "p", Outcome: "submitted"})
	if _, err := os.Stat(path + ".1"); err != nil {
		t.Fatalf("an oversized log must rotate: %v", err)
	}
	if evs := readCoordinationEvents(t, dir); len(evs) != 1 || evs[0].Outcome != "submitted" {
		t.Fatalf("fresh log = %+v", evs)
	}
}
