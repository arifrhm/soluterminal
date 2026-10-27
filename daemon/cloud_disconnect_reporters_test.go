package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"soluterminal-shared/machineauth"
)

// cloudRecorder is a fake cloud that counts every request by path.
type cloudRecorder struct {
	mu     sync.Mutex
	hits   map[string]int
	bodies map[string][]string
	auth   map[string][]string
	status func(path string) int
}

func newCloudRecorder(t *testing.T) (*cloudRecorder, *httptest.Server) {
	t.Helper()
	rec := &cloudRecorder{hits: map[string]int{}, bodies: map[string][]string{}, auth: map[string][]string{}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		rec.mu.Lock()
		rec.hits[r.URL.Path]++
		rec.bodies[r.URL.Path] = append(rec.bodies[r.URL.Path], string(b))
		rec.auth[r.URL.Path] = append(rec.auth[r.URL.Path], r.Header.Get("Authorization"))
		status := rec.status
		rec.mu.Unlock()
		code := http.StatusNoContent
		if status != nil {
			code = status(r.URL.Path)
		}
		w.WriteHeader(code)
	}))
	t.Cleanup(srv.Close)
	return rec, srv
}

func (c *cloudRecorder) count(path string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.hits[path]
}

func (c *cloudRecorder) total() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, v := range c.hits {
		n += v
	}
	return n
}

// fireEveryReporter makes each cloud-bound reporter try to send once.
func fireEveryReporter(sink *crashSink, updater *Updater, pusher *Pusher, n int) {
	site := "test-site-" + string(rune('a'+n))
	sink.report(site, "error", "boom", "stack "+site)
	updater.reportApplyFailureAsync("failed")
	if base, tok := updater.cloudCreds(); base != "" && tok != "" {
		postFleetStatus(context.Background(), base, tok, updater.fleetUpdateStatus())
	}
	pusher.deliver([]byte(`{"paneId":"p1","state":"awaiting-input"}`), "p1", "awaiting-input")
	pusher.fireClear("p1")
	pusher.SyncReminders("wsl", nil)
}

