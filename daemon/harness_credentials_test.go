package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func harnessTestAccount(name, key string) harnessAccount {
	return harnessAccount{Name: name, APIKey: key, Billing: "api", Model: "deepseek-flash"}
}

func TestHarnessCredentialIsolationAndRevocation(t *testing.T) {
	s := newHarnessCredStore(t.TempDir(), "glm")
	personal := harnessTestAccount("main", "private-sentinel")
	team := harnessTestAccount("main", "team-sentinel")
	if err := s.save(personal); err != nil {
		t.Fatal(err)
	}
	if err := s.bindScope("pairing-a"); err != nil {
		t.Fatal(err)
	}
	if err := s.replaceTeam("pairing-a", "team-id", map[string]harnessAccount{"main": team}); err != nil {
		t.Fatal(err)
	}
	if err := s.selectAccount("team", "main"); err != nil {
		t.Fatal(err)
	}
	rows, err := s.list()
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(rows)
	if strings.Contains(string(b), "sentinel") || len(rows) != 2 {
		t.Fatalf("unsafe or incomplete DTO: %s", b)
	}
	if rows[0].Selected || !rows[1].Selected {
		t.Fatal("personal/team names collided")
	}
	info, err := os.Stat(s.path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("credential file must be private", err)
	}
	if err := s.replaceTeam("pairing-a", "team-id", map[string]harnessAccount{}); err != nil {
		t.Fatal(err)
	}
	rows, _ = s.list()
	if len(rows) != 1 || rows[0].Selected {
		t.Fatal("revocation fell back to personal credential")
	}
	if err := s.replaceTeam("pairing-a", "team-id", map[string]harnessAccount{"main": team}); err != nil {
		t.Fatal(err)
	}
	_ = s.selectAccount("team", "main")
	_ = s.bindScope("pairing-b")
	if err := s.replaceTeam("pairing-a", "team-id", map[string]harnessAccount{"main": team}); err == nil {
		t.Fatal("stale pairing response accepted")
	}
	rows, _ = s.list()
	if len(rows) != 1 || rows[0].Selected {
		t.Fatal("pairing retained team credential")
	}
}

func TestHarnessMalformedStoreNotOverwritten(t *testing.T) {
	s := newHarnessCredStore(t.TempDir(), "deepseek")
	_ = os.MkdirAll(filepath.Dir(s.path), 0700)
	before := []byte(`{"version":1,"personal":{"main":{"apiKey":"secret"}}}`)
	_ = os.WriteFile(s.path, before, 0600)
	if err := s.save(harnessTestAccount("new", "key")); err == nil {
		t.Fatal("malformed store overwritten")
	}
	after, _ := os.ReadFile(s.path)
	if string(before) != string(after) {
		t.Fatal("store changed")
	}
	acc := harnessTestAccount("main", "key")
	acc.Billing = "coding-plan"
	if validateHarnessAccount("deepseek", acc) == nil {
		t.Fatal("DeepSeek accepted a GLM billing profile")
	}
}

// TestHarnessStaleTempSecretsAreSwept covers the crash path: writeFileAtomicSynced
// leaves a ".<name>.tmp-*" scratch file if the process dies before rename, and
// that file holds the same API key as the store. Opening a store must clear the
// stale ones without touching a fresh in-flight write or any unrelated file.
func TestHarnessStaleTempSecretsAreSwept(t *testing.T) {
	dataDir := t.TempDir()
	dir := filepath.Join(dataDir, "harness-accounts")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	stale := filepath.Join(dir, ".deepseek.json.tmp-111")
	fresh := filepath.Join(dir, ".deepseek.json.tmp-222")
	unrelated := filepath.Join(dir, "notes.txt")
	for _, file := range []string{stale, fresh, unrelated} {
		if err := os.WriteFile(file, []byte(`{"secret":"KEY-SENTINEL"}`), 0600); err != nil {
			t.Fatal(err)
		}
	}
	old := time.Now().Add(-2 * harnessTempFileGrace)
	if err := os.Chtimes(stale, old, old); err != nil {
		t.Fatal(err)
	}
	// Opening the store triggers the sweep.
	s := newHarnessCredStore(dataDir, "deepseek")
	if s == nil {
		t.Fatal("no store")
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatalf("stale secret scratch file survived: %v", err)
	}
	for _, file := range []string{fresh, unrelated} {
		if _, err := os.Stat(file); err != nil {
			t.Fatalf("sweep removed a file it must keep (%s): %v", filepath.Base(file), err)
		}
	}
}

