package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestUpdateRollbackStoreDefaultsAndCorruptFallback(t *testing.T) {
	dir := t.TempDir()
	store, err := newUpdateRollbackStore(dir)
	if err != nil || store.Snapshot().Schema != updateRollbackSchema {
		t.Fatalf("default store = %#v, %v", store, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "update_rollback.json"), []byte(`{"schema":`), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err = newUpdateRollbackStore(dir)
	if err != nil || store.Snapshot().Hold != nil {
		t.Fatalf("corrupt store did not fall back: %#v, %v", store.Snapshot(), err)
	}
}

func TestUpdateRollbackStoreHoldBoundsExpiryAndPersistence(t *testing.T) {
	dir := t.TempDir()
	store, _ := newUpdateRollbackStore(dir)
	now := time.Unix(1_800_000_000, 0)
	for _, tc := range []struct {
		name string
		d    time.Duration
		want time.Duration
	}{
		{"minimum", time.Minute, minRollbackHoldDuration},
		{"default", 0, defaultRollbackHoldDuration},
		{"maximum", 30 * 24 * time.Hour, maxRollbackHoldDuration},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := rollbackHold{Version: "2.0.0", FromVersion: "1.0.0", CreatedAtMs: now.UnixMilli()}
			if tc.d > 0 {
				h.ExpiresAtMs = now.Add(tc.d).UnixMilli()
			}
			if err := store.SetHold(h, now); err != nil {
				t.Fatal(err)
			}
			got, ok := store.ActiveHold(now)
			if !ok || time.Duration(got.ExpiresAtMs-got.CreatedAtMs)*time.Millisecond != tc.want {
				t.Fatalf("hold = %+v, want duration %s", got, tc.want)
			}
		})
	}
	info, err := os.Stat(filepath.Join(dir, "update_rollback.json"))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v, err=%v", info.Mode().Perm(), err)
	}
	fresh, _ := newUpdateRollbackStore(dir)
	hold, ok := fresh.ActiveHold(now.Add(-time.Hour))
	if !ok || hold.Version != "2.0.0" {
		t.Fatalf("backwards clock released persisted hold: %+v, %v", hold, ok)
	}
	if _, ok := fresh.ActiveHold(time.UnixMilli(hold.ExpiresAtMs)); ok {
		t.Fatal("hold remained active at its expiry boundary")
	}
	again, _ := newUpdateRollbackStore(dir)
	if again.Snapshot().Hold != nil {
		t.Fatal("expired hold was resurrected from disk")
	}
}

func TestUpdateRollbackStoreHistoryDedupeCapClearAndUnknownFields(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "update_rollback.json")
	if err := os.WriteFile(path, []byte(`{"schema":1,"history":[],"future":{"kept":true}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	store, _ := newUpdateRollbackStore(dir)
	now := time.Now()
	for _, version := range []string{"1.0.0", "1.0.1", "1.0.2", "1.0.3", "1.0.1"} {
		store.RecordRan(version, now)
	}
	history := store.Snapshot().History
	if len(history) != oldRingDepth+1 || history[0].Version != "1.0.1" {
		t.Fatalf("history = %+v", history)
	}
	if err := store.SetHold(rollbackHold{Version: "2.0.0"}, now); err != nil {
		t.Fatal(err)
	}
	if err := store.ClearHold("test release"); err != nil {
		t.Fatal(err)
	}
	var raw map[string]json.RawMessage
	b, _ := os.ReadFile(path)
	if err := json.Unmarshal(b, &raw); err != nil || raw["future"] == nil {
		t.Fatalf("unknown field lost: %s (%v)", b, err)
	}
	fresh, _ := newUpdateRollbackStore(dir)
	if fresh.Snapshot().Hold != nil || fresh.Snapshot().HoldReleaseReason != "test release" {
		t.Fatalf("cleared hold persisted incorrectly: %+v", fresh.Snapshot())
	}
}

func TestReconcileRollbackAttemptAtBoot(t *testing.T) {
	store, _ := newUpdateRollbackStore(t.TempDir())
	now := time.Now()
	_ = store.SetLastAttempt(rollbackAttempt{TargetVersion: "1.0.0", State: "swapped", AtMs: now.Add(-time.Minute).UnixMilli()})
	reconcileRollbackAttemptAtBoot(store, "2.0.0", now)
	attempt := store.Snapshot().LastAttempt
	if attempt == nil || attempt.State != "failed" || attempt.Error == "" {
		t.Fatalf("failed boot reconcile = %+v", attempt)
	}
	_ = store.SetLastAttempt(rollbackAttempt{TargetVersion: "1.0.0", State: "swapped", AtMs: now.UnixMilli()})
	reconcileRollbackAttemptAtBoot(store, "1.0.0", now)
	if got := store.Snapshot().LastAttempt; got == nil || got.State != "completed" || got.Error != "" {
		t.Fatalf("successful boot reconcile = %+v", got)
	}
}
