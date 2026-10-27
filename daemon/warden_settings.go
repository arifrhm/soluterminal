package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// The Warden is an OPT-IN, event-driven fleet caretaker: a short-lived
// headless Claude run summoned by the daemon when a pane finishes real work
// (debrief) or has been asking for the user too long (triage). It is DEFAULT
// OFF — nothing spawns until the install explicitly enables it — and its tool
// budget is enforced daemon-side by a scoped capability token (mcp_scope.go),
// not by prompt text.
//
// Settings live in <dataDir>/warden_settings.json. This file owns only the
// store; the summoner is warden.go and the HTTP surface is warden_api.go.

var errInvalidWardenSettings = errors.New("invalid warden settings")

const (
	wardenDefaultTriageAfterMin = 10
	wardenDefaultMaxRunsPerHour = 6
	// Guardrails so a bad PUT can't turn the caretaker into a quota fire-hose
	// or a no-op. Runs/hour is the spend lever; triage delay is how patient it
	// is before preparing a brief the real-time push already fired for.
	wardenMaxRunsPerHourCeiling = 30
	wardenMinTriageAfterMin     = 2
	wardenMaxTriageAfterMin     = 120
)

type wardenSettings struct {
	// Enabled is the master switch. False (the default) means the summoner
	// loop runs but never spawns — the whole feature is inert.
	Enabled bool `json:"enabled"`
	// AutoFlags permits episode-bound triage flags. Old settings omit it and
	// therefore remain off even when the master switch is enabled.
	AutoFlags bool `json:"autoFlags"`
	// Model overrides the model for warden runs ("" = the account default).
	// A cheaper model is usually right for read-and-summarize work.
	Model string `json:"model,omitempty"`
	// TriageAfterMin is how long a pane must sustain needs-user before a
	// triage run is summoned. The real-time push already fired earlier; this
	// is "the user still hasn't come, prepare a proper brief."
	TriageAfterMin int `json:"triageAfterMin,omitempty"`
	// MaxRunsPerHour caps total warden spawns across all event types — the
	// spend ceiling and the fork-bomb guard.
	MaxRunsPerHour int `json:"maxRunsPerHour,omitempty"`
}

func defaultWardenSettings() wardenSettings {
	return wardenSettings{
		Enabled:        os.Getenv("SOLU_WARDEN") == "1",
		Model:          "",
		TriageAfterMin: wardenDefaultTriageAfterMin,
		MaxRunsPerHour: wardenDefaultMaxRunsPerHour,
	}
}

// normalizeWardenSettings clamps a candidate into valid ranges and fills
// zero values with defaults. Returns an error only for structurally invalid
// input (none today — everything clamps), keeping the shape parallel to the
// update-settings store.
func normalizeWardenSettings(in wardenSettings) wardenSettings {
	out := in
	if out.TriageAfterMin == 0 {
		out.TriageAfterMin = wardenDefaultTriageAfterMin
	}
	if out.TriageAfterMin < wardenMinTriageAfterMin {
		out.TriageAfterMin = wardenMinTriageAfterMin
	}
	if out.TriageAfterMin > wardenMaxTriageAfterMin {
		out.TriageAfterMin = wardenMaxTriageAfterMin
	}
	if out.MaxRunsPerHour == 0 {
		out.MaxRunsPerHour = wardenDefaultMaxRunsPerHour
	}
	if out.MaxRunsPerHour < 1 {
		out.MaxRunsPerHour = 1
	}
	if out.MaxRunsPerHour > wardenMaxRunsPerHourCeiling {
		out.MaxRunsPerHour = wardenMaxRunsPerHourCeiling
	}
	return out
}

type wardenSettingsStore struct {
	path string
	mu   sync.Mutex
	s    wardenSettings
}

func newWardenSettingsStore(dataDir string) (*wardenSettingsStore, error) {
	st := &wardenSettingsStore{
		path: filepath.Join(dataDir, "warden_settings.json"),
		s:    defaultWardenSettings(),
	}
	if err := st.load(); err != nil {
		return nil, err
	}
	return st, nil
}

func (s *wardenSettingsStore) load() error {
	b, err := os.ReadFile(s.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("read warden settings: %w", err)
	}
	var loaded wardenSettings
	if err := json.Unmarshal(b, &loaded); err != nil {
		return fmt.Errorf("parse warden settings: %w", err)
	}
	s.mu.Lock()
	s.s = normalizeWardenSettings(loaded)
	s.mu.Unlock()
	return nil
}

func (s *wardenSettingsStore) Snapshot() wardenSettings {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.s
}

func (s *wardenSettingsStore) Enabled() bool {
	return s.Snapshot().Enabled
}

func (s *wardenSettingsStore) Set(next wardenSettings) (wardenSettings, error) {
	norm := normalizeWardenSettings(next)
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.saveLocked(norm); err != nil {
		return wardenSettings{}, err
	}
	s.s = norm
	return s.s, nil
}

type wardenSettingsPatch struct {
	Enabled        *bool   `json:"enabled"`
	AutoFlags      *bool   `json:"autoFlags"`
	Model          *string `json:"model"`
	TriageAfterMin *int    `json:"triageAfterMin"`
	MaxRunsPerHour *int    `json:"maxRunsPerHour"`
}

// Patch merges under one lock so concurrent disjoint PUTs cannot erase one
// another. Persist before publishing the new live value.
func (s *wardenSettingsStore) Patch(p wardenSettingsPatch) (wardenSettings, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	next := s.s
	if p.Enabled != nil {
		next.Enabled = *p.Enabled
	}
	if p.AutoFlags != nil {
		next.AutoFlags = *p.AutoFlags
	}
	if p.Model != nil {
		next.Model = *p.Model
	}
	if p.TriageAfterMin != nil {
		next.TriageAfterMin = *p.TriageAfterMin
	}
	if p.MaxRunsPerHour != nil {
		next.MaxRunsPerHour = *p.MaxRunsPerHour
	}
	next = normalizeWardenSettings(next)
	if err := s.saveLocked(next); err != nil {
		return wardenSettings{}, err
	}
	s.s = next
	return next, nil
}

func (s *wardenSettingsStore) saveLocked(next wardenSettings) error {
	b, err := json.MarshalIndent(next, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal warden settings: %w", err)
	}
	if err := writeFileAtomicSynced(s.path, b, 0o600); err != nil {
		return fmt.Errorf("write warden settings: %w", err)
	}
	return nil
}
