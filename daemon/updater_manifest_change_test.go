package main

// The onManifestChange hook is the daemon's "a release was published" signal.
// Release readiness subscribes to it so its anchor moves to the new release on
// the poll that first sees it, instead of serving the previous release's anchor
// until its own cache TTL lapses.

import (
	"context"
	"net/http"
	"sync/atomic"
	"testing"
	"time"
)

func TestCheckFiresOnManifestChangeOnlyOnChange(t *testing.T) {
	disableControlSignature(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", t.TempDir())

	versionA := []byte(`{"version":"0.0.900","source":{"sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},"platforms":{"linux-amd64":{"url":"https://example.test/solud","sha256":"abc"}}}`)
	versionB := []byte(`{"version":"0.0.901","source":{"sha":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"},"platforms":{"linux-amd64":{"url":"https://example.test/solud","sha256":"abc"}}}`)
	versionBRecut := []byte(`{"version":"0.0.901","source":{"sha":"cccccccccccccccccccccccccccccccccccccccc"},"platforms":{"linux-amd64":{"url":"https://example.test/solud","sha256":"abc"}}}`)

	u := NewUpdater("0.0.899", "", "", nil, nil)
	u.homeDir = t.TempDir()
	u.manifestURL = "https://updates.example.test/update.json"
	transport := staticRoundTripper{u.manifestURL: versionA}
	u.httpClient = &http.Client{Transport: transport}

	var fires atomic.Int64
	u.onManifestChange = func() { fires.Add(1) }

	// First successful fetch after boot fires: a daemon that just self-updated
	// must re-anchor promptly rather than inherit the pre-update anchor.
	if _, err := u.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	waitForFires(t, &fires, 1)

	// Same manifest again: the 30s poll must not spam the listener.
	if _, err := u.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	if got := fires.Load(); got != 1 {
		t.Fatalf("fires after an unchanged re-fetch = %d, want 1", got)
	}

	// A new release (version and source sha both move) fires again.
	transport[u.manifestURL] = versionB
	if _, err := u.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	waitForFires(t, &fires, 2)
	if got := fires.Load(); got != 2 {
		t.Fatalf("total fires = %d, want 2", got)
	}

	// A re-cut of the SAME version from a different source commit is a publish
	// too — the anchor has to move to the rebuilt commit, so the sha alone must
	// be enough to fire.
	transport[u.manifestURL] = versionBRecut
	if _, err := u.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	waitForFires(t, &fires, 3)

	// ...and that re-cut is then the new baseline: no fire on re-fetching it.
	if _, err := u.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	if got := fires.Load(); got != 3 {
		t.Fatalf("total fires = %d, want 3", got)
	}
}

// waitForFires polls the async hook counter; the callback runs on its own
// goroutine because Check holds the updater lock until it returns.
func waitForFires(t *testing.T, fires *atomic.Int64, want int64) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if fires.Load() >= want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("onManifestChange fires = %d, want %d", fires.Load(), want)
}
