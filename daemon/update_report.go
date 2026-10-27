package main

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"time"

	"soluterminal-shared/machineauth"
)

// reportUpdateOutcome posts a "I just successfully booted as the
// successor of an auto-update from X to Y" report to the cloud. Fired
// once on startup, fire-and-forget — failure to deliver is logged but
// doesn't fail the daemon. Cloud aggregates per-machine, per-version
// outcomes so we can spot a release that's bricking installs.
//
// We POST instead of riding the relay because the relay tunnel may
// not be up yet when this fires (5s into boot, before relay's first
// successful dial). HTTP gives us an immediate result.
//
// The report is signed with the same HMAC scheme as /api/internal/notify
// (see pusher.go) — bearer token authenticates the machine, and an
// HMAC-SHA256 over "<timestamp>.<body>" using the token as the key
// adds tamper-evidence + replay protection.
func reportUpdateOutcome(ctx context.Context, cloudBase, token, fromVersion, toVersion, outcome string) {
	if cloudBase == "" || token == "" {
		// No relay configured — local-only daemon, nothing to report to.
		// Log so the operator at least sees the outcome.
		slog.Info("update outcome (no cloud, skipping report)", "from", fromVersion, "to", toVersion, "outcome", outcome)
		return
	}
	body, _ := json.Marshal(map[string]string{
		"fromVersion": fromVersion,
		"toVersion":   toVersion,
		"outcome":     outcome,
	})

	url := cloudBase + "/api/internal/update-report"
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		slog.Error("update-report build request failed", "err", err)
		return
	}
	machineauth.SignRequest(req, token, body, time.Now())

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		slog.Error("update-report POST failed", "url", url, "err", err, "from", fromVersion, "to", toVersion)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		slog.Error("update-report rejected", "url", url, "http", resp.StatusCode, "from", fromVersion, "to", toVersion)
		return
	}
	slog.Info("update-report delivered", "from", fromVersion, "to", toVersion, "http", resp.StatusCode)
}

// startUpdateOutcomeReporter is called once during daemon startup. If
// our env says we're a freshly-spawned auto-update successor, fire a
// one-shot report after a short stabilization delay so the cloud sees
// a per-machine record of update outcomes. The delay is purely a "we
// made it past the gauntlet" signal — by the time it fires, the port
// is bound and the mux is serving, so a successful POST from here
// genuinely means the new binary boots cleanly.
func startUpdateOutcomeReporter(ctx context.Context, cloudBase, token, currentVersion string) {
	if os.Getenv("SOLU_SUCCESSOR_OF_UPDATE") != "1" {
		return
	}
	from := os.Getenv("SOLU_PREDECESSOR_VERSION")
	go func() {
		select {
		case <-time.After(5 * time.Second):
		case <-ctx.Done():
			return
		}
		reportUpdateOutcome(ctx, cloudBase, token, from, currentVersion, "ok")
	}()
}
