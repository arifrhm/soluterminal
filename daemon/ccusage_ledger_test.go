package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestInAppLedgerRecordLookup(t *testing.T) {
	l := newInAppLedger(t.TempDir())
	if err := l.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}

	if _, known := l.Lookup("nope"); known {
		t.Fatalf("unknown session should report known=false")
	}

	l.Record("s1", true)
	if inApp, known := l.Lookup("s1"); !known || !inApp {
		t.Fatalf("Lookup s1 = (%v,%v), want (true,true)", inApp, known)
	}

	l.Record("s2", false)
	if inApp, known := l.Lookup("s2"); !known || inApp {
		t.Fatalf("Lookup s2 = (%v,%v), want (false,true)", inApp, known)
	}
}

func TestInAppLedgerUpgradeOnly(t *testing.T) {
	l := newInAppLedger(t.TempDir())
	_ = l.Load()

	// out → in upgrades.
	l.Record("s", false)
	l.Record("s", true)
	if inApp, _ := l.Lookup("s"); !inApp {
		t.Fatalf("out→in upgrade failed: want in_app=true")
	}

	// in stays in even if a later false arrives.
	l.Record("s", false)
	if inApp, _ := l.Lookup("s"); !inApp {
		t.Fatalf("in→out must NOT downgrade: want in_app=true")
	}
}

func TestInAppLedgerFirstSeenWins(t *testing.T) {
	l := newInAppLedger(t.TempDir())
	_ = l.Load()

	l.Record("s", false)
	first := l.m["s"].FirstSeenUnix
	// A later record (even an upgrade) must not move FirstSeenUnix.
	time.Sleep(2 * time.Millisecond)
	l.Record("s", true)
	if l.m["s"].FirstSeenUnix != first {
		t.Fatalf("FirstSeenUnix moved on second Record: was %d now %d", first, l.m["s"].FirstSeenUnix)
	}
}

