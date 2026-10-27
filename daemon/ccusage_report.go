package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"soluterminal-shared/machineauth"
	"soluterminal-shared/wire"
)

// Batch reporter for Claude Code usage rows.
//
// The transcript scanner (a separate, later-owned file) extracts one
// ccUsageRecord per billed assistant turn, dedups a pass in-memory by
// (message_id, request_id) keep-max, then hands batches here. We POST them to
// the cloud's ingest endpoint, HMAC-signed exactly like update_report.go.
//
// The cloud is the dedup authority (keep-max upsert on
// (machine_id, message_id, request_id)), so re-sending is harmless: the
// scanner should advance its file cursor ONLY after a 2xx ack. Any non-2xx
// response is returned as an error so the caller leaves the cursor where it
// was and re-reads + re-sends next pass.

// ccUsageRecordsPerRequest caps how many records ride in a single POST.
// Postgres' shared pooler is small (MaxConns=2) and a multi-row upsert is one
// statement, so we keep batches modest. The plan specifies ≤500.
const ccUsageRecordsPerRequest = 500

// ccUsageReportTimeout is the default per-request timeout when the caller
// passes a nil *http.Client.
const ccUsageReportTimeout = 30 * time.Second

// ccUsageRecord aliases the single wire contract in soluterminal-shared/wire.
type ccUsageRecord = wire.CCUsageRecord

// ccUsageBatch aliases the single wire contract in soluterminal-shared/wire.
type ccUsageBatch = wire.CCUsageBatch

// reportCCUsageBatch POSTs recs to <cloudBase>/api/internal/cc-usage in
// chunks of at most ccUsageRecordsPerRequest, each HMAC-signed with the
// machine token (same scheme as update_report.go). It returns nil only if
// EVERY chunk got a 2xx; any non-2xx (or transport/build error) is returned
// so the caller does not advance its cursor.
//
// If client is nil a default client with ccUsageReportTimeout is used. An
// empty cloudBase, empty token, or empty recs is a no-op returning nil.
func reportCCUsageBatch(ctx context.Context, client *http.Client, cloudBase, token string, recs []ccUsageRecord) error {
	if cloudBase == "" || token == "" || len(recs) == 0 {
		// Nothing to do — local-only daemon or empty pass. Not an error;
		// the caller may still advance its cursor on an empty pass.
		return nil
	}
	if client == nil {
		client = &http.Client{Timeout: ccUsageReportTimeout}
	}

	url := cloudBase + "/api/internal/cc-usage"
	for start := 0; start < len(recs); start += ccUsageRecordsPerRequest {
		end := start + ccUsageRecordsPerRequest
		if end > len(recs) {
			end = len(recs)
		}
		if err := postCCUsageChunk(ctx, client, url, token, recs[start:end]); err != nil {
			return err
		}
	}
	return nil
}

// postCCUsageChunk sends one ≤500-record chunk and turns any non-2xx into an
// error.
func postCCUsageChunk(ctx context.Context, client *http.Client, url, token string, chunk []ccUsageRecord) error {
	body, err := json.Marshal(ccUsageBatch{Records: chunk})
	if err != nil {
		return fmt.Errorf("cc-usage marshal: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("cc-usage build request: %w", err)
	}
	// SignRequest sets Authorization: Bearer <token>, Content-Type, and the
	// HMAC timestamp + signature headers over "<ts>.<body>".
	machineauth.SignRequest(req, token, body, time.Now())

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("cc-usage POST: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("cc-usage rejected: HTTP %d", resp.StatusCode)
	}
	return nil
}
