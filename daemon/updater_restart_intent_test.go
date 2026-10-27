package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestRestartIntentRoundTripAndClear(t *testing.T) {
	now := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	self := filepath.Join(t.TempDir(), "solud")
	want := restartIntent{
		Reason: restartIntentHumanForced, From: "1.0.0", To: "2.0.0",
		DrainDurationMs: 42_000, Inflight: 2,
		InvocationIDs: []string{"inv_a", "inv_b"}, WrittenAtMs: now.UnixMilli(),
	}
	if err := writeRestartIntent(self, want); err != nil {
		t.Fatal(err)
	}
	got := readAndClearRestartIntent(self, now.Add(time.Minute))
	if got == nil || !reflect.DeepEqual(*got, want) {
		t.Fatalf("round trip = %+v, want %+v", got, want)
	}
	if _, err := os.Stat(restartIntentPath(self)); !os.IsNotExist(err) {
		t.Fatalf("intent was not consumed: %v", err)
	}
	if again := readAndClearRestartIntent(self, now); again != nil {
		t.Fatalf("intent consumed twice: %+v", again)
	}
	clearRestartIntent(self)
}

func TestFleetSafeBoundaryRestartIntentRoundTrip(t *testing.T) {
	now := time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC)
	self := filepath.Join(t.TempDir(), "solud")
	want := restartIntent{
		Reason: restartIntentFleetSafeBoundary, From: "1.0.0", To: "2.0.0",
		DrainDurationMs: 19_000, WrittenAtMs: now.UnixMilli(),
	}
	if err := writeRestartIntent(self, want); err != nil {
		t.Fatal(err)
	}
	if got := readAndClearRestartIntent(self, now.Add(time.Minute)); got == nil || !reflect.DeepEqual(*got, want) {
		t.Fatalf("fleet safe-boundary round trip = %+v, want %+v", got, want)
	}
}

func TestRestartIntentRejectsMalformedAndStale(t *testing.T) {
	now := time.Now()
	for _, tc := range []struct {
		name string
		body []byte
	}{
		{name: "malformed", body: []byte("{")},
		{name: "stale", body: []byte(`{"reason":"auto_quiet","writtenAtMs":1}`)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			self := filepath.Join(t.TempDir(), "solud")
			if err := os.WriteFile(restartIntentPath(self), tc.body, 0o600); err != nil {
				t.Fatal(err)
			}
			if got := readAndClearRestartIntent(self, now); got != nil {
				t.Fatalf("invalid intent accepted: %+v", got)
			}
			if _, err := os.Stat(restartIntentPath(self)); !os.IsNotExist(err) {
				t.Fatalf("invalid intent not deleted: %v", err)
			}
		})
	}
}

func TestRestartIntentRejectsEmptyBinaryPathWithoutCWDResidue(t *testing.T) {
	t.Chdir(t.TempDir())
	now := time.Now()
	intent := restartIntent{Reason: restartIntentAutoQuiet, WrittenAtMs: now.UnixMilli()}
	for _, self := range []string{"", " \t\n"} {
		if err := writeRestartIntent(self, intent); err == nil {
			t.Fatalf("writeRestartIntent accepted an empty binary path %q", self)
		}
		if got := readAndClearRestartIntent(self, now); got != nil {
			t.Fatalf("readAndClearRestartIntent(%q) = %+v, want nil", self, got)
		}
		clearRestartIntent(self)
	}
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("empty binary path left cwd residue: %v", entries)
	}
}

func TestRestartIntentMissingReadIsIdempotent(t *testing.T) {
	self := filepath.Join(t.TempDir(), "missing-solud")
	for i := 0; i < 2; i++ {
		if got := readAndClearRestartIntent(self, time.Now()); got != nil {
			t.Fatalf("missing read %d = %+v, want nil", i+1, got)
		}
	}
	clearRestartIntent(self)
}

