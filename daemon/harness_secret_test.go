package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// resetHarnessSecret installs a backend and clears the process key cache so a
// test starts from a known state. It restores both on cleanup.
func resetHarnessSecret(t *testing.T, backend harnessSecretBackend) {
	t.Helper()
	oldBackend := harnessSecretBackendImpl
	harnessSecretBackendImpl = backend
	harnessSecretKeyCache = sync.Map{}
	t.Cleanup(func() {
		harnessSecretBackendImpl = oldBackend
		harnessSecretKeyCache = sync.Map{}
	})
}

type fakeSecretBackend struct{ keys map[string][]byte }

func (f *fakeSecretBackend) Get(account string) ([]byte, bool) {
	key, ok := f.keys[account]
	return key, ok
}

func (f *fakeSecretBackend) Set(account string, key []byte) error {
	if f.keys == nil {
		f.keys = map[string][]byte{}
	}
	f.keys[account] = append([]byte(nil), key...)
	return nil
}

func TestHarnessSecretSealOpen(t *testing.T) {
	key := make([]byte, harnessSecretKeyBytes)
	for i := range key {
		key[i] = byte(i)
	}
	sealed, err := sealHarnessSecret(key, "sk-secret-sentinel")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(sealed, harnessSecretPrefix) {
		t.Fatalf("sealed value has no prefix: %s", sealed)
	}
	if strings.Contains(sealed, "sk-secret-sentinel") {
		t.Fatalf("plaintext leaked into sealed value: %s", sealed)
	}
	if plain, err := openHarnessSecret(key, sealed); err != nil || plain != "sk-secret-sentinel" {
		t.Fatalf("open = %q, %v", plain, err)
	}
	// A legacy value without the prefix is plaintext and passes through.
	if plain, err := openHarnessSecret(key, "sk-legacy"); err != nil || plain != "sk-legacy" {
		t.Fatalf("legacy open = %q, %v", plain, err)
	}
	// A different key, and a flipped byte, both fail closed.
	other := make([]byte, harnessSecretKeyBytes)
	for i := range other {
		other[i] = 9
	}
	if _, err := openHarnessSecret(other, sealed); err == nil {
		t.Fatal("a different key decrypted the value")
	}
	tampered := sealed[:len(sealed)-2] + "AA"
	if _, err := openHarnessSecret(key, tampered); err == nil {
		t.Fatal("a tampered ciphertext decrypted")
	}
}

