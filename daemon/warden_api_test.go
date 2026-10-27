package main

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestWardenSettingsNormalizeClamps(t *testing.T) {
	t.Parallel()
	// Zero values fill with defaults.
	got := normalizeWardenSettings(wardenSettings{})
	if got.TriageAfterMin != wardenDefaultTriageAfterMin || got.MaxRunsPerHour != wardenDefaultMaxRunsPerHour {
		t.Fatalf("defaults not applied: %+v", got)
	}
	// Out-of-range clamps.
	got = normalizeWardenSettings(wardenSettings{TriageAfterMin: 9999, MaxRunsPerHour: 9999})
	if got.TriageAfterMin != wardenMaxTriageAfterMin || got.MaxRunsPerHour != wardenMaxRunsPerHourCeiling {
		t.Fatalf("upper clamp failed: %+v", got)
	}
	got = normalizeWardenSettings(wardenSettings{TriageAfterMin: 1, MaxRunsPerHour: -5})
	if got.TriageAfterMin != wardenMinTriageAfterMin || got.MaxRunsPerHour != 1 {
		t.Fatalf("lower clamp failed: %+v", got)
	}
}

func TestWardenSettingsStorePersists(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	st, err := newWardenSettingsStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if st.Enabled() {
		t.Fatal("default must be disabled")
	}
	if _, err := st.Set(wardenSettings{Enabled: true, TriageAfterMin: 15, MaxRunsPerHour: 4}); err != nil {
		t.Fatal(err)
	}
	// Reload from disk.
	st2, err := newWardenSettingsStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !st2.Enabled() || st2.Snapshot().TriageAfterMin != 15 {
		t.Fatalf("persisted settings not reloaded: %+v", st2.Snapshot())
	}
}

func TestWardenAutomaticFlagsDefaultOffForFreshAndLegacy(t *testing.T) {
	t.Setenv("SOLU_WARDEN", "1")
	dir := t.TempDir()
	st, err := newWardenSettingsStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !st.Enabled() || st.Snapshot().AutoFlags {
		t.Fatalf("fresh env settings = %+v", st.Snapshot())
	}
	if err := os.WriteFile(filepath.Join(dir, "warden_settings.json"), []byte(`{"enabled":true,"model":"haiku","triageAfterMin":17}`), 0600); err != nil {
		t.Fatal(err)
	}
	st, err = newWardenSettingsStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !st.Enabled() || st.Snapshot().AutoFlags || st.Snapshot().Model != "haiku" {
		t.Fatalf("legacy settings = %+v", st.Snapshot())
	}
}

func TestWardenSettingsHTTPPatchPreservesFieldsAndFailure(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	st, err := newWardenSettingsStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	_, err = st.Set(wardenSettings{Enabled: true, AutoFlags: true, Model: "sonnet", TriageAfterMin: 25, MaxRunsPerHour: 9})
	if err != nil {
		t.Fatal(err)
	}
	a := &apiServer{wardenSettings: st}
	put := func(body string) int {
		rec := httptest.NewRecorder()
		a.handleWardenSettings(rec, httptest.NewRequest("PUT", "/api/warden/settings", strings.NewReader(body)))
		return rec.Code
	}
	for _, step := range []struct {
		body string
		want wardenSettings
	}{
		{`{"autoFlags":false}`, wardenSettings{Enabled: true, AutoFlags: false, Model: "sonnet", TriageAfterMin: 25, MaxRunsPerHour: 9}},
		{`{"model":""}`, wardenSettings{Enabled: true, AutoFlags: false, Model: "", TriageAfterMin: 25, MaxRunsPerHour: 9}},
		{`{"enabled":false}`, wardenSettings{Enabled: false, AutoFlags: false, Model: "", TriageAfterMin: 25, MaxRunsPerHour: 9}},
		{`{"triageAfterMin":30}`, wardenSettings{Enabled: false, AutoFlags: false, Model: "", TriageAfterMin: 30, MaxRunsPerHour: 9}},
		{`{"maxRunsPerHour":11}`, wardenSettings{Enabled: false, AutoFlags: false, Model: "", TriageAfterMin: 30, MaxRunsPerHour: 11}},
		{`{}`, wardenSettings{Enabled: false, AutoFlags: false, Model: "", TriageAfterMin: 30, MaxRunsPerHour: 11}},
	} {
		if got := put(step.body); got != 200 {
			t.Fatalf("PUT %s = %d", step.body, got)
		}
		if got := st.Snapshot(); got != step.want {
			t.Fatalf("after PUT %s settings = %+v, want %+v", step.body, got, step.want)
		}
	}
	var wg sync.WaitGroup
	for _, body := range []string{`{"enabled":true}`, `{"autoFlags":true}`, `{"model":"haiku"}`} {
		wg.Add(1)
		go func(body string) {
			defer wg.Done()
			if got := put(body); got != 200 {
				t.Errorf("concurrent PUT %s = %d", body, got)
			}
		}(body)
	}
	wg.Wait()
	if got := st.Snapshot(); !got.Enabled || !got.AutoFlags || got.Model != "haiku" || got.TriageAfterMin != 30 || got.MaxRunsPerHour != 11 {
		t.Fatalf("concurrent patches lost fields: %+v", got)
	}
	before := st.Snapshot()
	if got := put(`{"autoFlags":"yes"}`); got != 400 {
		t.Fatalf("invalid type = %d", got)
	}
	st.path = filepath.Join(dir, "missing", "settings.json")
	if got := put(`{"autoFlags":false}`); got != 500 {
		t.Fatalf("failed save = %d", got)
	}
	if got := st.Snapshot(); got != before {
		t.Fatalf("failed save mutated memory: %+v", got)
	}
	reloaded, err := newWardenSettingsStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := reloaded.Snapshot(); got != before {
		t.Fatalf("failed save mutated disk: %+v", got)
	}
}

