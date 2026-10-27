package main

import (
	"context"
	"errors"
	"math"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"soluterminal-shared/typesafe"
	"soluterminal/jev"
)

type ariaJevSafetyClient struct {
	call func(context.Context, string, any, map[string]typesafe.Question) (*typesafe.Response, error)
}

func (c ariaJevSafetyClient) SystemOneModel(ctx context.Context, model string, state any, questions map[string]typesafe.Question) (*typesafe.Response, error) {
	return c.call(ctx, model, state, questions)
}

func ariaJevSafetyFixture(t *testing.T, tail string, withSecret bool) (*Aria, *Pane) {
	t.Helper()
	p := testPaneWithTail(t, "pane-jev-safety", "test", tail)
	p.AutoMode = "cautious"
	// Codex test identity makes the focused menu independent of a live child
	// process while preserving the production prompt parser.
	p.forceCodexForTest = true
	a := testAriaWithPane(t, p)
	seedJevTestProcess(a, p)
	local, remote := net.Pipe()
	p.agentConn = local
	t.Cleanup(func() { local.Close(); remote.Close() })
	t.Cleanup(p.shutdownGrid)
	a.dataDir = t.TempDir()
	a.sweepStreaks = map[string]*sweepStreak{}
	a.evalLog = newAriaEvalLog()
	if err := os.WriteFile(filepath.Join(a.dataDir, ariaJevAutoMarker), nil, 0600); err != nil {
		t.Fatal(err)
	}
	if withSecret {
		seedRuntimeSecret(t, a.dataDir, jevTypesafeRuntimeID, jevTypesafeSecretEnv, "jev-test-key")
	} else {
		// A build without a secret store reads the environment; keep a key
		// the developer exported out of the no-key case.
		t.Setenv(jevTypesafeSecretEnv, "")
	}
	return a, p
}

func ariaJevSafeResponse() *typesafe.Response {
	safe := .999
	return &typesafe.Response{Model: ariaJevModel, Answers: map[string]typesafe.Answer{
		"safe_action": {Type: "noul", Noul: &safe},
	}, Usage: typesafe.Usage{InputTokens: 120}}
}

func ariaJevRequireActionableFixture(t *testing.T, p *Pane) {
	t.Helper()
	tail := tailOfPane(p, 30)
	if reason := cautiousAutoDenyReason(tail, "1\n"); reason != "" {
		t.Fatalf("fixture declined by deterministic gate: %s", reason)
	}
	if _, _, fp, ok := currentChoicePromptIdentity(p, tail); !ok || fp == 0 {
		t.Fatalf("fixture has no stable prompt identity: ok=%v fp=%d", ok, fp)
	}
}

func TestJevCautiousPrecheckDoesNotSendSensitiveOrNonMenuText(t *testing.T) {
	for _, tc := range []struct{ name, tail string }{
		{"secret path", strings.Replace(claudeEditPermissionScreen, "daemon/state.go", ".env", 1)},
		{"system path", strings.Replace(claudeEditPermissionScreen, "daemon/state.go", "/etc/shadow", 1)},
		{"network endpoint", strings.Replace(claudeEditPermissionScreen, "daemon/state.go", "https://example.invalid/api", 1)},
		{"non-menu", "Would you like me to continue?\nThere is no permission menu here."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, p := ariaJevSafetyFixture(t, tc.tail, true)
			if reason := cautiousAutoDenyReason(tailOfPane(p, 30), "1\n"); reason == "" {
				t.Fatal("adversarial fixture must be rejected by deterministic precheck")
			}
			var calls atomic.Int32
			a.jevClientFactory = func(string) jevSystemOneClient {
				return ariaJevSafetyClient{call: func(context.Context, string, any, map[string]typesafe.Question) (*typesafe.Response, error) {
					calls.Add(1)
					return ariaJevSafeResponse(), nil
				}}
			}
			if handled := a.tryJevCautiousTerminal(context.Background(), p, p.ID, p.Title, tc.tail, time.Now()); handled {
				t.Fatal("unsafe or non-menu observation was handled by Jev")
			}
			if got := calls.Load(); got != 0 {
				t.Fatalf("Jev received unsafe pane text (%d calls)", got)
			}
		})
	}
}

func TestJevCautiousSensitiveValueNeverReachesProvider(t *testing.T) {
	for _, tc := range []struct{ name, prefix string }{
		{"bare github token", "ghp_abcdefghijklmnopqrstuvwxyz123456\n"},
		{"PEM key block", "-----BEGIN PRIVATE KEY-----\nYWJjZGVmZ2hpamtsbW5vcA==\n-----END PRIVATE KEY-----\n"},
		{"opaque value", "abcdefghijklmnopqrstuvwxyz1234567890ABCD\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tail := tc.prefix + claudeEditPermissionScreen
			a, p := ariaJevSafetyFixture(t, tail, true)
			var calls atomic.Int32
			a.jevClientFactory = func(string) jevSystemOneClient {
				return ariaJevSafetyClient{call: func(context.Context, string, any, map[string]typesafe.Question) (*typesafe.Response, error) {
					calls.Add(1)
					return ariaJevSafeResponse(), nil
				}}
			}
			if a.tryJevCautiousTerminal(context.Background(), p, p.ID, p.Title, tail, time.Now()) {
				t.Fatal("sensitive tail was handled by Jev")
			}
			if calls.Load() != 0 {
				t.Fatal("sensitive tail reached Jev provider")
			}
		})
	}
}

