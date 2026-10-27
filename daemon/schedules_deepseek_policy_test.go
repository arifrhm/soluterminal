package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestScheduleRejectsAndDisablesRetiredDeepseekModel(t *testing.T) {
	dir := t.TempDir()
	store, err := NewScheduleStore(dir, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Stop)
	for _, raw := range []string{"deepseek-v4-pro", " DEEPSEEK-V4-PRO ", "vendor/deepseek-v4-pro", "deepseek-v4-flash", "vendor/deepseek-chat", "deepseek-reasoner"} {
		_, err := store.Create(ScheduleInput{
			Name: "unsafe", Kind: "task", Cron: "0 9 * * *", Prompt: "test", Cwd: dir, Permission: "plan", Model: raw,
		})
		if err == nil || !strings.Contains(err.Error(), "retired DeepSeek") {
			t.Fatalf("model %q error=%v", raw, err)
		}
	}
	store.Stop()

	stale := &Schedule{ID: "sched_stale_deepseek", Name: "stale", Kind: "task", Cron: "0 9 * * *", Prompt: "test", Cwd: dir, Permission: "plan", Model: "vendor/DEEPSEEK-V4-PRO", Enabled: true}
	raw, err := json.Marshal(scheduleFile{Schedules: []*Schedule{stale}, Order: []string{stale.ID}})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "schedules.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	reloaded, err := NewScheduleStore(dir, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(reloaded.Stop)
	got, ok := reloaded.Get(stale.ID)
	if !ok || got.Enabled || got.ErrorCount != 1 || len(got.Runs) == 0 || !strings.Contains(got.Runs[0].Detail, "retired DeepSeek") {
		t.Fatalf("stale schedule did not fail closed: %+v ok=%v", got, ok)
	}
	persisted, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var saved scheduleFile
	if err := json.Unmarshal(persisted, &saved); err != nil {
		t.Fatal(err)
	}
	if len(saved.Schedules) != 1 || saved.Schedules[0].Enabled {
		t.Fatalf("disabled state was not persisted: %+v", saved.Schedules)
	}
}
