package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

type checkinSink struct {
	mu     sync.Mutex
	bodies []map[string]any
	status int
}

func newCheckinSink(t *testing.T) (*checkinSink, *httptest.Server) {
	t.Helper()
	sink := &checkinSink{status: http.StatusNoContent}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/checkin" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
			t.Errorf("check-in must carry no credentials")
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		sink.mu.Lock()
		sink.bodies = append(sink.bodies, body)
		status := sink.status
		sink.mu.Unlock()
		w.WriteHeader(status)
	}))
	t.Cleanup(srv.Close)
	return sink, srv
}

func (s *checkinSink) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.bodies)
}

func TestCheckinInstallIDIsPersistedUUIDv4(t *testing.T) {
	dir := t.TempDir()
	id, err := loadOrCreateCheckinInstallID(dir)
	if err != nil || !checkinInstallIDPattern.MatchString(id) {
		t.Fatalf("id %q err %v", id, err)
	}
	again, _ := loadOrCreateCheckinInstallID(dir)
	if again != id {
		t.Fatalf("id not stable: %q then %q", id, again)
	}
	info, err := os.Stat(filepath.Join(dir, checkinInstallIDFile))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("id file mode %v err %v", info, err)
	}
	// It is not the updater/browser install-id.
	if durable, err := loadOrCreateInstallID(dir); err != nil || durable == id {
		t.Fatalf("check-in id must be independent of install-id (%q, %v)", durable, err)
	}
	if err := os.WriteFile(filepath.Join(dir, checkinInstallIDFile), []byte("garbage"), 0o600); err != nil {
		t.Fatal(err)
	}
	if fresh, _ := loadOrCreateCheckinInstallID(dir); fresh == id || !checkinInstallIDPattern.MatchString(fresh) {
		t.Fatalf("malformed id must be replaced, got %q", fresh)
	}
}

func TestCheckinSendsOncePerDayWithMinimalPayload(t *testing.T) {
	t.Setenv(checkinDisabledEnv, "")
	sink, srv := newCheckinSink(t)
	dir := t.TempDir()
	signedIn := false
	c := newInstallCheckin(dir, "0.0.1200", func() string { return srv.URL }, func() bool { return signedIn })
	now := time.Date(2026, 10, 21, 9, 0, 0, 0, time.UTC)
	c.now = func() time.Time { return now }

	if sent, err := c.sendOnce(context.Background()); !sent || err != nil {
		t.Fatalf("first send: sent=%v err=%v", sent, err)
	}
	body := sink.bodies[0]
	if len(body) != 6 {
		t.Fatalf("payload must have exactly 6 fields, got %v", body)
	}
	id, _ := loadOrCreateCheckinInstallID(dir)
	if body["installId"] != id || body["version"] != "0.0.1200" || body["os"] != checkinOSFamily(runtime.GOOS) || body["signedIn"] != false {
		t.Fatalf("payload = %v", body)
	}
	if body["agentPaneDay"] != false || body["agentsTogetherDay"] != false {
		t.Fatalf("no activity yet, so both flags are no: %v", body)
	}

	if sent, _ := c.sendOnce(context.Background()); sent {
		t.Fatal("an identical check-in the same day must not resend")
	}
	// The last-sent day survives a restart.
	restarted := newInstallCheckin(dir, "0.0.1200", func() string { return srv.URL }, func() bool { return signedIn })
	restarted.now = c.now
	if sent, _ := restarted.sendOnce(context.Background()); sent {
		t.Fatal("a restart the same day must not resend")
	}
	signedIn = true
	if sent, _ := c.sendOnce(context.Background()); !sent || sink.bodies[1]["signedIn"] != true {
		t.Fatal("signing in must be reported the same day")
	}
	now = now.Add(24 * time.Hour)
	if sent, _ := c.sendOnce(context.Background()); !sent {
		t.Fatal("next day must send")
	}
	if sink.count() != 3 {
		t.Fatalf("sends = %d, want 3", sink.count())
	}
}

