package main

// Reminder insurance: one-time schedules with NotifyOnRun exist to reach the
// user's phone at a specific moment — but the local scheduler only runs while
// this machine is on. The daemon therefore mirrors its UPCOMING one-time
// reminders to the cloud (POST /api/internal/reminders, replace semantics),
// and a cloud sweeper delivers the push at run-at time if this daemon didn't.
//
// The contract is deliberately confirmation-free: after a local fire the
// schedule auto-disables, drops out of the upcoming set, and the very next
// sync removes its cloud row — so "row still present past run-at" IS the
// "daemon didn't handle it" signal. A daemon that fires and crashes before
// syncing produces at worst one duplicate push, which the clients coalesce
// by tag ("sched:<id>").
//
// Sync triggers piggyback on notifyChanged (every create/update/delete/
// enable/run) behind a debounce, plus SetPusher (cloud connect) so a fresh
// pairing uploads the existing set.

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"soluterminal-shared/machineauth"
)

// reminderSyncDebounce batches bursts (e.g. a run recording + auto-disable +
// save all notify within milliseconds) into one POST.
var reminderSyncDebounce = 2 * time.Second

type reminderSyncItem struct {
	SchedID string    `json:"schedId"`
	Name    string    `json:"name"`
	RunAt   time.Time `json:"runAt"`
}

// SetBackendID records which daemon this is when several share one machine
// claim (e.g. "wsl" + "win") so their cloud reminder rows don't clobber each
// other. Safe for concurrent use; set once at boot.
func (s *ScheduleStore) SetBackendID(id string) {
	s.mu.Lock()
	s.backendID = id
	s.mu.Unlock()
}

// collectUpcomingReminders snapshots the one-time, enabled, notify-on-run
// schedules whose moment is still ahead. Caller must hold s.mu.
func (s *ScheduleStore) collectUpcomingRemindersUnlocked(now time.Time) []reminderSyncItem {
	out := []reminderSyncItem{} // non-nil: an empty sync CLEARS the cloud set
	for _, id := range s.order {
		sched, ok := s.items[id]
		if !ok || !sched.oneTime() || !sched.Enabled || !sched.NotifyOnRun {
			continue
		}
		if sched.RunAt == nil || !sched.RunAt.After(now) {
			continue
		}
		out = append(out, reminderSyncItem{SchedID: sched.ID, Name: sched.Name, RunAt: *sched.RunAt})
	}
	return out
}

// scheduleReminderSync arms (or re-arms) the debounced sync. Called from
// notifyChanged and SetPusher; cheap no-op when no cloud is connected.
func (s *ScheduleStore) scheduleReminderSync() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pusher == nil {
		return
	}
	if s.reminderSyncTimer != nil {
		s.reminderSyncTimer.Stop()
	}
	s.reminderSyncTimer = time.AfterFunc(reminderSyncDebounce, s.syncRemindersNow)
}

func (s *ScheduleStore) syncRemindersNow() {
	s.mu.Lock()
	pusher := s.pusher
	backendID := s.backendID
	items := s.collectUpcomingRemindersUnlocked(time.Now())
	s.mu.Unlock()
	if pusher == nil {
		return
	}
	pusher.SyncReminders(backendID, items)
}

// reminderPayload is what the cloud may see of the reminders: with only
// approved devices allowed (Pusher.private), the name the person gave the
// reminder is replaced, like push text (minimisePushBody); the cloud only
// needs the id and the time to deliver the safety-net push.
func (p *Pusher) reminderPayload(items []reminderSyncItem) []reminderSyncItem {
	if p.private == nil || !p.private() {
		return items
	}
	out := make([]reminderSyncItem, len(items))
	for i, it := range items {
		it.Name = "Reminder"
		out[i] = it
	}
	return out
}

// SyncReminders POSTs the machine's full upcoming-reminder set to the cloud
// (replace semantics — idempotent and self-healing after either side loses
// state). Best-effort: a failed sync just means the cloud safety net lags
// until the next schedules change; the local scheduler is unaffected.
func (p *Pusher) SyncReminders(backendID string, items []reminderSyncItem) {
	token := p.getToken()
	if token == "" {
		return
	}
	body, _ := json.Marshal(map[string]any{
		"backendId": backendID,
		"reminders": p.reminderPayload(items),
	})
	req, err := http.NewRequest("POST", p.cloudBase+"/api/internal/reminders", bytes.NewReader(body))
	if err != nil {
		return
	}
	machineauth.SignRequest(req, token, body, time.Now())
	resp, err := p.client.Do(req)
	if err != nil {
		slog.Warn("reminder sync failed", "err", err)
		return
	}
	defer resp.Body.Close()
	// 404 = older cloud without the endpoint — fine, feature simply absent.
	if resp.StatusCode >= 400 && resp.StatusCode != 404 {
		slog.Warn("reminder sync rejected", "http", resp.StatusCode)
		return
	}
	slog.Debug("reminder sync ok", "count", len(items), "http", resp.StatusCode)
}
