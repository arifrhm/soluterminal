package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"soluterminal-shared/typesafe"
)

const cautiousAcceptanceMenu = "Bash command\n  git status --short\nDo you want to proceed?\n❯ 1. Yes\n  2. No (esc)"

// Exercise the public cautious default, with a synthetic provider and actual
// watcher evaluator, daemon countdown and framed terminal transport. No real
// command is executed; the peer records the bytes an agent would receive.
func cautiousAcceptanceFixture(t *testing.T) (*Aria, *Pane, <-chan directAutoFrameRead, *atomic.Int32) {
	t.Helper()
	a, p, frames := newJevFullHarness(t, cautiousAcceptanceMenu)
	p.SetAutoMode("cautious")
	t.Setenv(ariaJevAutoEnv, "")
	t.Setenv(ariaJevDetectEnv, "0")
	a.teamFeatures = func() bool { return false }
	calls := &atomic.Int32{}
	a.jevClientFactory = func(string) jevSystemOneClient {
		return ariaJevSafetyClient{call: func(context.Context, string, any, map[string]typesafe.Question) (*typesafe.Response, error) {
			calls.Add(1)
			return ariaJevSafeResponse(), nil
		}}
	}
	ariaJevRequireActionableFixture(t, p)
	return a, p, frames, calls
}

func startCautiousAcceptance(t *testing.T, a *Aria, p *Pane) (ariaAutoPendingConfirm, <-chan struct{}) {
	t.Helper()
	events := a.uiBus.subscribe()
	defer a.uiBus.unsubscribe(events)
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	done := make(chan struct{})
	go func() {
		defer close(done)
		a.evaluateObservation(ctx, p.ID, p.Title, cautiousAcceptanceMenu)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Error("cautious evaluator did not stop")
		}
	})
	deadline := time.After(2 * time.Second)
	for {
		select {
		case e := <-events:
			if e.Type != "aria-auto-needs-confirm" {
				continue
			}
			var confirm ariaAutoPendingConfirm
			if err := json.Unmarshal([]byte(e.Text), &confirm); err != nil {
				t.Fatal(err)
			}
			if confirm.ID == "" || confirm.AutoCountdownMs != ariaAutoCountdownMs {
				t.Fatalf("unexpected confirmation: %+v", confirm)
			}
			return confirm, done
		case <-done:
			t.Fatalf("cautious evaluator ended without confirmation: %+v", a.recentEvalOutcomes(p.ID))
		case <-deadline:
			t.Fatal("cautious evaluator did not open confirmation")
		}
	}
}

func finishCautiousAcceptance(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(8 * time.Second):
		t.Fatal("cautious evaluator did not finish")
	}
}

func TestCautiousAcceptanceDaemonCountdownSubmitsOnce(t *testing.T) {
	a, p, frames, calls := cautiousAcceptanceFixture(t)
	started := time.Now()
	_, done := startCautiousAcceptance(t, a, p)
	expectNoFrame(t, frames, "during cancel window")
	finishCautiousAcceptance(t, done)
	if time.Since(started) < time.Duration(ariaAutoCountdownMs)*time.Millisecond {
		t.Fatal("approval fired before the cancel window elapsed")
	}
	expectFrame(t, frames, "safe cautious permission", "\r")
	expectNoFrame(t, frames, "duplicate submission")
	if calls.Load() != 1 || a.metrics.AriaEvalFiredTotal.Load() != 1 {
		t.Fatalf("provider calls=%d fires=%d", calls.Load(), a.metrics.AriaEvalFiredTotal.Load())
	}
}

func TestCautiousAcceptanceCancelSurvivesReevaluation(t *testing.T) {
	a, p, frames, calls := cautiousAcceptanceFixture(t)
	confirm, done := startCautiousAcceptance(t, a, p)
	server := &apiServer{mgr: a.mgr, aria: a}
	response := httptest.NewRecorder()
	server.handleAriaConfirm(response, httptest.NewRequest("POST", "/api/aria/confirm", strings.NewReader(`{"id":"`+confirm.ID+`","approve":false}`)))
	if response.Code != http.StatusNoContent {
		t.Fatalf("cancel HTTP status=%d: %s", response.Code, response.Body.String())
	}
	finishCautiousAcceptance(t, done)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	a.evaluateObservation(ctx, p.ID, p.Title, cautiousAcceptanceMenu)
	expectNoFrame(t, frames, "cancelled prompt on watcher retry")
	if calls.Load() != 1 {
		t.Fatalf("cancelled prompt was evaluated again: calls=%d", calls.Load())
	}
	// The optional team path must not fall through to its ARIA evaluator
	// after this same user cancellation either.
	t.Setenv(ariaJevAutoEnv, "1")
	if !a.tryJevCautiousTerminal(context.Background(), p, p.ID, p.Title, cautiousAcceptanceMenu, time.Now()) {
		t.Fatal("cancelled permission fell through to the team evaluator")
	}
}

