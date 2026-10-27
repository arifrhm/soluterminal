package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"soluterminal-shared/machineauth"
)

// Disconnect removes this computer from its account in the cloud, not just
// locally. The token is deleted at once; what stays on disk is a revoke key
// (machineauth.RevokeKey) that can only revoke this machine, so the revoke can
// be retried while the cloud is unreachable without keeping a credential that
// still works for anything else.

const (
	cloudRevokeFile = "cloud_revoke_pending.json"
	// cloudRevokeMaxAge drops a revoke the cloud never confirmed. By then a
	// rotated token or a manual "Remove computer" has long made it moot.
	cloudRevokeMaxAge  = 30 * 24 * time.Hour
	cloudRevokeTimeout = 10 * time.Second
	// Retry backoff for an unconfirmed revoke.
	cloudRevokeRetryMin = 30 * time.Second
	cloudRevokeRetryMax = time.Hour
)

type pendingCloudRevoke struct {
	CloudBase string `json:"cloudBase"`
	MachineID string `json:"machineId"`
	RevokeKey string `json:"revokeKey"`
	QueuedAt  string `json:"queuedAt"`
}

func cloudRevokePath(dataDir string) string {
	return filepath.Join(dataDir, cloudRevokeFile)
}

// cloudRevokeFromClaim builds the revoke record for a claim, or nil when the
// claim can't identify a machine in a cloud. fallbackRelay covers claims
// written without a relay URL.
func cloudRevokeFromClaim(c *Claim, fallbackRelay string, now time.Time) *pendingCloudRevoke {
	if c == nil || strings.TrimSpace(c.Token) == "" || strings.TrimSpace(c.MachineID) == "" {
		return nil
	}
	relay := strings.TrimSpace(c.RelayURL)
	if relay == "" {
		relay = strings.TrimSpace(fallbackRelay)
	}
	base, err := relayToHTTPBase(relay)
	if err != nil {
		return nil
	}
	return &pendingCloudRevoke{
		CloudBase: base,
		MachineID: c.MachineID,
		RevokeKey: machineauth.RevokeKey(c.Token),
		QueuedAt:  now.UTC().Format(time.RFC3339),
	}
}

// cloudRevokeQueue is the on-disk list of revokes the cloud hasn't confirmed.
// Its mutex serialises the read-modify-write between the Disconnect handler
// and the background revoker.
type cloudRevokeQueue struct {
	mu      sync.Mutex
	dataDir string
}

func (q *cloudRevokeQueue) loadLocked() []pendingCloudRevoke {
	b, err := os.ReadFile(cloudRevokePath(q.dataDir))
	if err != nil {
		return nil
	}
	var items []pendingCloudRevoke
	if json.Unmarshal(b, &items) != nil {
		return nil
	}
	return items
}

