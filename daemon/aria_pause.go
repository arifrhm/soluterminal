package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// ariaPauseStore persists the autoresponder kill-switch flag across
// daemon restarts. Pause used to be in-memory only; auto-update would
// silently resume after a binary swap, surprising users who paused
// because of a runaway. We persist the bool, and on next boot raise a
// one-shot "resume?" banner via an SSE event.
//
// Stored at <dataDir>/aria_pause.json next to aria_settings.json /
// aria_orders.json. Tiny file, atomic write-then-rename.

type ariaPauseFile struct {
	Paused bool `json:"paused"`
}

type ariaPauseStore struct {
	path string

	mu sync.Mutex
	s  ariaPauseFile
}

func newAriaPauseStore(dataDir string) (*ariaPauseStore, error) {
	st := &ariaPauseStore{
		path: filepath.Join(dataDir, "aria_pause.json"),
	}
	if err := st.load(); err != nil {
		return nil, err
	}
	return st, nil
}

func (s *ariaPauseStore) load() error {
	b, err := os.ReadFile(s.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("read aria pause: %w", err)
	}
	var loaded ariaPauseFile
	if err := json.Unmarshal(b, &loaded); err != nil {
		return fmt.Errorf("parse aria pause: %w", err)
	}
	s.mu.Lock()
	s.s = loaded
	s.mu.Unlock()
	return nil
}

func (s *ariaPauseStore) Paused() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.s.Paused
}

// Set persists the new flag. No-op if value matches the on-disk state
// to spare the disk on rapid toggles.
func (s *ariaPauseStore) Set(paused bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.s.Paused == paused {
		return nil
	}
	s.s.Paused = paused
	return s.saveLocked()
}

func (s *ariaPauseStore) saveLocked() error {
	b, err := json.MarshalIndent(s.s, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal aria pause: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return fmt.Errorf("mkdir aria pause: %w", err)
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return fmt.Errorf("write aria pause: %w", err)
	}
	if err := os.Rename(tmp, s.path); err != nil {
		return fmt.Errorf("rename aria pause: %w", err)
	}
	return nil
}
