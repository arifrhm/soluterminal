package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math/rand"
	"net/http"
	"time"

	"soluterminal-shared/machineauth"
)

// startTokenRotationPoller runs a goroutine that polls the cloud's
// token-rotation status endpoint every 60s. When the cloud signals a
// pending rotation, the goroutine:
//
//  1. Calls onRotated(newToken) so the rest of the daemon can swap its
//     in-memory copies (Pusher, Aria, api.cloudToken, relay reconnect).
//  2. Persists the new token to ~/.solu/claim.json (atomic write).
//  3. POSTs /api/internal/token-rotation/confirm with the NEW token as
//     bearer so the cloud knows the rotation succeeded.
//
// Returns immediately. The goroutine exits when ctx is cancelled.
//
// Mode A semantics: the goroutine maintains its own view of the active
// token (starts at currentToken; updates after a confirmed rotation).
// All HMAC sigs are computed against that current token, so the GET
// after a rotation is signed with the new token, matching the cloud's
// expectations.
func startTokenRotationPoller(ctx context.Context, cloudBase, currentToken string, onRotated func(newToken string)) {
	cfg := tokenRotationCfg{
		cloudBase:    cloudBase,
		token:        currentToken,
		onRotated:    onRotated,
		client:       &http.Client{Timeout: 15 * time.Second},
		dataDir:      defaultDataDir(),
		baseInterval: 60 * time.Second,
	}
	go runTokenRotationPoller(ctx, cfg)
}

// tokenRotationCfg carries the test-tweakable knobs for the poller.
// Production code uses startTokenRotationPoller which fills in
// defaults; tests construct one of these directly to use a httptest
// server URL, a fast tick, and a tempdir.
type tokenRotationCfg struct {
	cloudBase    string
	token        string
	onRotated    func(newToken string)
	client       *http.Client
	dataDir      string
	baseInterval time.Duration // tests use ~10ms; production uses 60s
}

// runTokenRotationPoller is the loop body, factored out so tests can
// drive it with a custom http.Client (httptest server URL) and a
// short tick interval.
func runTokenRotationPoller(ctx context.Context, cfg tokenRotationCfg) {
	const maxBackoff = 5 * time.Minute
	baseInterval := cfg.baseInterval
	if baseInterval <= 0 {
		baseInterval = 60 * time.Second
	}
	backoff := 5 * time.Second
	token := cfg.token
	cloudBase := cfg.cloudBase
	client := cfg.client
	dataDir := cfg.dataDir
	onRotated := cfg.onRotated

	// Recover from any unexpected panic so a single rotation bug can't
	// take down a goroutine the daemon relies on for credential health.
	defer func() {
		if r := recover(); r != nil {
			slog.Error("token rotation poller crashed", "panic", r)
			reportPanicToCloud("token-rotation-poller", r)
		}
	}()

	// Wait one interval before the first poll so a freshly-claimed
	// daemon isn't hammering the rotation endpoint at startup.
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(baseInterval):
		}

		newTok, err := pollRotationOnce(ctx, client, cloudBase, token)
		if err != nil {
			// 5xx or network: back off, but don't kill the goroutine.
			slog.Warn("token rotation poll failed", "err", err, "backoff", backoff)
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
			}
			backoff = nextBackoff(backoff, maxBackoff)
			continue
		}
		// Success — reset backoff for the next 5xx burst.
		backoff = 5 * time.Second
		if newTok == "" {
			// pending:false — nothing to do.
			continue
		}

		// Mode A swap: in-memory first (so any in-flight cloud calls the
		// daemon kicks off after this point use the new token), then
		// disk, then confirm.
		oldTok := token
		if onRotated != nil {
			onRotated(newTok)
		}
		if err := UpdateClaimToken(dataDir, newTok); err != nil {
			// Disk write failure is bad but not fatal — the in-memory
			// swap already happened. Log and let the next iteration
			// either re-confirm or surface another rotation.
			slog.Error("token rotation: persist claim.json failed", "err", err)
		}

		if err := postRotationConfirm(ctx, client, cloudBase, newTok); err != nil {
			// Per spec: do not swap our view of the token in this
			// failure case. The cloud will eventually force-switch on
			// grace expiry, after which the daemon re-claims. Surface
			// loudly so the operator can intervene.
			slog.Error("token rotation: confirm failed (cloud may force-switch on grace expiry)",
				"err", err,
				"from_prefix", safePrefix(oldTok),
				"to_prefix", safePrefix(newTok),
			)
			// Don't update our local `token` — keep signing future polls
			// with the old token until either confirm succeeds on a
			// later iteration or the cloud rolls us off.
			continue
		}

		token = newTok
		slog.Info("token rotated",
			"from_prefix", safePrefix(oldTok),
			"to_prefix", safePrefix(newTok),
		)
	}
}