func (q *cloudRevokeQueue) saveLocked(items []pendingCloudRevoke) error {
	path := cloudRevokePath(q.dataDir)
	if len(items) == 0 {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	b, err := json.MarshalIndent(items, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(q.dataDir, 0o700); err != nil {
		return err
	}
	return writeFileAtomicSynced(path, b, 0o600)
}

// add records a revoke, replacing any earlier one for the same machine.
func (q *cloudRevokeQueue) add(p pendingCloudRevoke) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	items := q.loadLocked()
	kept := items[:0]
	for _, it := range items {
		if it.MachineID != p.MachineID || it.CloudBase != p.CloudBase {
			kept = append(kept, it)
		}
	}
	return q.saveLocked(append(kept, p))
}

func (q *cloudRevokeQueue) list() []pendingCloudRevoke {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.loadLocked()
}

// remove drops the given revoke (matched on machine, cloud and key, so a
// newer record queued meanwhile for the same machine survives).
func (q *cloudRevokeQueue) remove(p pendingCloudRevoke) {
	q.mu.Lock()
	defer q.mu.Unlock()
	items := q.loadLocked()
	kept := items[:0]
	for _, it := range items {
		if it.MachineID == p.MachineID && it.CloudBase == p.CloudBase && it.RevokeKey == p.RevokeKey {
			continue
		}
		kept = append(kept, it)
	}
	if err := q.saveLocked(kept); err != nil {
		slog.Warn("cloud revoke: could not update pending list", "err", err)
	}
}

// cloudRevoker sends queued revokes, retrying with backoff until the cloud
// confirms or the record expires. It resumes from disk after a restart.
type cloudRevoker struct {
	queue  *cloudRevokeQueue
	client *http.Client
	kick   chan struct{}
	// retryMin and retryMax bound the backoff; tests shrink them.
	retryMin, retryMax time.Duration
}

func newCloudRevoker(dataDir string) *cloudRevoker {
	return &cloudRevoker{
		queue:  &cloudRevokeQueue{dataDir: dataDir},
		client: &http.Client{Timeout: cloudRevokeTimeout},
		kick:   make(chan struct{}, 1),

		retryMin: cloudRevokeRetryMin,
		retryMax: cloudRevokeRetryMax,
	}
}

// enqueue persists a revoke and wakes the sender. A nil record is a no-op.
func (r *cloudRevoker) enqueue(p *pendingCloudRevoke) error {
	if r == nil || p == nil {
		return nil
	}
	if err := r.queue.add(*p); err != nil {
		return err
	}
	select {
	case r.kick <- struct{}{}:
	default:
	}
	return nil
}

// queueCloudRevoke persists a revoke and wakes the revoker. Without a running
// revoker (tests) the record is still written, for the next run to send.
func (a *apiServer) queueCloudRevoke(p *pendingCloudRevoke) error {
	if p == nil {
		return nil
	}
	if a.cloudRevoker != nil {
		return a.cloudRevoker.enqueue(p)
	}
	return (&cloudRevokeQueue{dataDir: a.dataDir}).add(*p)
}

func (r *cloudRevoker) run(ctx context.Context) {
	delay := r.retryMin
	for {
		if r.flush(ctx) {
			delay = r.retryMin
		} else {
			delay = min(delay*2, r.retryMax)
		}
		var wait <-chan time.Time
		if len(r.queue.list()) > 0 {
			wait = time.After(delay)
		}
		select {
		case <-ctx.Done():
			return
		case <-r.kick:
			delay = r.retryMin
		case <-wait:
		}
	}
}

// flush tries every pending revoke once. It reports whether the queue is now
// empty.
func (r *cloudRevoker) flush(ctx context.Context) bool {
	now := time.Now()
	for _, p := range r.queue.list() {
		if queued, err := time.Parse(time.RFC3339, p.QueuedAt); err == nil && now.Sub(queued) > cloudRevokeMaxAge {
			slog.Warn("cloud revoke: giving up after 30 days", "machine", p.MachineID)
			r.queue.remove(p)
			continue
		}
		if err := r.send(ctx, p); err != nil {
			slog.Info("cloud revoke: will retry", "machine", p.MachineID, "err", err)
			continue
		}
		r.queue.remove(p)
	}
	return len(r.queue.list()) == 0
}

var errCloudRevokeRetry = errors.New("cloud did not confirm the revoke")

// send posts one revoke. nil means done: the cloud removed the machine, or
// answered that it no longer exists (404) or will never accept this request
// (other 4xx). A network error, 429 or 5xx is retried.
func (r *cloudRevoker) send(ctx context.Context, p pendingCloudRevoke) error {
	body, _ := json.Marshal(map[string]string{"machineId": p.MachineID, "revokeKey": p.RevokeKey})
	ctx, cancel := context.WithTimeout(ctx, cloudRevokeTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(p.CloudBase, "/")+machineauth.RevokePath, bytes.NewReader(body))
	if err != nil {
		return nil // malformed record: retrying can't help
	}
	req.Header.Set("Content-Type", machineauth.ContentTypeJSON)
	resp, err := r.client.Do(req)
	if err != nil {
		return err
	}
	_ = resp.Body.Close()
	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		slog.Info("cloud revoke: computer removed from its account", "machine", p.MachineID)
		return nil
	case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500:
		return errCloudRevokeRetry
	default:
		slog.Info("cloud revoke: cloud no longer knows this computer", "machine", p.MachineID, "http", resp.StatusCode)
		return nil
	}
}
