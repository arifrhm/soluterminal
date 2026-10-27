package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"soluterminal-shared/typesafe"
)

// nonTeamJevFixture is the cautious fixture on a non-team install, without
// the pilot marker: Jev must run by default there.
func nonTeamJevFixture(t *testing.T, withKey bool) (*Aria, *Pane) {
	t.Helper()
	a, p := ariaJevSafetyFixture(t, claudeEditPermissionScreen, withKey)
	ariaJevRequireActionableFixture(t, p)
	if err := os.Remove(filepath.Join(a.dataDir, ariaJevAutoMarker)); err != nil {
		t.Fatal(err)
	}
	a.teamFeatures = func() bool { return false }
	a.metrics = NewMetrics()
	return a, p
}

func lowConfidenceJev(calls *atomic.Int32) func(string) jevSystemOneClient {
	return func(string) jevSystemOneClient {
		return ariaJevSafetyClient{call: func(context.Context, string, any, map[string]typesafe.Question) (*typesafe.Response, error) {
			calls.Add(1)
			doubt := .2
			return &typesafe.Response{Model: ariaJevModel, Answers: map[string]typesafe.Answer{
				"safe_action": {Type: "noul", Noul: &doubt},
			}, Usage: typesafe.Usage{InputTokens: 120}}, nil
		}}
	}
}

func TestNonTeamCautiousUsesJevWithoutPilotMarker(t *testing.T) {
	a, p := nonTeamJevFixture(t, true)
	var calls atomic.Int32
	a.jevClientFactory = lowConfidenceJev(&calls)
	if !a.cautiousJevOnly() {
		t.Fatal("non-team install should use Jev alone")
	}
	handled, outcome := a.jevCautiousDecide(context.Background(), p, p.ID, p.Title, "", time.Now(), true)
	if handled || outcome != "uncertain_fallback" || calls.Load() != 1 {
		t.Fatalf("handled=%v outcome=%q calls=%d", handled, outcome, calls.Load())
	}
	// The same permission is not sent again.
	handled, outcome = a.jevCautiousDecide(context.Background(), p, p.ID, p.Title, "", time.Now(), true)
	if handled || outcome != "uncertain_fallback" || calls.Load() != 1 {
		t.Fatalf("repeat: handled=%v outcome=%q calls=%d", handled, outcome, calls.Load())
	}
	// The team path still needs the pilot marker.
	if handled, outcome := a.jevCautiousDecide(context.Background(), p, p.ID, p.Title, "", time.Now(), false); handled || outcome != "disabled_fallback" {
		t.Fatalf("team path without marker: handled=%v outcome=%q", handled, outcome)
	}
}

func TestNonTeamCautiousLeavesDoubtForHumanWithReason(t *testing.T) {
	a, p := nonTeamJevFixture(t, true)
	var calls atomic.Int32
	a.jevClientFactory = lowConfidenceJev(&calls)
	a.evaluateObservation(context.Background(), p.ID, p.Title, claudeEditPermissionScreen)
	outcomes := a.recentEvalOutcomes(p.ID)
	if len(outcomes) == 0 {
		t.Fatal("no outcome recorded")
	}
	got := outcomes[0]
	if got.Kind != "declined" || !strings.Contains(got.Reason, "weren't confident") {
		t.Fatalf("outcome=%+v", got)
	}
	if calls.Load() != 1 {
		t.Fatalf("Jev calls=%d, want 1", calls.Load())
	}
}

func TestNonTeamCautiousWithoutAccountExplainsSignIn(t *testing.T) {
	a, p := nonTeamJevFixture(t, false) // no local key, not paired
	a.jevClientFactory = func(string) jevSystemOneClient {
		t.Fatal("local client used without a key")
		return nil
	}
	a.evaluateObservation(context.Background(), p.ID, p.Title, claudeEditPermissionScreen)
	outcomes := a.recentEvalOutcomes(p.ID)
	want := "free account"
	if runtimeSecretStore == nil {
		want = jevTypesafeSecretEnv
	}
	if len(outcomes) == 0 || !strings.Contains(outcomes[0].Reason, want) {
		t.Fatalf("want %q in outcomes=%+v", want, outcomes)
	}
}