// pollRotationOnce hits GET /api/internal/token-rotation/status. Returns
// ("", nil) when pending:false. Returns (newToken, nil) when the cloud
// has a rotation pending. Returns ("", err) on transport/5xx errors.
//
// 4xx responses are returned as errors but don't trigger backoff
// escalation in the caller — they typically mean the token is already
// invalid (cloud kicked us off), and there's nothing this poller can
// do about that.
func pollRotationOnce(ctx context.Context, client *http.Client, cloudBase, token string) (string, error) {
	url := cloudBase + "/api/internal/token-rotation/status"
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return "", err
	}
	signRequest(req, token, nil)

	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 500 {
		return "", fmt.Errorf("status %d: %s", resp.StatusCode, bytes.TrimSpace(body))
	}
	if resp.StatusCode >= 400 {
		// Auth failure or similar — surface but don't escalate backoff.
		return "", fmt.Errorf("status %d: %s", resp.StatusCode, bytes.TrimSpace(body))
	}

	var r struct {
		Pending   bool   `json:"pending"`
		NewToken  string `json:"newToken"`
		ExpiresAt int64  `json:"expiresAt"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return "", fmt.Errorf("parse rotation status: %w", err)
	}
	if !r.Pending {
		return "", nil
	}
	if r.NewToken == "" {
		return "", fmt.Errorf("cloud signaled pending=true but newToken is empty")
	}
	return r.NewToken, nil
}

// postRotationConfirm tells the cloud the daemon has accepted the new
// token. Bearer is the NEW token (the cloud uses the confirm to verify
// the daemon actually received it).
func postRotationConfirm(ctx context.Context, client *http.Client, cloudBase, newToken string) error {
	url := cloudBase + "/api/internal/token-rotation/confirm"
	body := []byte("{}")
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	signRequest(req, newToken, body)

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	// Spec: expect 204. Accept any 2xx as success in case the cloud
	// chooses to return 200 with a body in the future.
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("confirm: status %d: %s", resp.StatusCode, bytes.TrimSpace(respBody))
	}
	return nil
}

// signRequest applies the same Bearer + X-Solu-Timestamp + X-Solu-Sig
// headers used by Pusher.fire (pusher.go). HMAC-SHA256 over
// "<timestamp>.<body>" using the supplied token as the key. body may
// be nil for GET; the empty byte slice is signed in that case so the
// cloud can verify with the same code path.
func signRequest(req *http.Request, token string, body []byte) {
	machineauth.SignRequest(req, token, body, time.Now())
}

// nextBackoff doubles the current backoff with ±20% jitter, capped at
// max. Jitter prevents many daemons synchronizing their retries against
// a recovering cloud.
func nextBackoff(cur, max time.Duration) time.Duration {
	next := cur * 2
	if next > max {
		next = max
	}
	// ±20% jitter.
	jitter := time.Duration(float64(next) * (0.8 + 0.4*rand.Float64()))
	if jitter > max {
		jitter = max
	}
	if jitter < time.Second {
		jitter = time.Second
	}
	return jitter
}

// safePrefix returns the first 6 chars of a token for logging without
// leaking the secret. Tokens are 64 hex chars; first 6 is plenty to
// distinguish before/after in operator logs.
func safePrefix(tok string) string {
	if len(tok) < 6 {
		return tok
	}
	return tok[:6]
}