func TestCheckinOptOutsAndDevBuilds(t *testing.T) {
	sink, srv := newCheckinSink(t)
	base := func() string { return srv.URL }

	t.Setenv(checkinDisabledEnv, "1")
	c := newInstallCheckin(t.TempDir(), "0.0.1200", base, nil)
	if sent, _ := c.sendOnce(context.Background()); sent {
		t.Fatal("SOLU_DISABLE_CHECKIN=1 must suppress the check-in")
	}
	t.Setenv(checkinDisabledEnv, "")

	dev := newInstallCheckin(t.TempDir(), "dev", base, nil)
	if sent, _ := dev.sendOnce(context.Background()); sent {
		t.Fatal("dev builds never check in")
	}

	dir := t.TempDir()
	off := newInstallCheckin(dir, "0.0.1200", base, nil)
	if err := off.setEnabled(false); err != nil {
		t.Fatal(err)
	}
	reloaded := newInstallCheckin(dir, "0.0.1200", base, nil)
	if sent, _ := reloaded.sendOnce(context.Background()); sent {
		t.Fatal("the persisted opt-out must suppress the check-in")
	}
	info, err := os.Stat(filepath.Join(dir, checkinSettingsFile))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("settings file mode %v err %v", info, err)
	}
	if sink.count() != 0 {
		t.Fatalf("nothing should have been sent, got %d", sink.count())
	}
}

func TestCheckinSettingsEndpointIsLocalOnly(t *testing.T) {
	t.Setenv(checkinDisabledEnv, "")
	dir := t.TempDir()
	a := &apiServer{checkin: newInstallCheckin(dir, "0.0.1200", nil, nil)}

	rec := httptest.NewRecorder()
	a.handleGetCheckinSettings(rec, httptest.NewRequest(http.MethodGet, "/api/settings/checkin", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"enabled":true`) {
		t.Fatalf("default GET = %d %s", rec.Code, rec.Body.String())
	}

	relayed := httptest.NewRequest(http.MethodPut, "/api/settings/checkin", strings.NewReader(`{"enabled":false}`))
	relayed.Header.Set("X-Forwarded-Prefix", "/m/mach_x")
	rec = httptest.NewRecorder()
	a.handlePutCheckinSettings(rec, relayed)
	if rec.Code != http.StatusForbidden || !a.checkin.userEnabled() {
		t.Fatalf("relayed PUT must be refused: %d", rec.Code)
	}

	rec = httptest.NewRecorder()
	a.handlePutCheckinSettings(rec, httptest.NewRequest(http.MethodPut, "/api/settings/checkin", strings.NewReader(`{}`)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("missing enabled: %d", rec.Code)
	}

	rec = httptest.NewRecorder()
	a.handlePutCheckinSettings(rec, httptest.NewRequest(http.MethodPut, "/api/settings/checkin", strings.NewReader(`{"enabled":false}`)))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"enabled":false`) {
		t.Fatalf("local PUT = %d %s", rec.Code, rec.Body.String())
	}
	if newInstallCheckin(dir, "0.0.1200", nil, nil).userEnabled() {
		t.Fatal("opt-out not persisted")
	}

	t.Setenv(checkinDisabledEnv, "1")
	rec = httptest.NewRecorder()
	a.handleGetCheckinSettings(rec, httptest.NewRequest(http.MethodGet, "/api/settings/checkin", nil))
	if !strings.Contains(rec.Body.String(), `"envDisabled":true`) {
		t.Fatalf("env opt-out not reported: %s", rec.Body.String())
	}
}

func TestCheckinSignedInSignal(t *testing.T) {
	t.Setenv("SOLU_TEAM_FEATURES", "")
	a := &apiServer{entitlements: newTeamEntitlements(t.TempDir(), nil)}
	if a.checkinSignedIn() {
		t.Fatal("an unpaired install with no cloud verdict is not signed in")
	}
	a.cloudToken = "machine-token"
	if !a.checkinSignedIn() {
		t.Fatal("a paired install is signed in")
	}
	a.cloudToken = ""
	a.entitlements.store(entitlementRecord{Tier: tierAccount, Source: "account", CheckedAt: time.Now()})
	if !a.checkinSignedIn() {
		t.Fatal("a cached account tier is signed in")
	}
}

func TestCheckinOSFamily(t *testing.T) {
	for goos, want := range map[string]string{"windows": "windows", "linux": "linux", "darwin": "darwin", "freebsd": "other", "android": "other"} {
		if got := checkinOSFamily(goos); got != want {
			t.Errorf("%s -> %s, want %s", goos, got, want)
		}
	}
}
