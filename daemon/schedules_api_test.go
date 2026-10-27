package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// previewSchedule is stateless (no store), so a bare apiServer suffices.
func TestPreviewScheduleIncludesMachineTimezone(t *testing.T) {
	t.Parallel()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/schedules/preview?cron=0+9+*+*+*&count=3", nil)
	(&apiServer{}).previewSchedule(rec, req)

	if rec.Code != 200 {
		t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	var resp struct {
		Fires           []time.Time `json:"fires"`
		TzName          string      `json:"tzName"`
		TzOffsetMinutes int         `json:"tzOffsetMinutes"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(resp.Fires) != 3 {
		t.Fatalf("fires = %d, want 3", len(resp.Fires))
	}
	wantName, wantOffsetSec := time.Now().Zone()
	if resp.TzName != wantName {
		t.Errorf("tzName = %q, want %q", resp.TzName, wantName)
	}
	if resp.TzOffsetMinutes != wantOffsetSec/60 {
		t.Errorf("tzOffsetMinutes = %d, want %d", resp.TzOffsetMinutes, wantOffsetSec/60)
	}
}

func TestPreviewScheduleRejectsInvalidCron(t *testing.T) {
	t.Parallel()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/schedules/preview?cron=not+a+cron", nil)
	(&apiServer{}).previewSchedule(rec, req)
	if rec.Code != 400 {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

// The reports reader's read marker: monotonic, persisted, 404 on unknown.
func TestMarkReadMonotonicPersistsAndValidates(t *testing.T) {
	t.Parallel()
	work := t.TempDir()
	dir := t.TempDir()
	s, err := NewScheduleStore(dir, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	sched, err := s.Create(ScheduleInput{
		Kind: "task", Cron: "0 * * * *", Prompt: "digest", Cwd: work, Permission: "plan",
	})
	if err != nil {
		t.Fatal(err)
	}

	newer := time.Now().Truncate(time.Second)
	older := newer.Add(-time.Hour)

	if _, err := s.MarkRead(sched.ID, newer); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Get(sched.ID)
	if got.LastReadAt == nil || !got.LastReadAt.Equal(newer) {
		t.Fatalf("LastReadAt = %v, want %v", got.LastReadAt, newer)
	}
	// A stale tab POSTing an old timestamp must NOT un-read newer runs.
	if _, err := s.MarkRead(sched.ID, older); err != nil {
		t.Fatal(err)
	}
	got, _ = s.Get(sched.ID)
	if !got.LastReadAt.Equal(newer) {
		t.Fatalf("stale MarkRead moved the marker back: %v", got.LastReadAt)
	}
	if _, err := s.MarkRead("nope", newer); err == nil {
		t.Fatal("unknown id should error")
	}

	// Survives a reload — unread state must not reset on daemon restart.
	s.Stop()
	s2, err := NewScheduleStore(dir, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Stop()
	got2, ok := s2.Get(sched.ID)
	if !ok || got2.LastReadAt == nil || !got2.LastReadAt.Equal(newer) {
		t.Fatalf("LastReadAt lost across reload: %+v", got2.LastReadAt)
	}

	// Handler: bad body → 400, unknown id → 404, happy path → 200 with DTO.
	a := &apiServer{scheds: s2}
	post := func(id, body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/schedules/"+id+"/read", strings.NewReader(body))
		req.SetPathValue("id", id)
		a.markScheduleRead(rec, req)
		return rec
	}
	if rec := post(sched.ID, `{}`); rec.Code != 400 {
		t.Errorf("empty at: code=%d, want 400", rec.Code)
	}
	if rec := post("nope", `{"at":"2026-07-10T09:00:00Z"}`); rec.Code != 404 {
		t.Errorf("unknown id: code=%d, want 404", rec.Code)
	}
	future := newer.Add(time.Hour).Format(time.RFC3339)
	if rec := post(sched.ID, `{"at":"`+future+`"}`); rec.Code != 200 {
		t.Errorf("happy path: code=%d body=%s", rec.Code, rec.Body.String())
	}
}