func TestHarnessZcodeLiveKeyCleanup(t *testing.T) {
	file := filepath.Join(t.TempDir(), "config.json")
	key := "managed-live-sentinel"
	hash := sha256.Sum256([]byte(key))
	makeConfig := func(current string) {
		config := map[string]any{"provider": map[string]any{"zai": map[string]any{"options": map[string]any{"apiKey": current, "baseURL": "https://api.z.ai/api/paas/v4"}}, "other": map[string]string{"note": "preserved"}}, "ui": map[string]string{"locale": "en-US"}, "soluterminalManagedGLM": map[string]string{"storePath": "/managed/glm.json", "source": "team", "name": "shared", "billing": "api", "model": "deepseek-flash", "scope": "pair-a", "keyHash": hex.EncodeToString(hash[:])}}
		b, _ := json.Marshal(config)
		if err := os.WriteFile(file, b, 0600); err != nil {
			t.Fatal(err)
		}
	}
	makeConfig(key)
	st := harnessAccountState{Selected: "team/shared", Scope: "pair-a", Team: map[string]harnessAccount{"shared": harnessTestAccount("shared", key)}}
	if err := reconcileZcodeConfig(file, "/managed/glm.json", st); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(file)
	if !strings.Contains(string(b), key) {
		t.Fatal("current key was removed")
	}
	st.Selected = ""
	st.Team = nil
	if err := reconcileZcodeConfig(file, "/managed/glm.json", st); err != nil {
		t.Fatal(err)
	}
	b, _ = os.ReadFile(file)
	if strings.Contains(string(b), key) || strings.Contains(string(b), "soluterminalManagedGLM") || !strings.Contains(string(b), "preserved") {
		t.Fatal("revocation did not clean only managed key")
	}
	makeConfig("user-replaced-key")
	if err := reconcileZcodeConfig(file, "/managed/glm.json", st); err != nil {
		t.Fatal(err)
	}
	b, _ = os.ReadFile(file)
	if !strings.Contains(string(b), "user-replaced-key") {
		t.Fatal("user-managed key was deleted")
	}
	makeConfig(key)
	if err := reconcileZcodeConfig(file, "/another/store", st); err != nil {
		t.Fatal(err)
	}
	b, _ = os.ReadFile(file)
	if !strings.Contains(string(b), key) {
		t.Fatal("another daemon store cleared the live key")
	}
}

func TestValidateHarnessAccountUsesProviderFacts(t *testing.T) {
	acc := harnessTestAccount("main", "sk-key")
	acc.Model = "deepseek-flash"
	if err := validateHarnessAccount("deepseek", acc); err != nil {
		t.Fatalf("deepseek api rejected: %v", err)
	}
	acc.Billing = "coding-plan"
	if err := validateHarnessAccount("deepseek", acc); err == nil {
		t.Fatal("deepseek coding-plan accepted")
	}
	acc.Model = "glm-5.3"
	acc.Billing = "coding-plan"
	if err := validateHarnessAccount("glm", acc); err != nil {
		t.Fatalf("glm coding-plan rejected: %v", err)
	}
	acc.Billing = "api"
	if err := validateHarnessAccount("glm", acc); err != nil {
		t.Fatalf("glm api rejected: %v", err)
	}
	acc.Billing = "other"
	if err := validateHarnessAccount("glm", acc); err == nil {
		t.Fatal("glm unknown billing accepted")
	}
	acc.Billing = "api"
	if err := validateHarnessAccount("claude", acc); err == nil {
		t.Fatal("non-harness provider accepted")
	}
}

func TestIsHarnessProviderMatchesFacts(t *testing.T) {
	for _, slug := range []string{"deepseek", "glm", "pi", "pi-glm"} {
		if !isHarnessProvider(slug) {
			t.Fatalf("%s not a harness provider", slug)
		}
	}
	for _, slug := range []string{"claude", "codex", "muse", ""} {
		if isHarnessProvider(slug) {
			t.Fatalf("%s incorrectly a harness provider", slug)
		}
	}
}

