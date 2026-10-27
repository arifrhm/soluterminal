package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"soluterminal-shared/machineauth"
)

// Retroactive git_repo backfill — the daemon half of the cc-usage self-heal.
// Forward-fill (ccusage_scan.go) stamps git_repo on NEW turns, but rows captured
// before this machine upgraded carry no repo. A remote URL can only be resolved
// on THIS machine (the cwds are its working trees), so once per machine we:
//
//  1. ask the cloud which of our own cwds still have no git_repo,
//  2. resolve each locally via repoResolver (git config),
//  3. POST the (cwd → repo) mapping back; the cloud fills only its empty rows.
//
// Gated by a marker file so it runs once. It is idempotent regardless (cloud
// only returns still-empty cwds, fill-if-empty UPDATE), so a re-run is harmless;
// the marker just avoids needless work. A failed round-trip leaves the marker
// unwritten so the next boot retries; unresolvable cwds (deleted dir / no remote)
// resolve to "" and are simply skipped — they keep the folder-name fallback.

const (
	// ccRepoBackfillMarker is the once-done sentinel under dataDir. The "-v1"
	// lets a future resolution improvement force a re-run by bumping the name.
	ccRepoBackfillMarker = "cc-usage-repo-backfill-v1.done"
	// ccRepoBackfillInitialDelay staggers the backfill a bit further past boot
	// than the first scan, so it never competes with latency-sensitive startup.
	ccRepoBackfillInitialDelay = 30 * time.Second
	// ccRepoBackfillApplyBatch caps cwd→repo pairs per apply POST (cloud caps too).
	ccRepoBackfillApplyBatch = 500
)

// maybeRunRepoBackfill runs the one-time retroactive backfill if its marker is
// absent. Safe to call from a goroutine; recovers its own panics. A no-op on
// empty creds (local-only daemon) or if already done.
func maybeRunRepoBackfill(ctx context.Context, client *http.Client, cloudBase, token, dataDir string, repos *repoResolver) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("cc-usage: repo-backfill panic recovered: %v", r)
			reportPanicToCloud("cc-usage-repo-backfill", r)
		}
	}()
	if cloudBase == "" || token == "" || repos == nil {
		return
	}
	marker := filepath.Join(dataDir, ccRepoBackfillMarker)
	if _, err := os.Stat(marker); err == nil {
		return // already done
	}

	select {
	case <-ctx.Done():
		return
	case <-time.After(ccRepoBackfillInitialDelay):
	}

	cwds, err := repoBackfillFetchCwds(ctx, client, cloudBase, token)
	if err != nil {
		log.Printf("cc-usage: repo-backfill fetch cwds failed (will retry next boot): %v", err)
		return
	}
	if len(cwds) == 0 {
		// Nothing to fill — mark done so we never ask again.
		writeRepoBackfillMarker(marker)
		return
	}

	// Resolve each cwd locally; keep only the ones with a real remote.
	pairs := make([]ccRepoPair, 0, len(cwds))
	for _, cwd := range cwds {
		select {
		case <-ctx.Done():
			return
		default:
		}
		if repo := repos.resolve(ctx, cwd); repo != "" {
			pairs = append(pairs, ccRepoPair{Cwd: cwd, Repo: repo})
		}
	}
	if len(pairs) == 0 {
		// None resolvable (deleted dirs / no remotes). Done — re-running can't help.
		writeRepoBackfillMarker(marker)
		log.Printf("cc-usage: repo-backfill: %d cwds, none resolvable to a remote", len(cwds))
		return
	}

	var updated int64
	for start := 0; start < len(pairs); start += ccRepoBackfillApplyBatch {
		end := start + ccRepoBackfillApplyBatch
		if end > len(pairs) {
			end = len(pairs)
		}
		n, err := repoBackfillApply(ctx, client, cloudBase, token, pairs[start:end])
		if err != nil {
			log.Printf("cc-usage: repo-backfill apply failed (will retry next boot): %v", err)
			return // leave marker unwritten → retry
		}
		updated += n
	}
	writeRepoBackfillMarker(marker)
	log.Printf("cc-usage: repo-backfill done: %d cwds, %d resolved, %d rows filled", len(cwds), len(pairs), updated)
}

func writeRepoBackfillMarker(path string) {
	if err := os.WriteFile(path, []byte(time.Now().UTC().Format(time.RFC3339)+"\n"), 0o600); err != nil {
		log.Printf("cc-usage: repo-backfill: write marker failed: %v", err)
	}
}

type ccRepoPair struct {
	Cwd  string `json:"cwd"`
	Repo string `json:"repo"`
}

// repoBackfillFetchCwds asks the cloud for this machine's cwds that still lack a
// git_repo. Signed exactly like reportCCUsageBatch (machine token + HMAC).
func repoBackfillFetchCwds(ctx context.Context, client *http.Client, cloudBase, token string) ([]string, error) {
	body := []byte("{}")
	resp, err := signedPost(ctx, client, cloudBase+"/api/internal/cc-usage/repo-backfill/cwds", token, body)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("cwds: HTTP %d", resp.StatusCode)
	}
	var out struct {
		Cwds []string `json:"cwds"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("cwds decode: %w", err)
	}
	return out.Cwds, nil
}

// repoBackfillApply posts a (cwd → repo) batch and returns rows filled.
func repoBackfillApply(ctx context.Context, client *http.Client, cloudBase, token string, pairs []ccRepoPair) (int64, error) {
	body, err := json.Marshal(struct {
		Repos []ccRepoPair `json:"repos"`
	}{Repos: pairs})
	if err != nil {
		return 0, err
	}
	resp, err := signedPost(ctx, client, cloudBase+"/api/internal/cc-usage/repo-backfill/apply", token, body)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return 0, fmt.Errorf("apply: HTTP %d", resp.StatusCode)
	}
	var out struct {
		Updated int64 `json:"updated"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return 0, fmt.Errorf("apply decode: %w", err)
	}
	return out.Updated, nil
}

// signedPost builds + sends a machine-HMAC-signed POST, mirroring
// postCCUsageChunk's signing (machineauth.SignRequest over the body).
func signedPost(ctx context.Context, client *http.Client, url, token string, body []byte) (*http.Response, error) {
	if client == nil {
		client = &http.Client{Timeout: ccUsageReportTimeout}
	}
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	machineauth.SignRequest(req, token, body, time.Now())
	return client.Do(req)
}
