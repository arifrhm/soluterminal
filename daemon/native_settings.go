package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sync"
)

// nativeSettings holds per-machine toggles for native (Focus-renderer) panes.
// It mirrors recovery_settings.go: a JSON file in the data dir, mutex-guarded,
// atomic-write persistence, read fresh at every (re)spawn (see pane_spawn.go).
type nativeSettings struct {
	// PromptSuggestions opt-in for Claude's own model-generated "next-prompt"
	// suggestions (emitted as prompt_suggestion stream-json events ~10-20s after
	// a turn, best-effort). When on, each native-pane turn triggers one extra
	// (cached, cheap) background model call, so it defaults OFF.
	PromptSuggestions bool `json:"promptSuggestions"`
	// DefaultClaudeMode is the renderer NEW Claude sessions preselect in the
	// new-session dialog: "" (terminal, the shipping default) or "native"
	// (Focus). Purely a client-default hint — the per-session choice always
	// wins; the daemon stores it so every device the user opens agrees.
	DefaultClaudeMode string `json:"defaultClaudeMode"`
}

type nativeSettingsStore struct {
	path string

	mu sync.Mutex
	s  nativeSettings
}

func defaultNativeSettings() nativeSettings {
	return nativeSettings{} // zero value → PromptSuggestions OFF
}

func defaultNativeSettingsStore(dataDir string) *nativeSettingsStore {
	return &nativeSettingsStore{
		path: filepath.Join(dataDir, "native_settings.json"),
		s:    defaultNativeSettings(),
	}
}

func newNativeSettingsStore(dataDir string) (*nativeSettingsStore, error) {
	st := defaultNativeSettingsStore(dataDir)
	if err := st.load(); err != nil {
		return nil, err
	}
	return st, nil
}

func (s *nativeSettingsStore) load() error {
	b, err := os.ReadFile(s.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("read native settings: %w", err)
	}
	var loaded nativeSettings
	if err := json.Unmarshal(b, &loaded); err != nil {
		return fmt.Errorf("parse native settings: %w", err)
	}
	s.mu.Lock()
	s.s = nativeSettings{
		PromptSuggestions: loaded.PromptSuggestions,
		DefaultClaudeMode: normalizeDefaultClaudeMode(loaded.DefaultClaudeMode),
	}
	s.mu.Unlock()
	return nil
}

// normalizeDefaultClaudeMode clamps the stored/requested value to the two
// valid kinds — an unknown value (future client, hand-edited file) degrades to
// the terminal default rather than poisoning the dialog.
//
// While Focus is retired (nativeRendererEnabled false) the default is always
// terminal, so clients that preselect it (the mobile new-session sheet, an
// older web bundle) never offer a renderer the daemon refuses.
func normalizeDefaultClaudeMode(v string) string {
	if v == "native" && nativeRendererEnabled() {
		return "native"
	}
	return ""
}

func (s *nativeSettingsStore) Snapshot() nativeSettings {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.s
}

// PromptSuggestionsEnabled reports whether the per-machine prompt-suggestions
// toggle is on. Read by the Manager at every native (re)spawn to decide whether
// to add --prompt-suggestions to the headless claude argv.
func (s *nativeSettingsStore) PromptSuggestionsEnabled() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.s.PromptSuggestions
}

func (s *nativeSettingsStore) Set(next nativeSettings) (nativeSettings, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.s.PromptSuggestions = next.PromptSuggestions
	s.s.DefaultClaudeMode = normalizeDefaultClaudeMode(next.DefaultClaudeMode)
	if err := s.saveLocked(); err != nil {
		return nativeSettings{}, err
	}
	return s.s, nil
}

func (s *nativeSettingsStore) saveLocked() error {
	b, err := json.MarshalIndent(s.s, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal native settings: %w", err)
	}
	if err := writeFileAtomicSynced(s.path, b, 0o600); err != nil {
		return fmt.Errorf("write native settings: %w", err)
	}
	return nil
}

// handleNativeSettings serves GET/PUT /api/native/settings. Mirrors
// handleRecoverySettings. A nil store (e.g. some tests) → 503.
func (a *apiServer) handleNativeSettings(w http.ResponseWriter, r *http.Request) {
	if a.nativeSettings == nil {
		http.Error(w, "native settings unavailable", http.StatusServiceUnavailable)
		return
	}
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, a.nativeSettings.Snapshot())
	case http.MethodPut:
		var req struct {
			PromptSuggestions bool   `json:"promptSuggestions"`
			DefaultClaudeMode string `json:"defaultClaudeMode"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid json: "+err.Error(), http.StatusBadRequest)
			return
		}
		updated, err := a.nativeSettings.Set(nativeSettings{
			PromptSuggestions: req.PromptSuggestions,
			DefaultClaudeMode: req.DefaultClaudeMode,
		})
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, updated)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}
