package main

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// The public build reports each provider CLI's own login.
func TestCLILoginsReadTheProviderFiles(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("CODEX_HOME", "")
	t.Setenv("MUSE_AUTH_PATH", "")
	t.Setenv("XDG_CONFIG_HOME", "")
	a := &apiServer{}
	a.useCLILogins()
	if a.claudeLogin == nil || a.codexLogin == nil || a.museLogin == nil {
		t.Fatalf("logins = %v %v %v, want all three", a.claudeLogin, a.codexLogin, a.museLogin)
	}
	if a.claudeLogin.LiveCredentialPresent() || a.museLogin.LiveAuthPresent() {
		t.Fatal("reported a login with no credential files")
	}

	claudeDir := filepath.Join(home, ".claude")
	if err := os.MkdirAll(claudeDir, 0o700); err != nil {
		t.Fatal(err)
	}
	creds := `{"claudeAiOauth":{"accessToken":"a","refreshToken":"r","expiresAt":4102444800000}}`
	if err := os.WriteFile(filepath.Join(claudeDir, ".credentials.json"), []byte(creds), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".claude.json"), []byte(`{"oauthAccount":{"emailAddress":"Me@Example.com"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if !a.claudeLogin.LiveCredentialPresent() {
		t.Fatal("valid Claude credentials not detected")
	}
	if got := a.claudeLogin.UsageAccountLabel(); got != "me@example.com" {
		t.Fatalf("Claude usage label = %q, want the login email", got)
	}

	if got := a.codexLogin.livePath(); got != filepath.Join(home, ".codex", "auth.json") {
		t.Fatalf("Codex live path = %q", got)
	}
	if err := a.codexLogin.requireCodexFileStorage(); err != nil {
		t.Fatalf("no config.toml: %v", err)
	}
	claims, _ := json.Marshal(map[string]any{"email": "dev@example.com", "https://api.openai.com/auth": map[string]any{"chatgpt_account_id": "acct", "chatgpt_user_id": "user"}})
	idToken := "e30." + base64.RawURLEncoding.EncodeToString(claims) + ".sig"
	auth, _ := json.Marshal(map[string]any{"tokens": map[string]any{"id_token": idToken, "access_token": "x", "refresh_token": "y", "account_id": "acct"}})
	if err := os.MkdirAll(filepath.Join(home, ".codex"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(a.codexLogin.livePath(), auth, 0o600); err != nil {
		t.Fatal(err)
	}
	acct, user, email := codexAuthUsageIdentity(auth)
	resolve := a.codexLogin.UsageAccountResolver()
	if got := resolve(acct, user); got != email || email == "" {
		t.Fatalf("Codex usage label = %q, want the live login's email %q", got, email)
	}
}