func TestInAppLedgerFlushOnlyWhenDirty(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cc-inapp-sessions.json")
	l := newInAppLedger(dir)
	_ = l.Load()

	// Nothing recorded → Flush is a no-op, file should not appear.
	if err := l.Flush(); err != nil {
		t.Fatalf("Flush (clean): %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("clean Flush should not write file; stat err=%v", err)
	}

	l.Record("s1", true)
	if err := l.Flush(); err != nil {
		t.Fatalf("Flush (dirty): %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("dirty Flush should write file: %v", err)
	}

	// After flush, dirty cleared. Recording the SAME (no change) keeps it
	// clean; capture mtime and confirm a no-op Flush doesn't rewrite.
	info1, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	l.Record("s1", true) // no change (already in_app)
	if l.dirty {
		t.Fatalf("no-op Record should not mark dirty")
	}
	time.Sleep(5 * time.Millisecond)
	if err := l.Flush(); err != nil {
		t.Fatalf("Flush (clean again): %v", err)
	}
	info2, _ := os.Stat(path)
	if !info1.ModTime().Equal(info2.ModTime()) {
		t.Fatalf("clean Flush rewrote the file")
	}
}

func TestInAppLedgerReloadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	l := newInAppLedger(dir)
	_ = l.Load()
	l.Record("in", true)
	l.Record("out", false)
	if err := l.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	l2 := newInAppLedger(dir)
	if err := l2.Load(); err != nil {
		t.Fatalf("reload Load: %v", err)
	}
	if inApp, known := l2.Lookup("in"); !known || !inApp {
		t.Fatalf("reloaded 'in' = (%v,%v), want (true,true)", inApp, known)
	}
	if inApp, known := l2.Lookup("out"); !known || inApp {
		t.Fatalf("reloaded 'out' = (%v,%v), want (false,true)", inApp, known)
	}
}

func TestInAppLedgerCorruptRecovery(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cc-inapp-sessions.json")
	if err := os.WriteFile(path, []byte("garbage{"), 0o600); err != nil {
		t.Fatalf("write corrupt: %v", err)
	}
	l := newInAppLedger(dir)
	if err := l.Load(); err != nil {
		t.Fatalf("Load on corrupt should not error: %v", err)
	}
	if _, known := l.Lookup("x"); known {
		t.Fatalf("expected empty ledger after corrupt recovery")
	}
	if _, err := os.Stat(path + ".corrupt"); err != nil {
		t.Fatalf("expected quarantined .corrupt: %v", err)
	}
}

func TestInAppLedgerPrune(t *testing.T) {
	l := newInAppLedger(t.TempDir())
	_ = l.Load()
	now := time.Now()
	// Inject an old and a fresh entry directly.
	l.m["old"] = inAppEntry{InApp: false, FirstSeenUnix: now.Add(-48 * time.Hour).Unix(), WorkflowInvocationID: "wfi_old"}
	l.m["new"] = inAppEntry{InApp: true, FirstSeenUnix: now.Unix(), WorkflowInvocationID: "wfi_new", WorkflowRole: "reviewer", CacheTTLClass: "5m", CacheTTLControl: "applied"}

	l.Prune(now.Add(-24 * time.Hour))
	if _, ok := l.m["old"]; ok {
		t.Fatalf("Prune should have removed 'old'")
	}
	if _, ok := l.m["new"]; !ok {
		t.Fatalf("Prune should have kept 'new'")
	}
	if inv, role, class, control := l.LookupWorkflow("new"); inv != "wfi_new" || role != "reviewer" || class != "5m" || control != "applied" {
		t.Fatalf("Prune changed fresh workflow metadata: %q %q %q %q", inv, role, class, control)
	}
	if !l.dirty {
		t.Fatalf("Prune that removed an entry should mark dirty")
	}
}

func TestInAppLedgerRecordEmptyIgnored(t *testing.T) {
	l := newInAppLedger(t.TempDir())
	_ = l.Load()
	l.Record("", true)
	if l.dirty {
		t.Fatalf("recording empty session id should not mark dirty")
	}
	if _, known := l.Lookup(""); known {
		t.Fatalf("empty session id should never be known")
	}
}

func TestInAppLedgerRecordAccountFirstWins(t *testing.T) {
	l := newInAppLedger(t.TempDir())
	_ = l.Load()

	// Setting an account records it.
	l.RecordAccount("s", "alice@team")
	if got := l.LookupAccount("s"); got != "alice@team" {
		t.Fatalf("LookupAccount = %q, want alice@team", got)
	}

	// A later different name must NOT clobber the start-of-session account.
	l.RecordAccount("s", "bob@personal")
	if got := l.LookupAccount("s"); got != "alice@team" {
		t.Fatalf("account was clobbered: got %q, want alice@team (first wins)", got)
	}

	// An empty account never overwrites a recorded one.
	l.RecordAccount("s", "")
	if got := l.LookupAccount("s"); got != "alice@team" {
		t.Fatalf("empty account overwrote: got %q, want alice@team", got)
	}
}

func TestInAppLedgerRecordAccountEmptyIsNoOp(t *testing.T) {
	l := newInAppLedger(t.TempDir())
	_ = l.Load()

	// An empty account must not create a phantom entry (would make Lookup
	// report the session known/out-app and skew the in_app semantics).
	l.RecordAccount("never", "")
	if l.dirty {
		t.Fatalf("RecordAccount with empty account should not mark dirty")
	}
	if _, known := l.Lookup("never"); known {
		t.Fatalf("empty-account RecordAccount created a phantom entry")
	}
	if got := l.LookupAccount("never"); got != "" {
		t.Fatalf("unknown session account = %q, want empty", got)
	}
}

func TestInAppLedgerAccountRoundTrip(t *testing.T) {
	dir := t.TempDir()
	l := newInAppLedger(dir)
	_ = l.Load()
	l.Record("in", true)
	l.RecordAccount("in", "alice@team")
	if err := l.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	l2 := newInAppLedger(dir)
	if err := l2.Load(); err != nil {
		t.Fatalf("reload Load: %v", err)
	}
	if inApp, known := l2.Lookup("in"); !known || !inApp {
		t.Fatalf("reloaded in_app = (%v,%v), want (true,true)", inApp, known)
	}
	if got := l2.LookupAccount("in"); got != "alice@team" {
		t.Fatalf("reloaded account = %q, want alice@team", got)
	}
}

func TestInAppLedgerBackwardCompatNoAccount(t *testing.T) {
	// A ledger file written by an OLDER daemon (before the account field) must
	// load with Account="" for every entry — unknown/unattributed, so the
	// scanner falls back to the machine owner exactly as before.
	dir := t.TempDir()
	path := filepath.Join(dir, "cc-inapp-sessions.json")
	old := []byte(`{"s":{"inApp":true,"firstSeenUnix":1700000000}}`)
	if err := os.WriteFile(path, old, 0o600); err != nil {
		t.Fatalf("write old-format ledger: %v", err)
	}
	l := newInAppLedger(dir)
	if err := l.Load(); err != nil {
		t.Fatalf("Load old-format: %v", err)
	}
	if inApp, known := l.Lookup("s"); !known || !inApp {
		t.Fatalf("old-format in_app = (%v,%v), want (true,true)", inApp, known)
	}
	if got := l.LookupAccount("s"); got != "" {
		t.Fatalf("old-format account = %q, want empty (unattributed)", got)
	}
	if inv, role, class, control := l.LookupWorkflow("s"); inv != "" || role != "" || class != "" || control != "" {
		t.Fatalf("old-format workflow metadata = %q %q %q %q, want empty", inv, role, class, control)
	}
	// After an account is recorded, the new field round-trips on the next flush.
	l.RecordAccount("s", "alice@team")
	_ = l.Flush()
	l2 := newInAppLedger(dir)
	_ = l2.Load()
	if got := l2.LookupAccount("s"); got != "alice@team" {
		t.Fatalf("upgraded account = %q, want alice@team", got)
	}
}

func TestInAppLedgerWorkflowMetadataFirstNonemptyWins(t *testing.T) {
	dir := t.TempDir()
	l := newInAppLedger(dir)
	l.Record("s", true) // hook-before-observer
	l.RecordWorkflow("s", "wfi_1", "reviewer", "5m", "applied")
	l.RecordWorkflow("s", "wfi_2", "refuter", "1h", "unsupported")
	inv, role, class, control := l.LookupWorkflow("s")
	if inv != "wfi_1" || role != "reviewer" || class != "5m" || control != "applied" {
		t.Fatalf("workflow metadata = %q %q %q %q", inv, role, class, control)
	}
	l.RecordWorkflow("observer-first", "wfi_3", "triage", "5m", "applied")
	l.Record("observer-first", true)
	if inApp, known := l.Lookup("observer-first"); !known || !inApp {
		t.Fatalf("observer-before-hook lost in-app upgrade")
	}
	if err := l.Flush(); err != nil {
		t.Fatal(err)
	}
	restarted := newInAppLedger(dir)
	if err := restarted.Load(); err != nil {
		t.Fatal(err)
	}
	inv, role, class, control = restarted.LookupWorkflow("s")
	if inv != "wfi_1" || role != "reviewer" || class != "5m" || control != "applied" {
		t.Fatalf("restarted metadata = %q %q %q %q", inv, role, class, control)
	}
}