func TestWardenSettingsAPIGetPut(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	st, _ := newWardenSettingsStore(dir)
	a := &apiServer{wardenSettings: st}

	// GET default.
	rec := httptest.NewRecorder()
	a.handleWardenSettings(rec, httptest.NewRequest("GET", "/api/warden/settings", nil))
	if rec.Code != 200 {
		t.Fatalf("GET = %d", rec.Code)
	}
	var got wardenSettings
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got.Enabled {
		t.Fatal("default GET should be disabled")
	}

	// PUT enable.
	rec = httptest.NewRecorder()
	req := httptest.NewRequest("PUT", "/api/warden/settings", strings.NewReader(`{"enabled":true,"triageAfterMin":20}`))
	a.handleWardenSettings(rec, req)
	if rec.Code != 200 {
		t.Fatalf("PUT = %d (%s)", rec.Code, rec.Body.String())
	}
	if !st.Enabled() || st.Snapshot().TriageAfterMin != 20 {
		t.Fatalf("PUT did not persist: %+v", st.Snapshot())
	}

	// PUT bad json → 400.
	rec = httptest.NewRecorder()
	a.handleWardenSettings(rec, httptest.NewRequest("PUT", "/api/warden/settings", strings.NewReader("{")))
	if rec.Code != 400 {
		t.Fatalf("bad json PUT = %d, want 400", rec.Code)
	}
}

func TestWardenStatusAndRunNow(t *testing.T) {
	t.Parallel()
	w, st := testWarden(t, false) // disabled — RunNow must still work (manual tool)
	fired := make(chan struct{}, 1)
	w.exec = func(ctx context.Context, bin string, args []string, cwd, prompt string) execResult {
		if !strings.Contains(prompt, "FLEET BRIEF") {
			t.Errorf("run-now prompt was not a fleet brief: %q", prompt)
		}
		fired <- struct{}{}
		return execResult{output: []byte("NEEDS A DECISION: none.\n")}
	}
	a := &apiServer{wardenSettings: st, warden: w}

	// run-now starts a run.
	rec := httptest.NewRecorder()
	a.handleWardenRunNow(rec, httptest.NewRequest("POST", "/api/warden/run-now", nil))
	var body map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if started, _ := body["started"].(bool); !started {
		t.Fatalf("run-now did not start: %s", rec.Body.String())
	}
	select {
	case <-fired:
	case <-time.After(2 * time.Second):
		t.Fatal("run-now brief never executed")
	}

	// Give runEvent a moment to record the run, then assert status lists it.
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) && len(w.RecentRuns()) == 0 {
		time.Sleep(10 * time.Millisecond)
	}
	rec = httptest.NewRecorder()
	a.handleWardenStatus(rec, httptest.NewRequest("GET", "/api/warden/status", nil))
	var status wardenStatusDTO
	_ = json.Unmarshal(rec.Body.Bytes(), &status)
	if status.Enabled {
		t.Fatal("status.enabled should be false")
	}
	if len(status.Runs) != 1 || status.Runs[0].Kind != wardenBrief {
		t.Fatalf("status.runs = %+v, want one brief run", status.Runs)
	}
}

func TestWardenRunLogPathTraversalRefused(t *testing.T) {
	t.Parallel()
	w, st := testWarden(t, false)
	a := &apiServer{wardenSettings: st, warden: w}
	for _, bad := range []string{"..", "../secret", "a/b"} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("GET", "/api/warden/runs/x", nil)
		req.SetPathValue("name", bad)
		a.handleWardenRunLog(rec, req)
		if rec.Code == 200 {
			t.Fatalf("path %q was served (code 200) — traversal not blocked", bad)
		}
	}
}

func TestWardenOutputPreviewScrubsSecrets(t *testing.T) {
	t.Parallel()
	got := wardenOutputPreview([]byte("done. token gho_ABCDEFGHIJKLMNOPQRSTUVWXYZ0123 leaked"))
	if strings.Contains(got, "gho_ABCDEF") {
		t.Fatalf("preview leaked a token: %q", got)
	}
}