func TestJevCautiousRejectsCurrentOptionTwoDespiteStaleOptionOne(t *testing.T) {
	// The old approval is still in scrollback. The latest menu has focus on a
	// persistent grant, so pressing Enter must not be inferred from row 1.
	tail := claudeEditPermissionScreen + "\n\nDo you want to proceed?\n  1. Yes\n❯ 2. Yes, and don't ask again\n  3. No (esc)\n"
	a, p := ariaJevSafetyFixture(t, tail, true)
	if ariaJevFocusedFirstOption(tail) {
		t.Fatal("latest focus was option 2, not option 1")
	}
	var calls atomic.Int32
	a.jevClientFactory = func(string) jevSystemOneClient {
		return ariaJevSafetyClient{call: func(context.Context, string, any, map[string]typesafe.Question) (*typesafe.Response, error) {
			calls.Add(1)
			return ariaJevSafeResponse(), nil
		}}
	}
	if handled := a.tryJevCautiousTerminal(context.Background(), p, p.ID, p.Title, tail, time.Now()); handled {
		t.Fatal("option 2 focused menu must remain with incumbent evaluator")
	}
	if got := calls.Load(); got != 0 {
		t.Fatalf("Jev received ambiguous stale/focused menu (%d calls)", got)
	}
}

func TestJevCautiousDoesNotExportBareCredentialFromScrollback(t *testing.T) {
	// This credential shape has no "secret" or "token" word for the cautious
	// deny list to match, and is shorter than the opaque-string floor.
	tail := "AKIAABCDEFGHIJKLMNOP\n" + claudeEditPermissionScreen
	a, p := ariaJevSafetyFixture(t, tail, true)
	if reason := cautiousAutoDenyReason(tailOfPane(p, 30), "1\n"); reason != "" {
		t.Fatalf("fixture should exercise the redactor guard, not keyword deny: %s", reason)
	}
	if redactTerminalTail(tail) == tail {
		t.Fatal("fixture credential was not detected by terminal redactor")
	}
	var calls atomic.Int32
	a.jevClientFactory = func(string) jevSystemOneClient {
		return ariaJevSafetyClient{call: func(context.Context, string, any, map[string]typesafe.Question) (*typesafe.Response, error) {
			calls.Add(1)
			return ariaJevSafeResponse(), nil
		}}
	}
	if handled := a.tryJevCautiousTerminal(context.Background(), p, p.ID, p.Title, tail, time.Now()); handled {
		t.Fatal("credential-bearing scrollback must fall back before Jev")
	}
	if calls.Load() != 0 {
		t.Fatal("credential-bearing scrollback was sent to Jev")
	}
}

func TestJevCautiousMissingCredentialFallsBackBeforeProvider(t *testing.T) {
	tail := claudeEditPermissionScreen
	a, p := ariaJevSafetyFixture(t, tail, false)
	ariaJevRequireActionableFixture(t, p)
	a.jevClientFactory = func(string) jevSystemOneClient { t.Fatal("provider reached without credential"); return nil }
	if handled := a.tryJevCautiousTerminal(context.Background(), p, p.ID, p.Title, tail, time.Now()); handled {
		t.Fatal("missing credential should leave the incumbent evaluator in charge")
	}
}

func TestJevAutoEnvZeroOverridesMarker(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ariaJevAutoMarker), nil, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(ariaJevAutoEnv, "0")
	if ariaJevAutoEnabled(dir) {
		t.Fatal("explicit zero must disable Jev even when marker exists")
	}
}

