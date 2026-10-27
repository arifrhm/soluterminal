package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	updateRollbackSchema        = 1
	minRollbackHoldDuration     = time.Hour
	maxRollbackHoldDuration     = 14 * 24 * time.Hour
	defaultRollbackHoldDuration = 72 * time.Hour
)

type rollbackHistoryEntry struct {
	Version string `json:"version"`
	RanAtMs int64  `json:"ranAtMs"`
}

type rollbackHold struct {
	Version     string `json:"version"`
	FromVersion string `json:"fromVersion"`
	Reason      string `json:"reason"`
	CreatedAtMs int64  `json:"createdAtMs"`
	ExpiresAtMs int64  `json:"expiresAtMs"`
}

type rollbackAttempt struct {
	TargetVersion string `json:"targetVersion"`
	State         string `json:"state"`
	Error         string `json:"error,omitempty"`
	AtMs          int64  `json:"atMs"`
}

type updateRollbackState struct {
	Schema            int                    `json:"schema"`
	History           []rollbackHistoryEntry `json:"history"`
	Hold              *rollbackHold          `json:"hold,omitempty"`
	LastAttempt       *rollbackAttempt       `json:"lastAttempt,omitempty"`
	HoldReleaseReason string                 `json:"holdReleaseReason,omitempty"`
	unknown           map[string]json.RawMessage
}

type updateRollbackStore struct {
	path string

	mu sync.Mutex
	s  updateRollbackState
}

func defaultUpdateRollbackState() updateRollbackState {
	return updateRollbackState{Schema: updateRollbackSchema, History: []rollbackHistoryEntry{}}
}

func defaultUpdateRollbackStore(dataDir string) *updateRollbackStore {
	return &updateRollbackStore{
		path: filepath.Join(dataDir, "update_rollback.json"),
		s:    defaultUpdateRollbackState(),
	}
}

func newUpdateRollbackStore(dataDir string) (*updateRollbackStore, error) {
	s := defaultUpdateRollbackStore(dataDir)
	// Rollback bookkeeping is recovery metadata, never a boot dependency. A
	// corrupt, truncated, or unreadable file starts from safe empty defaults.
	if err := s.load(); err != nil {
		s.s = defaultUpdateRollbackState()
	}
	return s, nil
}

func (s *updateRollbackStore) load() error {
	b, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read update rollback state: %w", err)
	}
	loaded := defaultUpdateRollbackState()
	if err := json.Unmarshal(b, &loaded); err != nil {
		return fmt.Errorf("parse update rollback state: %w", err)
	}
	if loaded.Schema != updateRollbackSchema {
		return fmt.Errorf("unsupported update rollback schema %d", loaded.Schema)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err == nil {
		for _, key := range []string{"schema", "history", "hold", "lastAttempt", "holdReleaseReason"} {
			delete(raw, key)
		}
		loaded.unknown = raw
	}
	s.s = loaded
	return nil
}

func cloneRollbackState(in updateRollbackState) updateRollbackState {
	out := in
	out.History = append([]rollbackHistoryEntry(nil), in.History...)
	if in.Hold != nil {
		v := *in.Hold
		out.Hold = &v
	}
	if in.LastAttempt != nil {
		v := *in.LastAttempt
		out.LastAttempt = &v
	}
	return out
}

func (s *updateRollbackStore) Snapshot() updateRollbackState {
	s.mu.Lock()
	defer s.mu.Unlock()
	return cloneRollbackState(s.s)
}

func (s *updateRollbackStore) RecordRan(version string, now time.Time) {
	version = strings.TrimSpace(version)
	if version == "" || version == "dev" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	history := []rollbackHistoryEntry{{Version: version, RanAtMs: now.UnixMilli()}}
	for _, item := range s.s.History {
		if item.Version != version {
			history = append(history, item)
		}
		if len(history) >= oldRingDepth+1 {
			break
		}
	}
	s.s.History = history
	_ = s.saveLocked()
}

func rollbackHoldDuration(hold rollbackHold, now time.Time) time.Duration {
	created := time.UnixMilli(hold.CreatedAtMs)
	if hold.CreatedAtMs <= 0 {
		created = now
	}
	d := time.UnixMilli(hold.ExpiresAtMs).Sub(created)
	if hold.ExpiresAtMs <= 0 {
		d = defaultRollbackHoldDuration
	}
	if d < minRollbackHoldDuration {
		d = minRollbackHoldDuration
	}
	if d > maxRollbackHoldDuration {
		d = maxRollbackHoldDuration
	}
	return d
}

func (s *updateRollbackStore) SetHold(hold rollbackHold, now time.Time) error {
	d := rollbackHoldDuration(hold, now)
	hold.CreatedAtMs = now.UnixMilli()
	hold.ExpiresAtMs = now.Add(d).UnixMilli()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.s.Hold = &hold
	s.s.HoldReleaseReason = ""
	return s.saveLocked()
}

func (s *updateRollbackStore) ClearHold(reason string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.s.Hold = nil
	s.s.HoldReleaseReason = strings.TrimSpace(reason)
	return s.saveLocked()
}

func (s *updateRollbackStore) ActiveHold(now time.Time) (rollbackHold, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.s.Hold == nil {
		return rollbackHold{}, false
	}
	hold := *s.s.Hold
	// A backwards wall clock must not accidentally release a safety hold.
	if now.UnixMilli() < hold.CreatedAtMs || now.UnixMilli() < hold.ExpiresAtMs {
		return hold, true
	}
	s.s.Hold = nil
	s.s.HoldReleaseReason = "hold expired"
	_ = s.saveLocked()
	return rollbackHold{}, false
}

func (s *updateRollbackStore) SetLastAttempt(attempt rollbackAttempt) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	v := attempt
	s.s.LastAttempt = &v
	return s.saveLocked()
}

func (s *updateRollbackStore) ClearLastAttempt() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.s.LastAttempt = nil
	return s.saveLocked()
}

func (s *updateRollbackStore) saveLocked() error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return fmt.Errorf("create update rollback state directory: %w", err)
	}
	known := cloneRollbackState(s.s)
	known.unknown = nil
	b, err := json.Marshal(known)
	if err != nil {
		return fmt.Errorf("marshal update rollback state: %w", err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	for key, value := range s.s.unknown {
		if _, exists := raw[key]; !exists {
			raw[key] = value
		}
	}
	b, err = json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return err
	}
	if err := writeFileAtomicSynced(s.path, b, 0o600); err != nil {
		return fmt.Errorf("write update rollback state: %w", err)
	}
	return nil
}

func reconcileRollbackAttemptAtBoot(store *updateRollbackStore, runningVersion string, now time.Time) {
	if store == nil {
		return
	}
	snapshot := store.Snapshot()
	if snapshot.LastAttempt == nil || snapshot.LastAttempt.State != "swapped" {
		return
	}
	attempt := *snapshot.LastAttempt
	attempt.AtMs = now.UnixMilli()
	if attempt.TargetVersion == runningVersion {
		attempt.State = "completed"
		attempt.Error = ""
	} else {
		attempt.State = "failed"
		attempt.Error = "rollback target did not remain running; the previous binary was restored by boot recovery and the rollback can be retried"
	}
	_ = store.SetLastAttempt(attempt)
}
