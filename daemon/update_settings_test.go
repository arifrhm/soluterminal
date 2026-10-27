package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUpdateSettingsDefaultsToOff(t *testing.T) {
	t.Setenv("SOLU_AUTO_UPDATE", "")
	t.Setenv("SOLU_AUTO_UPDATE_SHELL", "")
	st, err := newUpdateSettingsStore(t.TempDir())
	if err != nil {
		t.Fatalf("newUpdateSettingsStore: %v", err)
	}
	if got := st.Snapshot().AutoApply; got != updateAutoApplyOff {
		t.Fatalf("default autoApply = %q, want %q", got, updateAutoApplyOff)
	}
	if st.AutoApplyWhenIdle() {
		t.Fatal("AutoApplyWhenIdle true for off default")
	}
	if got := st.Snapshot().ShellAutoApply; got != updateAutoApplyWhenIdle {
		t.Fatalf("default shellAutoApply = %q, want %q", got, updateAutoApplyWhenIdle)
	}
}

func TestUpdateSettingsShellEnvDisablesDefault(t *testing.T) {
	t.Setenv("SOLU_AUTO_UPDATE_SHELL", "0")
	st, err := newUpdateSettingsStore(t.TempDir())
	if err != nil {
		t.Fatalf("newUpdateSettingsStore: %v", err)
	}
	if got := st.Snapshot().ShellAutoApply; got != updateAutoApplyOff {
		t.Fatalf("default shellAutoApply = %q, want %q", got, updateAutoApplyOff)
	}
}

func TestUpdateSettingsEnvSeedsWhenIdle(t *testing.T) {
	t.Setenv("SOLU_AUTO_UPDATE", "1")
	st, err := newUpdateSettingsStore(t.TempDir())
	if err != nil {
		t.Fatalf("newUpdateSettingsStore: %v", err)
	}
	if got := st.Snapshot().AutoApply; got != updateAutoApplyWhenIdle {
		t.Fatalf("default autoApply = %q, want %q", got, updateAutoApplyWhenIdle)
	}
	if !st.AutoApplyWhenIdle() {
		t.Fatal("AutoApplyWhenIdle false for when-idle default")
	}
}

func TestUpdateSettingsFileWinsOverEnv(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("SOLU_AUTO_UPDATE", "1")
	st, err := newUpdateSettingsStore(dir)
	if err != nil {
		t.Fatalf("newUpdateSettingsStore: %v", err)
	}
	if _, err := st.Set(updateSettings{AutoApply: updateAutoApplyOff, ShellAutoApply: updateAutoApplyWhenIdle}); err != nil {
		t.Fatalf("Set off: %v", err)
	}

	t.Setenv("SOLU_AUTO_UPDATE", "1")
	reloaded, err := newUpdateSettingsStore(dir)
	if err != nil {
		t.Fatalf("reload update settings: %v", err)
	}
	if got := reloaded.Snapshot().AutoApply; got != updateAutoApplyOff {
		t.Fatalf("reloaded autoApply = %q, want %q (file must win over env)", got, updateAutoApplyOff)
	}
}

func TestUpdateSettingsRejectsInvalidMode(t *testing.T) {
	st := defaultUpdateSettingsStore(t.TempDir())
	if _, err := st.Set(updateSettings{AutoApply: "always", ShellAutoApply: updateAutoApplyWhenIdle}); !errors.Is(err, errInvalidUpdateSettings) {
		t.Fatalf("Set invalid error = %v, want errInvalidUpdateSettings", err)
	}
}

func TestUpdateSettingsRejectsInvalidShellMode(t *testing.T) {
	st := defaultUpdateSettingsStore(t.TempDir())
	if _, err := st.Set(updateSettings{AutoApply: updateAutoApplyOff, ShellAutoApply: "always"}); !errors.Is(err, errInvalidUpdateSettings) {
		t.Fatalf("Set invalid shell error = %v, want errInvalidUpdateSettings", err)
	}
}

func TestUpdateSettingsLegacyFileUsesShellDefault(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("SOLU_AUTO_UPDATE_SHELL", "")
	if err := os.WriteFile(filepath.Join(dir, "update_settings.json"), []byte(`{"autoApply":"off"}`), 0o600); err != nil {
		t.Fatalf("write settings: %v", err)
	}
	st, err := newUpdateSettingsStore(dir)
	if err != nil {
		t.Fatalf("load legacy settings: %v", err)
	}
	if got := st.Snapshot().ShellAutoApply; got != updateAutoApplyWhenIdle {
		t.Fatalf("legacy shellAutoApply = %q, want %q", got, updateAutoApplyWhenIdle)
	}
}

func TestUpdateSettingsPutPreservesOmittedShellPreference(t *testing.T) {
	st := defaultUpdateSettingsStore(t.TempDir())
	if _, err := st.Set(updateSettings{AutoApply: updateAutoApplyOff, ShellAutoApply: updateAutoApplyOff}); err != nil {
		t.Fatalf("seed settings: %v", err)
	}
	a := &apiServer{updateSettings: st}
	rec := httptest.NewRecorder()
	a.handleUpdateSettings(rec, httptest.NewRequest(http.MethodPut, "/api/update/settings", strings.NewReader(`{"autoApply":"when-idle"}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var got updateSettings
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got.ShellAutoApply != updateAutoApplyOff {
		t.Fatalf("shellAutoApply = %q, want preserved off", got.ShellAutoApply)
	}
}

func TestUpdateSettingsPutRejectsInvalidShellPreference(t *testing.T) {
	st := defaultUpdateSettingsStore(t.TempDir())
	a := &apiServer{updateSettings: st}
	rec := httptest.NewRecorder()
	a.handleUpdateSettings(rec, httptest.NewRequest(http.MethodPut, "/api/update/settings", strings.NewReader(`{"autoApply":"off","shellAutoApply":"always"}`)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("PUT status = %d, want %d; body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
}

func TestUpdateSettingsRejectsInvalidFileMode(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "update_settings.json"), []byte(`{"autoApply":"always"}`), 0o600); err != nil {
		t.Fatalf("write settings: %v", err)
	}
	if _, err := newUpdateSettingsStore(dir); !errors.Is(err, errInvalidUpdateSettings) {
		t.Fatalf("load invalid error = %v, want errInvalidUpdateSettings", err)
	}
}