func TestNormalizeHarnessModelFoldsRetiredPro(t *testing.T) {
	for _, provider := range []string{"deepseek", "pi"} {
		for _, raw := range []string{"", "deepseek-v4-pro", " DEEPSEEK-V4-PRO ", "vendor/deepseek-v4-pro", "deepseek-v4-flash", "deepseek-chat", "deepseek-reasoner"} {
			if got, err := normalizeStoredHarnessModel(provider, raw); err != nil || got != "deepseek-flash" {
				t.Fatalf("%s model %q = %q, %v; want deepseek-flash", provider, raw, got, err)
			}
		}
		if got, err := normalizeStoredHarnessModel(provider, "deepseek-v4-flash-vision-exp"); err != nil || got != "deepseek-v4-flash-vision-exp" {
			t.Fatalf("%s vision = %q, %v", provider, got, err)
		}
		if _, err := normalizeStoredHarnessModel(provider, "deepseek-v4-pro-preview"); err == nil {
			t.Fatalf("%s accepted unsafe model", provider)
		}
	}
	// Non-DeepSeek providers are untouched.
	if got, err := normalizeStoredHarnessModel("glm", "glm-5.3"); err != nil || got != "glm-5.3" {
		t.Fatalf("glm model = %q", got)
	}
}

func TestHarnessTeamReplacementMigratesBlankDeepseekModel(t *testing.T) {
	s := newHarnessCredStore(t.TempDir(), "deepseek")
	if err := s.bindScope("pairing-a"); err != nil {
		t.Fatal(err)
	}
	account := harnessTestAccount("shared", "team-key")
	account.Model = ""
	if err := s.replaceTeam("pairing-a", "team-id", map[string]harnessAccount{"shared": account}); err != nil {
		t.Fatal(err)
	}
	state, err := s.readLocked()
	if err != nil {
		t.Fatal(err)
	}
	if got := state.Team["shared"].Model; got != "deepseek-flash" {
		t.Fatalf("blank team model=%q, want deepseek-flash", got)
	}
}

func TestHarnessDeepseekWritesAreStrictAndStoredAliasesMigrate(t *testing.T) {
	dataDir := t.TempDir()
	s := newHarnessCredStore(dataDir, "deepseek")
	for _, raw := range []string{"", "deepseek-v4-pro", " DEEPSEEK-V4-PRO ", "vendor/deepseek-v4-pro", "deepseek-v4-flash", "deepseek-chat", "deepseek-reasoner"} {
		acc := harnessTestAccount("new", "key")
		acc.Model = raw
		if err := s.save(acc); err == nil {
			t.Fatalf("new write accepted %q", raw)
		}
	}

	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		t.Fatal(err)
	}
	legacy := harnessAccountState{
		Version: 1, Selected: "personal/main",
		Personal: map[string]harnessAccount{"main": {Name: "main", APIKey: "legacy-key", Billing: "api", Model: " DeepSeek/deepseek-v4-pro "}},
		Team:     map[string]harnessAccount{},
	}
	raw, _ := json.Marshal(legacy)
	if err := os.WriteFile(s.path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	usagePath := filepath.Join(filepath.Dir(s.path), "deepseek-usage.csv")
	const usage = "historical,deepseek-v4-pro,unchanged\n"
	if err := os.WriteFile(usagePath, []byte(usage), 0o600); err != nil {
		t.Fatal(err)
	}
	rows, err := s.list()
	if err != nil || len(rows) != 1 || rows[0].Model != "deepseek-flash" {
		t.Fatalf("rows=%+v err=%v", rows, err)
	}
	persisted, err := os.ReadFile(s.path)
	if err != nil || strings.Contains(strings.ToLower(string(persisted)), "deepseek-v4-pro") || !strings.Contains(string(persisted), "deepseek-flash") {
		t.Fatalf("stored migration=%s err=%v", persisted, err)
	}
	if got, err := os.ReadFile(usagePath); err != nil || string(got) != usage {
		t.Fatalf("usage history changed: %q err=%v", got, err)
	}
}
