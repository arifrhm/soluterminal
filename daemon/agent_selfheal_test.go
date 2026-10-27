package main

import (
	"net"
	"os"
	"path/filepath"
	"testing"
)

// TestRebindSurvivesOldListenerClose pins the exact bug that shipped in the
// first socket self-heal attempt: a *net.UnixListener unlinks its socket path
// on Close by DEFAULT. The watchdog re-binds by creating a NEW listener at the
// same path, then closes the OLD one to unblock the accept loop — but a plain
// old.Close() then unlinks the path, deleting the file the new listener just
// created, so the agent is unreachable again. The fix is SetUnlinkOnClose(false)
// on the old listener before closing it. Without that call, the os.Stat below
// fails — which is exactly the regression this test guards against.
func TestRebindSurvivesOldListenerClose(t *testing.T) {
	// Short dir: an AF_UNIX path must fit in 108 bytes, so avoid t.TempDir()'s
	// long /tmp/TestName.../NNN form.
	dir, err := os.MkdirTemp("/tmp", "wd")
	if err != nil {
		t.Fatalf("mkdtemp: %v", err)
	}
	defer os.RemoveAll(dir)
	p := filepath.Join(dir, "s.sock")

	ln1, err := net.Listen("unix", p) // the agent's original listener
	if err != nil {
		t.Fatalf("listen 1: %v", err)
	}
	defer ln1.Close()

	os.Remove(p) // simulate the daemon path unlinking the socket out from under us

	ln2, err := net.Listen("unix", p) // the watchdog's re-bind
	if err != nil {
		t.Fatalf("re-listen: %v", err)
	}
	defer ln2.Close()

	// THE FIX: the old listener must not unlink the path that ln2 now owns.
	if ul, ok := ln1.(*net.UnixListener); ok {
		ul.SetUnlinkOnClose(false)
	}
	if err := ln1.Close(); err != nil {
		t.Fatalf("close old: %v", err)
	}

	// The socket file must still be on disk — that's the whole point: a
	// reconnecting daemon dials it by path.
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("socket file gone after closing old listener (the self-heal bug): %v", err)
	}

	// And it must still be connectable on ln2.
	c, err := net.Dial("unix", p)
	if err != nil {
		t.Fatalf("dial re-bound socket: %v", err)
	}
	c.Close()
}