func TestCautiousAcceptancePolicyChangeRearmsDecision(t *testing.T) {
	a, p, _, _ := cautiousAcceptanceFixture(t)
	_, pos, hash, ok := currentChoicePromptIdentity(p, cautiousAcceptanceMenu)
	if !ok {
		t.Fatal("fixture has no permission identity")
	}
	a.rememberJevCautious(p.ID, pos, hash, "cancelled")
	server := &apiServer{mgr: a.mgr, aria: a}
	for _, mode := range []string{"", "cautious"} {
		req := httptest.NewRequest("POST", "/api/panes/"+p.ID+"/auto-mode", strings.NewReader(`{"autoMode":"`+mode+`"}`))
		req.SetPathValue("id", p.ID)
		response := httptest.NewRecorder()
		server.setPaneAutoMode(response, req)
		if response.Code != http.StatusOK {
			t.Fatalf("policy update: %d %s", response.Code, response.Body.String())
		}
	}
	if _, seen := a.jevCautiousSeen(p.ID, pos, hash); seen {
		t.Fatal("explicit policy change retained the cancelled decision")
	}
}

func TestCautiousAcceptanceChangesDuringCountdownDoNotWrite(t *testing.T) {
	for _, change := range []string{"mode off", "mode off and on", "typing", "new prompt"} {
		t.Run(change, func(t *testing.T) {
			a, p, frames, _ := cautiousAcceptanceFixture(t)
			confirm, done := startCautiousAcceptance(t, a, p)
			switch change {
			case "mode off":
				server := &apiServer{mgr: a.mgr, aria: a}
				req := httptest.NewRequest("POST", "/api/panes/"+p.ID+"/auto-mode", strings.NewReader(`{"autoMode":""}`))
				req.SetPathValue("id", p.ID)
				response := httptest.NewRecorder()
				server.setPaneAutoMode(response, req)
				if response.Code != http.StatusOK {
					t.Fatalf("mode-off HTTP status=%d: %s", response.Code, response.Body.String())
				}
			case "mode off and on":
				p.SetAutoMode("")
				p.SetAutoMode("cautious")
			case "typing":
				atomic.StoreInt64(&p.LastInputMs, time.Now().UnixMilli())
			case "new prompt":
				next := strings.Replace(cautiousAcceptanceMenu, "git status --short", "git diff --stat", 1)
				appendDirectAutoMenuRedraw(t, p, next)
				seedGridMenu(t, p, next)
			}
			if !a.signalConfirm(confirm.ID, true, false) {
				t.Fatal("could not finish confirmation")
			}
			finishCautiousAcceptance(t, done)
			expectNoFrame(t, frames, change)
			if a.metrics.AriaEvalFiredTotal.Load() != 0 {
				t.Fatal("suppressed submission counted as a fire")
			}
		})
	}
}

func TestCautiousAcceptanceTypingSkipsProvider(t *testing.T) {
	a, p, frames, calls := cautiousAcceptanceFixture(t)
	atomic.StoreInt64(&p.LastInputMs, time.Now().UnixMilli())
	a.evaluateObservation(context.Background(), p.ID, p.Title, cautiousAcceptanceMenu)
	expectNoFrame(t, frames, "user typing before evaluation")
	if calls.Load() != 0 || a.BackoffStreak(p.ID) != 0 {
		t.Fatal("typing spent a provider call or parked the prompt")
	}
}

func TestCautiousAcceptanceCloudRefusalStaysWithHuman(t *testing.T) {
	for _, status := range []int{http.StatusTooManyRequests, http.StatusServiceUnavailable} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			a, p, frames, _ := cautiousAcceptanceFixture(t)
			a.dataDir = t.TempDir() // no local credential; use the real HTTP relay client
			t.Setenv(jevTypesafeSecretEnv, "")
			relay := &jevFakeRelay{t: t, status: status, retry: "60", errorCode: "smart_approvals_unavailable"}
			a.setCloudCreds(relay.start().URL, "machine-tok")
			for range 2 {
				a.evaluateObservation(context.Background(), p.ID, p.Title, cautiousAcceptanceMenu)
			}
			expectNoFrame(t, frames, "refused cloud permission")
			if relay.calls.Load() != 1 {
				t.Fatalf("relay backoff calls=%d, want one", relay.calls.Load())
			}
			want := "unavailable"
			if status == http.StatusTooManyRequests {
				want = "used up"
			}
			outcomes := a.recentEvalOutcomes(p.ID)
			if len(outcomes) == 0 || outcomes[0].Kind != "declined" || !strings.Contains(outcomes[0].Reason, want) {
				t.Fatalf("missing refusal reason: %+v", outcomes)
			}
		})
	}
}
