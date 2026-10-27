package main

import (
	"path/filepath"
	"testing"
)

// native_settings_test.go mirrors recovery_settings_test.go for the per-machine
// native (Focus) toggle store. PromptSuggestions is a plain bool with no env
// override and no invalid-value branch, so the surface is smaller than recovery's.

func TestNativeSettingsDefaultsOff(t *testing.T) {
	t.Parallel()
	st, err := newNativeSettingsStore(t.TempDir())
	if err != nil {
		t.Fatalf("newNativeSettingsStore: %v", err)
	}
	if st.Snapshot().PromptSuggestions {
		t.Fatal("default PromptSuggestions = true, want false (opt-in)")
	}
	if st.PromptSuggestionsEnabled() {
		t.Fatal("PromptSuggestionsEnabled true for the default-OFF store")
	}
}

func TestNativeSettingsFileWinsOverDefault(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := writeFileAtomicSynced(filepath.Join(dir, "native_settings.json"), []byte(`{"promptSuggestions":true}`), 0o600); err != nil {
		t.Fatalf("seed file: %v", err)
	}
	st, err := newNativeSettingsStore(dir)
	if err != nil {
		t.Fatalf("newNativeSettingsStore: %v", err)
	}
	if !st.PromptSuggestionsEnabled() {
		t.Fatal("seeded file promptSuggestions=true was not honored on load")
	}
}

func TestNativeSettingsSetRoundTripAndReload(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	st, err := newNativeSettingsStore(dir)
	if err != nil {
		t.Fatalf("newNativeSettingsStore: %v", err)
	}
	got, err := st.Set(nativeSettings{PromptSuggestions: true})
	if err != nil {
		t.Fatalf("Set true: %v", err)
	}
	if !got.PromptSuggestions {
		t.Fatal("Set(true) returned PromptSuggestions=false")
	}
	reloaded, err := newNativeSettingsStore(dir)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if !reloaded.PromptSuggestionsEnabled() {
		t.Fatal("reloaded PromptSuggestions=false, want true (should have persisted)")
	}
	if _, err := reloaded.Set(nativeSettings{PromptSuggestions: false}); err != nil {
		t.Fatalf("Set false: %v", err)
	}
	again, err := newNativeSettingsStore(dir)
	if err != nil {
		t.Fatalf("reload2: %v", err)
	}
	if again.PromptSuggestionsEnabled() {
		t.Fatal("reloaded2 PromptSuggestions=true, want false (toggle off should persist)")
	}
}

// Tolerates an unknown future field without rejecting the whole file (forward-
// compat: a newer daemon may add fields an older one doesn't know).
func TestNativeSettingsIgnoresUnknownFields(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := writeFileAtomicSynced(filepath.Join(dir, "native_settings.json"), []byte(`{"promptSuggestions":true,"futureField":42}`), 0o600); err != nil {
		t.Fatalf("seed file: %v", err)
	}
	st, err := newNativeSettingsStore(dir)
	if err != nil {
		t.Fatalf("newNativeSettingsStore with unknown field: %v", err)
	}
	if !st.PromptSuggestionsEnabled() {
		t.Fatal("unknown sibling field should not block loading promptSuggestions=true")
	}
}