func TestRestartIntentConcurrentWritesRemainAtomic(t *testing.T) {
	now := time.Now()
	self := filepath.Join(t.TempDir(), "solud")
	const writers = 24
	errCh := make(chan error, writers)
	var wg sync.WaitGroup
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errCh <- writeRestartIntent(self, restartIntent{
				Reason:      restartIntentAutoQuiet,
				From:        "1.0.0",
				To:          fmt.Sprintf("2.0.%d", i),
				WrittenAtMs: now.UnixMilli(),
			})
		}(i)
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			t.Fatalf("concurrent write failed: %v", err)
		}
	}
	if _, err := os.Stat(restartIntentPath(self) + ".tmp"); !os.IsNotExist(err) {
		t.Fatalf("concurrent writes left temporary residue: %v", err)
	}
	got := readAndClearRestartIntent(self, now)
	if got == nil || got.Reason != restartIntentAutoQuiet || got.From != "1.0.0" || !strings.HasPrefix(got.To, "2.0.") {
		t.Fatalf("concurrent writes left an invalid record: %+v", got)
	}
}

func TestApplyWritesAttributedRestartIntentBeforeSystemdExit(t *testing.T) {
	t.Setenv("INVOCATION_ID", "test-systemd-unit")
	for _, tc := range []struct {
		name     string
		force    bool
		required bool
		inflight int
		want     string
	}{
		{name: "automatic quiet", want: restartIntentAutoQuiet},
		{name: "human forced", force: true, inflight: 3, want: restartIntentHumanForced},
		{name: "critical policy", required: true, want: restartIntentCriticalPolicy},
	} {
		t.Run(tc.name, func(t *testing.T) {
			payload := []byte("replacement binary")
			u, binaryURL := leaseTestUpdater(t, payload)
			u.httpClient = &http.Client{Transport: staticRoundTripper{binaryURL: payload}}
			u.inflightCount = func() int { return tc.inflight }
			u.inflightIDs = func() []string {
				if tc.inflight == 0 {
					return nil
				}
				return []string{"inv_force_a", "inv_force_b", "inv_force_c"}
			}
			u.mu.Lock()
			u.status.Required = tc.required
			u.mu.Unlock()
			if err := u.Apply(context.Background(), func(int) {}, tc.force); err != nil {
				t.Fatalf("Apply: %v", err)
			}
			self, _ := u.executable()
			intent := readAndClearRestartIntent(self, time.Now())
			if intent == nil || intent.Reason != tc.want || intent.From != "1.0.0" || intent.To != "2.0.0" || intent.Inflight != tc.inflight {
				t.Fatalf("intent = %+v, want reason=%s inflight=%d", intent, tc.want, tc.inflight)
			}
			if tc.inflight > 0 && len(intent.InvocationIDs) != tc.inflight {
				t.Fatalf("intent ids = %v", intent.InvocationIDs)
			}
		})
	}
}

func TestApplySuccessorSpawnFailureClearsIntentAndRollsBackBinary(t *testing.T) {
	t.Setenv("INVOCATION_ID", "")
	payload := []byte("not an executable successor")
	u, binaryURL := leaseTestUpdater(t, payload)
	u.httpClient = &http.Client{Transport: staticRoundTripper{binaryURL: payload}}
	u.inflightCount = func() int { return 0 }
	self, err := u.executable()
	if err != nil {
		t.Fatal(err)
	}
	wantOriginal, err := os.ReadFile(self)
	if err != nil {
		t.Fatal(err)
	}

	err = u.Apply(context.Background(), func(int) {}, false)
	if err == nil || !strings.Contains(err.Error(), "spawn successor:") {
		t.Fatalf("Apply = %v, want successor spawn failure", err)
	}
	if _, statErr := os.Stat(restartIntentPath(self)); !os.IsNotExist(statErr) {
		t.Fatalf("successor rollback retained restart intent: %v", statErr)
	}
	got, readErr := os.ReadFile(self)
	if readErr != nil || !reflect.DeepEqual(got, wantOriginal) {
		t.Fatalf("rollback binary = %q err=%v, want original %q", got, readErr, wantOriginal)
	}
}
