package main

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// shortSocketPath keeps the path under the unix socket length limit even when
// TMPDIR is a long sandbox path.
func shortSocketPath(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "sd")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return filepath.Join(dir, "a.sock")
}

// A live agent that binds later than the old flat 3s budget must still be
// reached: on a loaded WSL host healthy agents took 5s+ and were killed.
func TestDialStartingAgentWaitsForSlowLiveAgent(t *testing.T) {
	path := shortSocketPath(t)
	listening := make(chan net.Listener, 1)
	time.AfterFunc(300*time.Millisecond, func() {
		ln, err := net.Listen("unix", path)
		if err != nil {
			close(listening)
			return
		}
		listening <- ln
	})
	conn, err := dialStartingAgent(path, make(chan struct{}), 5*time.Second)
	if ln, ok := <-listening; ok {
		_ = ln.Close()
	}
	if err != nil {
		t.Fatalf("dial slow live agent: %v", err)
	}
	_ = conn.Close()
}

// An agent that dies before listening fails the spawn at once rather than
// holding the request for the whole startup budget.
func TestDialStartingAgentFailsFastWhenAgentExits(t *testing.T) {
	exited := make(chan struct{})
	close(exited)
	start := time.Now()
	_, err := dialStartingAgent(shortSocketPath(t), exited, 10*time.Second)
	if err == nil || !strings.Contains(err.Error(), "agent exited before listening") {
		t.Fatalf("err = %v, want agent exited before listening", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("exited agent took %s to fail", elapsed)
	}
}

func TestDialStartingAgentTimesOutOnSilentLiveAgent(t *testing.T) {
	_, err := dialStartingAgent(shortSocketPath(t), make(chan struct{}), 150*time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "did not listen within") {
		t.Fatalf("err = %v, want did not listen within", err)
	}
}