// Disconnect queues a revoke that holds the revoke key, never the token, and
// points at the claim's own cloud.
func TestDisconnectQueuesRevokeWithoutToken(t *testing.T) {
	dir := t.TempDir()
	const token = "tok_never_on_disk_after"
	if err := SaveClaim(dir, &Claim{RelayURL: "wss://cloud.example/relay", Token: token, MachineID: "mach_q"}); err != nil {
		t.Fatalf("seed claim: %v", err)
	}
	a := &apiServer{dataDir: dir}
	rec := httptest.NewRecorder()
	a.handleCloudDisconnect(rec, httptest.NewRequest(http.MethodPost, "/api/cloud/disconnect", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	raw, err := os.ReadFile(cloudRevokePath(dir))
	if err != nil {
		t.Fatalf("revoke not queued: %v", err)
	}
	if strings.Contains(string(raw), token) {
		t.Fatal("pending revoke file contains the machine token")
	}
	var items []pendingCloudRevoke
	if err := json.Unmarshal(raw, &items); err != nil || len(items) != 1 {
		t.Fatalf("pending revokes = %v (err %v), want one", items, err)
	}
	got := items[0]
	if got.MachineID != "mach_q" || got.CloudBase != "https://cloud.example" || got.RevokeKey != machineauth.RevokeKey(token) {
		t.Fatalf("pending revoke = %+v", got)
	}
	if info, err := os.Stat(cloudRevokePath(dir)); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("pending revoke perms = %v (err %v), want 0600", info.Mode().Perm(), err)
	}
}

// Disconnecting an unpaired computer queues nothing and still succeeds.
func TestDisconnectWithoutClaimQueuesNothing(t *testing.T) {
	dir := t.TempDir()
	a := &apiServer{dataDir: dir}
	rec := httptest.NewRecorder()
	a.handleCloudDisconnect(rec, httptest.NewRequest(http.MethodPost, "/api/cloud/disconnect", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if _, err := os.Stat(cloudRevokePath(dir)); !os.IsNotExist(err) {
		t.Fatalf("revoke queued for an unpaired computer (err=%v)", err)
	}
}

func shrinkRevokeRetry(r *cloudRevoker) *cloudRevoker {
	r.retryMin, r.retryMax = 10*time.Millisecond, 40*time.Millisecond
	return r
}

// While the cloud is unreachable or failing the revoke stays queued and is
// retried; once the cloud confirms it the queue empties. The request carries
// the revoke key and no bearer token.
func TestCloudRevokerRetriesUntilConfirmed(t *testing.T) {
	cloud, srv := newCloudRecorder(t)
	var calls int32
	cloud.status = func(string) int {
		if atomic.AddInt32(&calls, 1) < 3 {
			return http.StatusServiceUnavailable
		}
		return http.StatusNoContent
	}
	dir := t.TempDir()
	r := shrinkRevokeRetry(newCloudRevoker(dir))
	const token = "tok_retry"
	p := cloudRevokeFromClaim(&Claim{RelayURL: "ws" + strings.TrimPrefix(srv.URL, "http") + "/relay", Token: token, MachineID: "mach_r"}, "", time.Now())
	if err := r.enqueue(p); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go r.run(ctx)

	eventuallyTrue(t, 3*time.Second, func() bool { return len(r.queue.list()) == 0 })
	if n := cloud.count(machineauth.RevokePath); n != 3 {
		t.Fatalf("revoke attempts = %d, want 3 (two failures, one success)", n)
	}
	cloud.mu.Lock()
	defer cloud.mu.Unlock()
	for i, body := range cloud.bodies[machineauth.RevokePath] {
		var got map[string]string
		_ = json.Unmarshal([]byte(body), &got)
		if got["machineId"] != "mach_r" || got["revokeKey"] != machineauth.RevokeKey(token) {
			t.Fatalf("attempt %d body = %s", i, body)
		}
		if strings.Contains(body, token) || cloud.auth[machineauth.RevokePath][i] != "" {
			t.Fatalf("attempt %d sent the machine token", i)
		}
	}
}

// A cloud that no longer knows the computer (404) ends the retry; there is
// nothing left to revoke.
func TestCloudRevokerStopsOnNotFound(t *testing.T) {
	cloud, srv := newCloudRecorder(t)
	cloud.status = func(string) int { return http.StatusNotFound }
	r := shrinkRevokeRetry(newCloudRevoker(t.TempDir()))
	_ = r.enqueue(&pendingCloudRevoke{CloudBase: srv.URL, MachineID: "mach_gone", RevokeKey: "ab", QueuedAt: time.Now().UTC().Format(time.RFC3339)})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go r.run(ctx)
	eventuallyTrue(t, 2*time.Second, func() bool { return len(r.queue.list()) == 0 })
	time.Sleep(60 * time.Millisecond)
	if n := cloud.count(machineauth.RevokePath); n != 1 {
		t.Fatalf("revoke attempts = %d, want 1", n)
	}
}

// A revoke the previous run could not deliver is sent after a restart.
func TestCloudRevokerResumesFromDisk(t *testing.T) {
	cloud, srv := newCloudRecorder(t)
	dir := t.TempDir()
	q := &cloudRevokeQueue{dataDir: dir}
	if err := q.add(pendingCloudRevoke{CloudBase: srv.URL, MachineID: "mach_old", RevokeKey: "cd", QueuedAt: time.Now().UTC().Format(time.RFC3339)}); err != nil {
		t.Fatalf("seed queue: %v", err)
	}
	r := shrinkRevokeRetry(newCloudRevoker(dir))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go r.run(ctx)
	eventuallyTrue(t, 2*time.Second, func() bool { return cloud.count(machineauth.RevokePath) == 1 })
	eventuallyTrue(t, 2*time.Second, func() bool {
		_, err := os.Stat(cloudRevokePath(dir))
		return os.IsNotExist(err)
	})
}

// A revoke older than the retry window is dropped without another attempt.
func TestCloudRevokerDropsExpired(t *testing.T) {
	cloud, srv := newCloudRecorder(t)
	r := newCloudRevoker(t.TempDir())
	old := time.Now().Add(-cloudRevokeMaxAge - time.Hour).UTC().Format(time.RFC3339)
	_ = r.queue.add(pendingCloudRevoke{CloudBase: srv.URL, MachineID: "mach_stale", RevokeKey: "ef", QueuedAt: old})
	if !r.flush(context.Background()) {
		t.Fatal("expired revoke still queued")
	}
	if n := cloud.count(machineauth.RevokePath); n != 0 {
		t.Fatalf("expired revoke was sent %d time(s)", n)
	}
}
