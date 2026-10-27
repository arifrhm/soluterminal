package main

import (
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInstallIDStableAcrossReads(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	first, err := loadOrCreateInstallID(dir)
	if err != nil {
		t.Fatal(err)
	}
	second, err := loadOrCreateInstallID(dir)
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("install id not stable: first=%q second=%q", first, second)
	}

	path := filepath.Join(dir, "install-id")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat install-id: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("install-id perms = %o, want 0600", perm)
	}
}

// Short or empty files were never a usable identity for either the updater
// cohort or browser ownership, so they are repaired (as the pre-ownership
// reader did) instead of disabling ownership and re-rolling the cohort forever.
func TestInstallIDRegeneratesIfShort(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "install-id")
	if err := os.WriteFile(path, []byte("abc123"), 0o600); err != nil {
		t.Fatalf("seed short file: %v", err)
	}

	id, err := loadOrCreateInstallID(dir)
	if err != nil || validateInstallID(id) != nil {
		t.Fatalf("short identity = %q, %v; want a regenerated canonical id", id, err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read install-id: %v", err)
	}
	if got := strings.TrimSpace(string(b)); got != id {
		t.Fatalf("file = %q, want overwritten with %q", got, id)
	}
	if cohort := installID(dir); cohort != id {
		t.Fatalf("updater cohort key = %q, want the repaired durable id %q", cohort, id)
	}
	if again, err := loadOrCreateInstallID(dir); err != nil || again != id {
		t.Fatalf("repaired identity unstable: %q, %v; want %q", again, err, id)
	}
}

func TestInstallIDRegeneratesIfEmpty(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "install-id")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatalf("seed empty file: %v", err)
	}

	id, err := loadOrCreateInstallID(dir)
	if err != nil || validateInstallID(id) != nil {
		t.Fatalf("empty identity = %q, %v; want a regenerated canonical id", id, err)
	}
	if cohort := installID(dir); cohort != id {
		t.Fatalf("updater cohort key = %q, want the repaired durable id %q", cohort, id)
	}
}

func TestInstallIDPersistsFormat(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	id, err := loadOrCreateInstallID(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(id) != 32 {
		t.Fatalf("id len = %d, want 32 (id=%q)", len(id), id)
	}
	if id != strings.ToLower(id) {
		t.Fatalf("id %q is not lowercase", id)
	}
	if _, err := hex.DecodeString(id); err != nil {
		t.Fatalf("id %q is not valid hex: %v", id, err)
	}
}

func TestInstallIDConcurrentCallersConverge(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	const callers = 16
	ids := make(chan string, callers)
	errs := make(chan error, callers)
	for i := 0; i < callers; i++ {
		go func() {
			id, err := loadOrCreateInstallID(dir)
			ids <- id
			errs <- err
		}()
	}
	var first string
	for i := 0; i < callers; i++ {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
		id := <-ids
		if first == "" {
			first = id
		}
		if id != first {
			t.Fatalf("concurrent identities diverged: %q != %q", id, first)
		}
	}
}

func TestInstallIDPersistenceFailureIsReported(t *testing.T) {
	t.Parallel()
	parent := t.TempDir()
	notDir := filepath.Join(parent, "not-a-directory")
	if err := os.WriteFile(notDir, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if id, err := loadOrCreateInstallID(filepath.Join(notDir, "child")); err == nil || id != "" {
		t.Fatalf("persistence failure = %q, %v; want error and no ephemeral identity", id, err)
	}
}

// Files written before durable browser ownership were accepted when they held
// at least 16 characters. Upgrading must neither disable native acceptance on
// those installs nor move them to another updater cohort.
func TestInstallIDLegacyDurableValueIsGrandfathered(t *testing.T) {
	t.Parallel()
	for name, legacy := range map[string]string{
		"twenty characters": "abcdef0123456789abcd",
		"uppercase hex":     "0123456789ABCDEF0123456789ABCDEF",
		"byte order mark":   "\ufeff0123456789abcdef0123456789abcdef",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			path := filepath.Join(dir, "install-id")
			original := []byte(legacy + "\n")
			if err := os.WriteFile(path, original, 0o600); err != nil {
				t.Fatal(err)
			}
			first, err := loadOrCreateInstallID(dir)
			if err != nil {
				t.Fatalf("legacy identity rejected: %v", err)
			}
			second, err := loadOrCreateInstallID(dir)
			if err != nil || second != first {
				t.Fatalf("legacy ownership identity unstable: %q then %q (%v)", first, second, err)
			}
			if err := validateInstallID(first); err != nil {
				t.Fatalf("legacy ownership identity %q is not canonical: %v", first, err)
			}
			if got := installID(dir); got != legacy {
				t.Fatalf("updater cohort key = %q, want pre-upgrade value %q", got, legacy)
			}
			after, err := os.ReadFile(path)
			if err != nil || string(after) != string(original) {
				t.Fatalf("legacy install-id was rewritten: %q (%v)", after, err)
			}
		})
	}
}

// Ownership fails closed on unusable identities, but the updater cohort key
// must never collapse to "" (one shared rollout bucket) because of it.
func TestInstallIDCohortKeyNeverEmptyWhenOwnershipFailsClosed(t *testing.T) {
	t.Parallel()
	// An install-id path that exists but cannot be read as a file.
	unreadable := t.TempDir()
	if err := os.Mkdir(filepath.Join(unreadable, "install-id"), 0o700); err != nil {
		t.Fatal(err)
	}
	parent := t.TempDir()
	notDir := filepath.Join(parent, "not-a-directory")
	if err := os.WriteFile(notDir, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, dir := range map[string]string{"unreadable": unreadable, "unpersistable": filepath.Join(notDir, "child")} {
		if id, err := loadOrCreateInstallID(dir); err == nil || id != "" {
			t.Fatalf("%s ownership identity = %q, %v; want fail closed", name, id, err)
		}
		cohort := installID(dir)
		if validateInstallID(cohort) != nil {
			t.Fatalf("%s updater cohort key = %q, want a usable per-run key", name, cohort)
		}
	}
	if info, err := os.Stat(filepath.Join(unreadable, "install-id")); err != nil || !info.IsDir() {
		t.Fatalf("cohort fallback replaced the unreadable identity path: %v (%v)", info, err)
	}
}
