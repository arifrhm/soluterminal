package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCursorStoreBilledTimestampRoundTripAndReset(t *testing.T) {
	dir := t.TempDir()
	s := newCursorStore(dir)
	s.SetBilledState("/turns.jsonl", 100, 100, 200, "2026-09-03T01:02:03Z", "message", "request", "invocation\x1freviewer\x1f5m\x1fapplied")
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	reloaded := newCursorStore(dir)
	if err := reloaded.Load(); err != nil {
		t.Fatal(err)
	}
	offset, ts, reset := reloaded.ReadState("/turns.jsonl", 110, 201)
	if offset != 100 || ts != "2026-09-03T01:02:03Z" || reset {
		t.Fatalf("state = %d %q %v", offset, ts, reset)
	}
	offset, ts, messageID, requestID, attribution, reset := reloaded.ReadBilledState("/turns.jsonl", 110, 201)
	if offset != 100 || ts != "2026-09-03T01:02:03Z" || messageID != "message" || requestID != "request" || attribution != "invocation\x1freviewer\x1f5m\x1fapplied" || reset {
		t.Fatalf("billed state = %d %q %q %q %q %v", offset, ts, messageID, requestID, attribution, reset)
	}
	offset, ts, reset = reloaded.ReadState("/turns.jsonl", 50, 202)
	if offset != 0 || ts != "" || !reset {
		t.Fatalf("reset state = %d %q %v", offset, ts, reset)
	}
	offset, ts, messageID, requestID, attribution, reset = reloaded.ReadBilledState("/turns.jsonl", 50, 202)
	if offset != 0 || ts != "" || messageID != "" || requestID != "" || attribution != "" || !reset {
		t.Fatalf("reset billed state = %d %q %q %q %q %v", offset, ts, messageID, requestID, attribution, reset)
	}

	legacyPath := filepath.Join(t.TempDir(), "cc-usage-cursors.json")
	if err := os.WriteFile(legacyPath, []byte(`{"/old":{"offset":1,"size":1,"modTimeUnixNano":1}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	legacy := &cursorStore{path: legacyPath, m: map[string]scanCursor{}}
	if err := legacy.Load(); err != nil {
		t.Fatal(err)
	}
	_, ts, _ = legacy.ReadState("/old", 1, 1)
	if ts != "" {
		t.Fatalf("legacy cursor timestamp = %q", ts)
	}
}

func TestCursorStoreRoundTrip(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	s := newCursorStore(dir)
	if err := s.Load(); err != nil {
		t.Fatalf("Load on empty dir: %v", err)
	}

	s.Set("/a/b.jsonl", 100, 100, 1000)
	s.Set("/c/d.jsonl", 250, 300, 2000)
	if err := s.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// Fresh store, reload from disk.
	s2 := newCursorStore(dir)
	if err := s2.Load(); err != nil {
		t.Fatalf("reload Load: %v", err)
	}
	if off, ok := s2.Get("/a/b.jsonl"); !ok || off != 100 {
		t.Fatalf("Get /a/b.jsonl = (%d,%v), want (100,true)", off, ok)
	}
	if off, ok := s2.Get("/c/d.jsonl"); !ok || off != 250 {
		t.Fatalf("Get /c/d.jsonl = (%d,%v), want (250,true)", off, ok)
	}
	if _, ok := s2.Get("/missing"); ok {
		t.Fatalf("Get /missing: expected ok=false")
	}
}

func TestCursorStoreShouldResetTo(t *testing.T) {
	t.Parallel()
	s := newCursorStore(t.TempDir())
	if err := s.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}

	// Never seen → 0.
	if got := s.ShouldResetTo("/new.jsonl", 500, 5000); got != 0 {
		t.Fatalf("new file ShouldResetTo = %d, want 0", got)
	}

	s.Set("/f.jsonl", 1000, 1000, 5000)

	// Grew (size >= offset and >= stored size, mtime forward) → resume at offset.
	if got := s.ShouldResetTo("/f.jsonl", 1500, 6000); got != 1000 {
		t.Fatalf("grown file ShouldResetTo = %d, want 1000", got)
	}

	// Same stat (no advance) → resume at offset.
	if got := s.ShouldResetTo("/f.jsonl", 1000, 5000); got != 1000 {
		t.Fatalf("unchanged file ShouldResetTo = %d, want 1000", got)
	}

	// Truncated below offset → reset 0.
	if got := s.ShouldResetTo("/f.jsonl", 500, 6000); got != 0 {
		t.Fatalf("truncated-below-offset ShouldResetTo = %d, want 0", got)
	}
}

func TestCursorStoreReplacedFileResets(t *testing.T) {
	t.Parallel()
	s := newCursorStore(t.TempDir())
	_ = s.Load()
	s.Set("/f.jsonl", 1000, 2000, 9000)

	// Replaced: mtime changed (older) and size smaller than recorded size,
	// but still >= offset. Should reset to 0.
	if got := s.ShouldResetTo("/f.jsonl", 1500, 8000); got != 0 {
		t.Fatalf("replaced (mtime back) ShouldResetTo = %d, want 0", got)
	}

	// Smaller than stored size but mtime forward → still a shrink → reset.
	if got := s.ShouldResetTo("/f.jsonl", 1500, 9500); got != 0 {
		t.Fatalf("shrunk-below-stored-size ShouldResetTo = %d, want 0", got)
	}
}

func TestCursorStoreCorruptRecovery(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "cc-usage-cursors.json")
	if err := os.WriteFile(path, []byte("{not valid json"), 0o600); err != nil {
		t.Fatalf("write corrupt: %v", err)
	}

	s := newCursorStore(dir)
	if err := s.Load(); err != nil {
		t.Fatalf("Load on corrupt should not error, got: %v", err)
	}
	// Started fresh.
	if _, ok := s.Get("/anything"); ok {
		t.Fatalf("expected empty store after corrupt recovery")
	}
	// Corrupt file quarantined.
	if _, err := os.Stat(path + ".corrupt"); err != nil {
		t.Fatalf("expected quarantined .corrupt file: %v", err)
	}
	// Store is usable: set, save, reload.
	s.Set("/x.jsonl", 42, 42, 100)
	if err := s.Save(); err != nil {
		t.Fatalf("Save after recovery: %v", err)
	}
	s2 := newCursorStore(dir)
	if err := s2.Load(); err != nil {
		t.Fatalf("reload after recovery: %v", err)
	}
	if off, ok := s2.Get("/x.jsonl"); !ok || off != 42 {
		t.Fatalf("post-recovery Get = (%d,%v), want (42,true)", off, ok)
	}
}