// A build without a runtime key store has no Credentials screen, so the reason
// names the environment variable instead of pointing there.
func TestJevCautiousCredentialReasonMatchesKeyStore(t *testing.T) {
	saved := runtimeSecretStore
	t.Cleanup(func() { runtimeSecretStore = saved })
	runtimeSecretStore = nil
	if r := jevCautiousReason("credential_fallback"); !strings.Contains(r, jevTypesafeSecretEnv) || strings.Contains(r, "Credentials") {
		t.Fatalf("without a key store: %q", r)
	}
	runtimeSecretStore = func(string) secretResolver { return envSecrets{} }
	if r := jevCautiousReason("credential_fallback"); !strings.Contains(r, "Credentials") {
		t.Fatalf("with a key store: %q", r)
	}
}

func TestNonTeamCautiousKillSwitch(t *testing.T) {
	a, p := nonTeamJevFixture(t, true)
	t.Setenv(ariaJevAutoEnv, "0")
	a.jevClientFactory = func(string) jevSystemOneClient {
		t.Fatal("Jev called while switched off")
		return nil
	}
	if handled, outcome := a.jevCautiousDecide(context.Background(), p, p.ID, p.Title, "", time.Now(), true); handled || outcome != "disabled_fallback" {
		t.Fatalf("handled=%v outcome=%q", handled, outcome)
	}
}

func TestNonTeamCautiousDenylistNeverAsksJev(t *testing.T) {
	a, p := nonTeamJevFixture(t, true)
	tail := strings.Replace(claudeEditPermissionScreen, "daemon/state.go", ".env", 1)
	p2 := testPaneWithTail(t, "pane-jev-deny", "test", tail)
	p2.AutoMode = "cautious"
	p2.forceCodexForTest = true
	_ = p
	a.jevClientFactory = func(string) jevSystemOneClient {
		t.Fatal("Jev asked about a denylisted request")
		return nil
	}
	if handled, outcome := a.jevCautiousDecide(context.Background(), p2, p2.ID, p2.Title, "", time.Now(), true); handled || outcome != "denylist_fallback" {
		t.Fatalf("handled=%v outcome=%q", handled, outcome)
	}
}

func TestJevCautiousReasonsAreUserFacing(t *testing.T) {
	for _, outcome := range []string{
		"uncertain_fallback", "denylist_fallback", "sensitive_tail_fallback", "precheck_fallback",
		jevCloudOutcomeQuota, jevCloudOutcomeUnavailable, "provider_fallback", "credential_fallback",
		"disabled_fallback", "native_fallback", "something_new",
	} {
		r := jevCautiousReason(outcome)
		// An environment variable's name is meant for the reader; a raw
		// outcome code is not.
		if r == "" || strings.Contains(strings.ReplaceAll(r, jevTypesafeSecretEnv, ""), "_") {
			t.Fatalf("%s: reason %q", outcome, r)
		}
	}
}

func TestSmartApprovalsStatusReportsCloudAllowance(t *testing.T) {
	relay := &jevFakeRelay{t: t, answer: .1, cautious: true}
	srv := relay.start()
	a, p := nonTeamJevFixture(t, false)
	a.setCloudCreds(srv.URL, "machine-tok")
	// The fake relay sends no quota headers; record what a real one sends.
	h := http.Header{}
	h.Set("X-Jev-Remaining", "97")
	h.Set("X-Jev-Limit", "100")
	recordJevCloudQuota(h)
	api := &apiServer{aria: a}
	rec := httptest.NewRecorder()
	api.handleSmartApprovalsStatus(rec, httptest.NewRequest("GET", "/api/smart-approvals", nil))
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out["source"] != jevSourceCloud || out["remaining"] != float64(97) || out["limit"] != float64(100) {
		t.Fatalf("status=%v", out)
	}
	_ = p
}
