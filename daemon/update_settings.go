package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Per-install self-update preferences. `off` keeps the current
// notify-then-click flow; `when-idle` lets the client auto-apply the daemon
// swap the moment it's safe (no native/Focus pane mid-turn) — see the client's
// maybeAutoApplyUpdate + applyUpdate({auto}). Daemon-stored (not localStorage)
// so it's correctly scoped to the install, survives a webview cache clear, and
// can drive a future headless auto-apply from the poll itself.
type updateAutoApplyMode string

const (
	updateAutoApplyOff      updateAutoApplyMode = "off"
	updateAutoApplyWhenIdle updateAutoApplyMode = "when-idle"
)

var errInvalidUpdateSettings = errors.New("invalid update settings")

type updateSettings struct {
	AutoApply      updateAutoApplyMode `json:"autoApply"`
	ShellAutoApply updateAutoApplyMode `json:"shellAutoApply"`
}

type updateSettingsStore struct {
	path string

	mu sync.Mutex
	s  updateSettings
}

func defaultUpdateSettings() updateSettings {
	mode := updateAutoApplyOff
	if os.Getenv("SOLU_AUTO_UPDATE") == "1" {
		mode = updateAutoApplyWhenIdle
	}
	shellMode := updateAutoApplyWhenIdle
	if os.Getenv("SOLU_AUTO_UPDATE_SHELL") == "0" {
		shellMode = updateAutoApplyOff
	}
	return updateSettings{AutoApply: mode, ShellAutoApply: shellMode}
}

func newUpdateSettingsStore(dataDir string) (*updateSettingsStore, error) {
	st := defaultUpdateSettingsStore(dataDir)
	if err := st.load(); err != nil {
		return nil, err
	}
	return st, nil
}

func defaultUpdateSettingsStore(dataDir string) *updateSettingsStore {
	return &updateSettingsStore{
		path: filepath.Join(dataDir, "update_settings.json"),
		s:    defaultUpdateSettings(),
	}
}

func (s *updateSettingsStore) load() error {
	b, err := os.ReadFile(s.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("read update settings: %w", err)
	}
	// Seed from defaults so files written before shellAutoApply existed opt in
	// to the new zero-click shell behavior instead of failing validation.
	loaded := defaultUpdateSettings()
	if err := json.Unmarshal(b, &loaded); err != nil {
		return fmt.Errorf("parse update settings: %w", err)
	}
	mode, ok := normalizeUpdateAutoApplyMode(loaded.AutoApply)
	if !ok {
		return fmt.Errorf("%w: autoApply must be off or when-idle", errInvalidUpdateSettings)
	}
	shellMode, ok := normalizeUpdateAutoApplyMode(loaded.ShellAutoApply)
	if !ok {
		return fmt.Errorf("%w: shellAutoApply must be off or when-idle", errInvalidUpdateSettings)
	}
	s.mu.Lock()
	s.s = updateSettings{AutoApply: mode, ShellAutoApply: shellMode}
	s.mu.Unlock()
	return nil
}

func (s *updateSettingsStore) Snapshot() updateSettings {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.s
}

// AutoApplyWhenIdle reports whether the install opted into unattended
// swap-when-safe. Wired for a future daemon-driven headless apply; the v1
// trigger lives in the client.
func (s *updateSettingsStore) AutoApplyWhenIdle() bool {
	return s.Snapshot().AutoApply == updateAutoApplyWhenIdle
}

func (s *updateSettingsStore) Set(next updateSettings) (updateSettings, error) {
	mode, ok := normalizeUpdateAutoApplyMode(next.AutoApply)
	if !ok {
		return updateSettings{}, fmt.Errorf("%w: autoApply must be off or when-idle", errInvalidUpdateSettings)
	}
	shellMode, ok := normalizeUpdateAutoApplyMode(next.ShellAutoApply)
	if !ok {
		return updateSettings{}, fmt.Errorf("%w: shellAutoApply must be off or when-idle", errInvalidUpdateSettings)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.s.AutoApply = mode
	s.s.ShellAutoApply = shellMode
	if err := s.saveLocked(); err != nil {
		return updateSettings{}, err
	}
	return s.s, nil
}

func (s *updateSettingsStore) saveLocked() error {
	b, err := json.MarshalIndent(s.s, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal update settings: %w", err)
	}
	if err := writeFileAtomicSynced(s.path, b, 0o600); err != nil {
		return fmt.Errorf("write update settings: %w", err)
	}
	return nil
}

func normalizeUpdateAutoApplyMode(mode updateAutoApplyMode) (updateAutoApplyMode, bool) {
	switch updateAutoApplyMode(strings.TrimSpace(string(mode))) {
	case updateAutoApplyOff:
		return updateAutoApplyOff, true
	case updateAutoApplyWhenIdle:
		return updateAutoApplyWhenIdle, true
	default:
		return "", false
	}
}

func (a *apiServer) handleUpdateSettings(w http.ResponseWriter, r *http.Request) {
	if a.updateSettings == nil {
		http.Error(w, "update settings unavailable", http.StatusServiceUnavailable)
		return
	}
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, a.updateSettings.Snapshot())
	case http.MethodPut:
		// Start from the snapshot so an older web bundle that PUTs only
		// autoApply preserves the shell preference.
		req := a.updateSettings.Snapshot()
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid json: "+err.Error(), http.StatusBadRequest)
			return
		}
		updated, err := a.updateSettings.Set(req)
		if err != nil {
			status := http.StatusInternalServerError
			if errors.Is(err, errInvalidUpdateSettings) {
				status = http.StatusBadRequest
			}
			http.Error(w, err.Error(), status)
			return
		}
		writeJSON(w, http.StatusOK, updated)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}
