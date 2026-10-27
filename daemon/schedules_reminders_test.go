package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

func TestCollectUpcomingReminders(t *testing.T) {
	s := newTestScheduleStore(t)
	now := time.Now()
	future := now.Add(2 * time.Hour)
	past := now.Add(-time.Hour)

	mk := func(runAt *time.Time, cron string, notify, enabled bool) *Schedule {
		t.Helper()
		en := enabled
		sched, err := s.Create(ScheduleInput{
			Cron: cron, RunAt: runAt, PaneID: "p1", Prompt: "x",
			NotifyOnRun: notify, Enabled: &en,
		})
		if err != nil {
			t.Fatal(err)
		}
		return sched
	}

	included := mk(&future, "", true, true)
	mk(&future, "", false, true)     // notify off → excluded
	mk(nil, "0 9 * * *", true, true) // recurring → excluded
	disabled := mk(&future, "", true, false)
	_ = disabled

	// A one-time whose moment already passed (simulated by backdating) is
	// excluded — the cloud must never re-deliver history.
	stale := mk(&future, "", true, true)
	s.mu.Lock()
	s.items[stale.ID].RunAt = &past
	s.mu.Unlock()

	s.mu.Lock()
	items := s.collectUpcomingRemindersUnlocked(now)
	s.mu.Unlock()

	if len(items) != 1 || items[0].SchedID != included.ID {
		t.Fatalf("collect = %+v, want exactly the one enabled future notify-on-run one-timer (%s)", items, included.ID)
	}
	if items[0].Name != included.Name || !items[0].RunAt.Equal(future) {
		t.Errorf("item fields wrong: %+v", items[0])
	}
}

// The debounced sync fires one POST carrying the current upcoming set, and
// an empty set still POSTs (that's what clears the cloud mirror).
func TestReminderSyncPostsToCloud(t *testing.T) {
	prev := reminderSyncDebounce
	reminderSyncDebounce = 20 * time.Millisecond
	t.Cleanup(func() { reminderSyncDebounce = prev })

	var mu sync.Mutex
	var bodies []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/internal/reminders" {
			http.NotFound(w, r)
			return
		}
		var m map[string]any
		_ = json.NewDecoder(r.Body).Decode(&m)
		mu.Lock()
		bodies = append(bodies, m)
		mu.Unlock()
		_, _ = w.Write([]byte(`{"stored":1}`))
	}))
	defer srv.Close()

	s := newTestScheduleStore(t)
	s.SetBackendID("wsl")
	s.SetPusher(NewPusher(srv.URL, "tok", nil, nil, nil))

	future := time.Now().Add(time.Hour)
	if _, err := s.Create(ScheduleInput{RunAt: &future, PaneID: "p1", Prompt: "remind me", NotifyOnRun: true}); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := len(bodies)
		mu.Unlock()
		if n > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(bodies) == 0 {
		t.Fatal("no sync POST arrived")
	}
	last := bodies[len(bodies)-1]
	if last["backendId"] != "wsl" {
		t.Errorf("backendId = %v, want wsl", last["backendId"])
	}
	rems, _ := last["reminders"].([]any)
	if len(rems) != 1 {
		t.Fatalf("reminders = %v, want 1", last["reminders"])
	}
	first, _ := rems[0].(map[string]any)
	if first["name"] == "" || first["schedId"] == "" {
		t.Errorf("reminder item incomplete: %v", first)
	}
}
