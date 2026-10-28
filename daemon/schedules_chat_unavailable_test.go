package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// A chat-delivery schedule in a build without the chat feature still tells
// the user: the run is stamped and a schedule-error push goes out, even with
// NotifyOnRun off.
func TestChatDeliveryUnavailableStampsAndPushes(t *testing.T) {
	prev := scheduleChatDelivery
	scheduleChatDelivery = nil
	t.Cleanup(func() { scheduleChatDelivery = prev })

	var mu sync.Mutex
	var pushes []map[string]string
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var m map[string]string
		_ = json.NewDecoder(r.Body).Decode(&m)
		mu.Lock()
		pushes = append(pushes, m)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"sent":1}`))
	}))
	defer relay.Close()

	s := newTestScheduleStore(t)
	s.SetPusher(NewPusher(relay.URL, "tok", nil, nil, nil))
	sched := &Schedule{ID: "sch_chat", Name: "Nightly triage", Kind: "task",
		DeliverChat: &ScheduleChatRef{Source: "wa", ChatID: "c1"}}
	s.mu.Lock()
	s.items[sched.ID] = sched
	s.order = append(s.order, sched.ID)
	s.mu.Unlock()

	at := time.Now()
	s.recordRun(sched.ID, ScheduleRun{At: at, Status: "fired", Detail: "exit 0 in 3s"})

	deadline := time.Now().Add(5 * time.Second)
	for {
		mu.Lock()
		n := len(pushes)
		mu.Unlock()
		s.mu.Lock()
		var detail string
		for _, r := range s.items[sched.ID].Runs {
			if r.At.Equal(at) {
				detail = r.Detail
			}
		}
		s.mu.Unlock()
		if n == 1 && strings.Contains(detail, "chat ✗") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("pushes = %d, run detail = %q; want one schedule-error push and a chat ✗ stamp", n, detail)
		}
		time.Sleep(10 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	if got := pushes[0]["state"]; got != "schedule-error" {
		t.Errorf("push state = %q, want schedule-error", got)
	}
}
