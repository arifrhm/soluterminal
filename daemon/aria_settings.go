package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// ARIA per-machine settings — the sweeper-model override. Stored in
// <dataDir>/aria_settings.json. Read-only since the panel's model
// picker was removed (edit the file or set ARIA_AUTO_MODEL to change
// it); the cloud force-routes the model regardless (cloud/aria.go),
// so this is a hint, not policy.

type ariaSettings struct {
	SweeperModel string `json:"sweeper_model"`
}

// ariaDefaultSweeperModel matches the original ariaAutoModel hardcoded
// default. Haiku 4.5 is fast/cheap enough for the menu-classification
// task the sweeper runs in a tight loop. Honored: ARIA_AUTO_MODEL env
// var still works as a bootstrap override for users who set it before
// the settings UI existed; the settings file wins if non-empty.
var ariaDefaultSweeperModel = func() string {
	if v := strings.TrimSpace(os.Getenv("ARIA_AUTO_MODEL")); v != "" {
		return v
	}
	return "claude-haiku-4-5"
}()

type ariaSettingsStore struct {
	path string

	mu sync.Mutex
	s  ariaSettings
}

func newAriaSettingsStore(dataDir string) (*ariaSettingsStore, error) {
	st := &ariaSettingsStore{
		path: filepath.Join(dataDir, "aria_settings.json"),
	}
	if err := st.load(); err != nil {
		return nil, err
	}
	return st, nil
}

func (s *ariaSettingsStore) load() error {
	b, err := os.ReadFile(s.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("read aria settings: %w", err)
	}
	var loaded ariaSettings
	if err := json.Unmarshal(b, &loaded); err != nil {
		return fmt.Errorf("parse aria settings: %w", err)
	}
	s.mu.Lock()
	s.s = loaded
	s.mu.Unlock()
	return nil
}

// SweeperModel returns the sweeper-model override or the default. Read
// per-tick by aria_auto.go so a UI change takes effect on the next eval.
func (s *ariaSettingsStore) SweeperModel() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if v := strings.TrimSpace(s.s.SweeperModel); v != "" {
		return v
	}
	return ariaDefaultSweeperModel
}