func TestHarnessSecretKeyFileProvisioned0600(t *testing.T) {
	resetHarnessSecret(t, noSecretBackend{})
	dir := t.TempDir()
	key, err := harnessSecretKey(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(key) != harnessSecretKeyBytes {
		t.Fatalf("key length %d", len(key))
	}
	keyPath := filepath.Join(dir, "harness-accounts", ".secret-key")
	info, err := os.Stat(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("key file mode = %v, want 0600", info.Mode().Perm())
	}
	// Cached in-process, and stable across a fresh cache (read from disk).
	harnessSecretKeyCache = sync.Map{}
	again, err := harnessSecretKey(dir)
	if err != nil {
		t.Fatal(err)
	}
	if string(key) != string(again) {
		t.Fatal("key file was not reused")
	}
}

func TestHarnessSecretKeychainPreferredAndNoFile(t *testing.T) {
	backend := &fakeSecretBackend{}
	resetHarnessSecret(t, backend)
	dir := t.TempDir()
	key, err := harnessSecretKey(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := backend.Get(harnessSecretAccount(dir)); !ok {
		t.Fatal("key was not stored in the keychain")
	}
	if _, err := os.Stat(filepath.Join(dir, "harness-accounts", ".secret-key")); !os.IsNotExist(err) {
		t.Fatal("key file was written even though a keychain is available")
	}
	// A fresh cache reads it back from the keychain, not the filesystem.
	harnessSecretKeyCache = sync.Map{}
	again, err := harnessSecretKey(dir)
	if err != nil {
		t.Fatal(err)
	}
	if string(key) != string(again) {
		t.Fatal("keychain key was not reused")
	}
}

// TestHarnessCredentialStoreEncryptsAtRest is the security contract: the store
// file must never contain the raw key, but the store still returns it.
func TestHarnessCredentialStoreEncryptsAtRest(t *testing.T) {
	resetHarnessSecret(t, noSecretBackend{})
	s := newHarnessCredStore(t.TempDir(), "deepseek")
	if err := s.save(harnessTestAccount("main", "sk-plaintext-sentinel")); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(s.path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "sk-plaintext-sentinel") {
		t.Fatalf("raw key on disk: %s", raw)
	}
	if !strings.Contains(string(raw), harnessSecretPrefix) {
		t.Fatalf("no sealed value on disk: %s", raw)
	}
	source, name, acc, err := s.selected()
	if err != nil || source != "personal" || name != "main" || acc.APIKey != "sk-plaintext-sentinel" {
		t.Fatalf("selected = %s/%s key=%q err=%v", source, name, acc.APIKey, err)
	}
	if rows, err := s.list(); err != nil {
		t.Fatal(err)
	} else if encoded, _ := json.Marshal(rows); strings.Contains(string(encoded), "sentinel") {
		t.Fatalf("metadata DTO leaked the key: %s", encoded)
	}
}

// TestHarnessCredentialStoreMigratesLegacyPlaintext proves an existing store
// still opens, and the next write seals every account.
func TestHarnessCredentialStoreMigratesLegacyPlaintext(t *testing.T) {
	resetHarnessSecret(t, noSecretBackend{})
	s := newHarnessCredStore(t.TempDir(), "deepseek")
	if err := os.MkdirAll(filepath.Dir(s.path), 0700); err != nil {
		t.Fatal(err)
	}
	legacy := harnessAccountState{
		Version:  1,
		Personal: map[string]harnessAccount{"main": harnessTestAccount("main", "sk-legacy-sentinel")},
		Team:     map[string]harnessAccount{},
		Selected: "personal/main",
	}
	b, _ := json.Marshal(legacy)
	if err := os.WriteFile(s.path, b, 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, acc, err := s.selected(); err != nil || acc.APIKey != "sk-legacy-sentinel" {
		t.Fatalf("legacy read = %q, %v", acc.APIKey, err)
	}
	if err := s.save(harnessTestAccount("second", "sk-second")); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(s.path)
	if strings.Contains(string(raw), "sk-legacy-sentinel") || strings.Contains(string(raw), "sk-second") {
		t.Fatalf("plaintext survived a write: %s", raw)
	}
}

const harnessSecretInitProbeEnv = "SOLU_TEST_HARNESS_SECRET_INIT_PROBE"

// Package init runs in every solud process, including the `solud agent` each
// pane spawns. Keychain detection walks PATH, which on WSL is dozens of 9P
// /mnt/c dirs; doing it at init pushed agent startup past the spawn dial and
// failed every new pane. A fresh process must reach main with it undetected.
func TestHarnessSecretBackendNotDetectedAtInit(t *testing.T) {
	cmd := exec.Command(os.Args[0], "-test.run=^TestHarnessSecretBackendInitProbeHelper$", "-test.v")
	cmd.Env = append(os.Environ(), harnessSecretInitProbeEnv+"=1")
	out, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "backend undetected at init") {
		t.Fatalf("init probe failed: %v\n%s", err, out)
	}
}

func TestHarnessSecretBackendInitProbeHelper(t *testing.T) {
	if os.Getenv(harnessSecretInitProbeEnv) != "1" {
		return
	}
	if harnessSecretBackendImpl != nil {
		t.Fatalf("package init detected the secret backend: %T", harnessSecretBackendImpl)
	}
	t.Log("backend undetected at init")
}