func TestJevCautiousProviderErrorFallsBackButStaleMenuStops(t *testing.T) {
	tail := claudeEditPermissionScreen
	for _, tc := range []struct {
		name        string
		call        func(*Pane) (*typesafe.Response, error)
		wantHandled bool
	}{
		{"provider error", func(*Pane) (*typesafe.Response, error) { return nil, errors.New("upstream failure") }, false},
		{"stale menu", func(p *Pane) (*typesafe.Response, error) {
			redraw := "\n" + strings.Replace(tail, "daemon/state.go", "daemon/other.go", 1)
			_, _ = p.buffer.Write([]byte(redraw))
			atomic.AddInt64(&p.TotalStrippedChars, int64(len(stripANSI([]byte(redraw)))))
			return ariaJevSafeResponse(), nil
		}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, p := ariaJevSafetyFixture(t, tail, true)
			ariaJevRequireActionableFixture(t, p)
			a.jevClientFactory = func(string) jevSystemOneClient {
				return ariaJevSafetyClient{call: func(context.Context, string, any, map[string]typesafe.Question) (*typesafe.Response, error) {
					return tc.call(p)
				}}
			}
			if handled := a.tryJevCautiousTerminal(context.Background(), p, p.ID, p.Title, tail, time.Now()); handled != tc.wantHandled {
				t.Fatalf("handled=%v, want %v", handled, tc.wantHandled)
			}
			a.mu.Lock()
			pending := len(a.confirms)
			a.mu.Unlock()
			if pending != 0 {
				t.Fatalf("unexpected Jev confirmation: %d", pending)
			}
			if tc.name == "stale menu" && a.BackoffStreak(p.ID) != 0 {
				t.Fatal("stale menu must not count as a policy decline")
			}
		})
	}
}

func TestJevCautiousUserCancelDoesNotFallBackToAnotherModel(t *testing.T) {
	tail := claudeEditPermissionScreen
	a, p := ariaJevSafetyFixture(t, tail, true)
	ariaJevRequireActionableFixture(t, p)
	a.jevClientFactory = func(string) jevSystemOneClient {
		return ariaJevSafetyClient{call: func(context.Context, string, any, map[string]typesafe.Question) (*typesafe.Response, error) {
			return ariaJevSafeResponse(), nil
		}}
	}
	result := make(chan bool, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	go func() { result <- a.tryJevCautiousTerminal(ctx, p, p.ID, p.Title, tail, time.Now()) }()
	var confirmID string
	deadline := time.After(time.Second)
	for confirmID == "" {
		a.mu.Lock()
		for id := range a.confirms {
			confirmID = id
			break
		}
		a.mu.Unlock()
		if confirmID != "" {
			break
		}
		select {
		case <-deadline:
			t.Fatal("Jev approval did not open a cancellable confirm")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	if !a.signalConfirm(confirmID, false, false) {
		t.Fatal("could not cancel Jev confirmation")
	}
	select {
	case handled := <-result:
		if !handled {
			t.Fatal("cancelled Jev decision fell through to ARIA")
		}
	case <-time.After(time.Second):
		t.Fatal("Jev confirmation did not end after cancel")
	}
	if got := a.recentEvalOutcomes(p.ID); len(got) != 1 || got[0].Kind != "declined" {
		t.Fatalf("outcomes after cancel = %+v", got)
	}
}

func TestJevApprovalRequiresValidHighConfidenceSafetyAnswer(t *testing.T) {
	probability := func(v float64) typesafe.Answer {
		return typesafe.Answer{Type: "noul", Noul: &v}
	}
	for _, tc := range []struct {
		name      string
		answers   map[string]typesafe.Answer
		threshold float64
		wantAllow bool
		wantErr   bool
	}{
		{"clearly safe", map[string]typesafe.Answer{"safe_action": probability(.96)}, .85, true, false},
		{"uncertain", map[string]typesafe.Answer{"safe_action": probability(.849)}, .85, false, false},
		{"unsafe", map[string]typesafe.Answer{"safe_action": probability(.20)}, .85, false, false},
		{"missing answer", map[string]typesafe.Answer{}, .85, false, true},
		{"wrong answer type", map[string]typesafe.Answer{"safe_action": {Type: "choice", Choice: "safe"}}, .85, false, true},
		{"out of range", map[string]typesafe.Answer{"safe_action": probability(1.01)}, .85, false, true},
		{"NaN", map[string]typesafe.Answer{"safe_action": probability(math.NaN())}, .85, false, true},
		{"missing probability", map[string]typesafe.Answer{"safe_action": {Type: "noul"}}, .85, false, true},
		{"invalid threshold", map[string]typesafe.Answer{"safe_action": probability(1)}, 0, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			allow, err := jev.InterpretApproval(&typesafe.Response{Answers: tc.answers}, tc.threshold)
			if allow != tc.wantAllow || (err != nil) != tc.wantErr {
				t.Fatalf("allow=%v err=%v, want allow=%v err=%v", allow, err, tc.wantAllow, tc.wantErr)
			}
		})
	}
	if allow, err := jev.InterpretApproval(nil, .95); allow || err == nil {
		t.Fatalf("nil response allowed=%v err=%v", allow, err)
	}
}

func TestJevApprovalQuestionNeverTreatsTerminalTextAsAuthority(t *testing.T) {
	questions := jev.ApprovalQuestions()
	if len(questions) != 1 {
		t.Fatalf("questions=%d, want one bounded safety question", len(questions))
	}
	for _, key := range []string{"safe_action"} {
		question, ok := questions[key]
		if !ok || question.Type != "noul" || question.Instructions == nil || question.Criteria == nil {
			t.Fatalf("%s question must be a bounded yes/no policy check: %+v", key, question)
		}
	}
}
