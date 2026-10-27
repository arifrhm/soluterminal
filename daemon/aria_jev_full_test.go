package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"soluterminal-shared/typesafe"
)

func newJevFullHarness(t *testing.T, tail string) (*Aria, *Pane, <-chan directAutoFrameRead) {
	t.Helper()
	t.Setenv(ariaJevFullEnv, "")
	a, p, frames := newJevDetectHarness(t, "always")
	if err := os.Remove(filepath.Join(a.dataDir, ariaJevDetectMarker)); err != nil {
		t.Fatal(err)
	}
	appendDirectAutoMenuRedraw(t, p, tail)
	seedGridMenu(t, p, tail)
	a.metrics = NewMetrics()
	a.inFlightEvals = map[string]bool{}
	return a, p, frames
}

func TestJevFullDefaultApprovesKnownAndNovelPermissions(t *testing.T) {
	for _, tc := range []struct{ name, tail string }{
		{"claude command", strings.Replace(claudeBashPermissionScreen, "fix/auto-approve-deny-plan-question-billing", "fix/approval", 1)},
		{"claude edit", claudeEditPermissionScreen},
		{"claude MCP", claudeMcpPermissionScreen},
		{"claude fetch", claudeWebFetchPermissionScreen},
		{"codex command", codexCommandApprovalScreen},
		{"codex vowel-initial test identifier", strings.Replace(codexCommandApprovalScreen, "go test -C daemon ./...", "go test -run '^TestVerificationPlaneLiveChildOwnsHealthLiveness$' -count=40", 1)},
		{"codex patch", codexPatchApprovalScreen},
		{"codex network", codexNetworkApprovalScreen},
		{"codex MCP", codexMcpPermissionObservedPrompt},
		{"novel read", jevNovelToolMenu},
		{"novel install", strings.Replace(jevNovelToolMenu, "read_file\n  src/app.go", "shell\n  npm install", 1)},
		{"novel destructive", strings.Replace(jevNovelToolMenu, "read_file\n  src/app.go", "shell\n  rm -rf build", 1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, p, frames := newJevFullHarness(t, tc.tail)
			calls := 0
			a.jevClientFactory = func(string) jevSystemOneClient {
				return ariaJevSafetyClient{call: func(_ context.Context, _ string, _ any, questions map[string]typesafe.Question) (*typesafe.Response, error) {
					calls++
					if _, ok := questions["permission_menu"]; !ok || len(questions) != 1 {
						t.Fatal("full mode must not ask a risk question")
					}
					return jevDetectYesResponse(false), nil
				}}
			}
			a.evaluateObservation(context.Background(), p.ID, p.Title, "")
			expectFrame(t, frames, "full-mode approval", "\r")
			// A locally recognized permission fires without waiting for the
			// provider; Jev decides only what the local classifier misses.
			known := isAutoApprovalPromptText(tc.tail, p.HasCodexDescendant() || p.HasMuseDescendant())
			wantCalls, wantModel, wantMetric := 1, ariaJevModel, `solud_aria_jev_full_total{cohort="novel",outcome="fired"} 1`
			if known {
				wantCalls, wantModel, wantMetric = 0, "direct", `solud_aria_jev_full_total{cohort="known",outcome="known_local"} 1`
			}
			if calls != wantCalls || a.metrics.AriaEvalFiredTotal.Load() != 1 {
				t.Fatalf("known=%v calls=%d fires=%d", known, calls, a.metrics.AriaEvalFiredTotal.Load())
			}
			outcomes := a.recentEvalOutcomes(p.ID)
			if len(outcomes) == 0 || outcomes[0].Model != wantModel || outcomes[0].Kind != "fired" {
				t.Fatalf("known=%v attribution: %+v", known, outcomes)
			}
			var metrics strings.Builder
			a.metrics.WriteText(&metrics, 1)
			if !strings.Contains(metrics.String(), wantMetric) {
				t.Fatalf("missing metric %s", wantMetric)
			}
		})
	}
}

func TestJevFullFallbackKeepsRecognizedPermissionsMoving(t *testing.T) {
	for _, outcome := range []string{"provider failure", "low confidence", "malformed", "missing credentials", "rollback"} {
		t.Run(outcome, func(t *testing.T) {
			a, p, frames := newJevFullHarness(t, codexCommandApprovalScreen)
			calls := 0
			if outcome == "rollback" {
				t.Setenv(ariaJevFullEnv, "0")
			}
			if outcome == "missing credentials" {
				a.dataDir = t.TempDir()
				t.Setenv(jevTypesafeSecretEnv, "")
			}
			a.jevClientFactory = func(string) jevSystemOneClient {
				return ariaJevSafetyClient{call: func(context.Context, string, any, map[string]typesafe.Question) (*typesafe.Response, error) {
					calls++
					if outcome == "provider failure" {
						return nil, errors.New("unavailable")
					}
					response := jevDetectYesResponse(false)
					if outcome == "low confidence" {
						low := .1
						response.Answers["permission_menu"] = typesafe.Answer{Type: "noul", Noul: &low}
					}
					if outcome == "malformed" {
						response.Answers = nil
					}
					return response, nil
				}}
			}
			a.evaluateObservation(context.Background(), p.ID, p.Title, "")
			expectFrame(t, frames, "recognized fallback", "\r")
			// Recognized permissions never reach the provider, so no provider
			// outcome can slow or block them.
			if calls != 0 {
				t.Fatalf("calls=%d want=0", calls)
			}
			if got := a.recentEvalOutcomes(p.ID)[0].Model; got != "direct" {
				t.Fatalf("fallback model=%s", got)
			}
		})
	}
}

func TestJevFullRejectsNonPermissionsAndSensitivePayloads(t *testing.T) {
	for _, tail := range []string{
		strings.Replace(jevNovelToolMenu, "Grant this action?", "Ready to code?", 1),
		strings.Replace(jevNovelToolMenu, "Grant this action?", "Use your reset?", 1),
		jevNovelToolMenu + "\nEnter to select",
		codexCommandApprovalScreen + "\nEnter to select",
		strings.Replace(jevNovelToolMenu, "1. Yes", "1. Yes, and don't ask again", 1),
		strings.Replace(jevNovelToolMenu, "src/app.go", "sk-proj-"+strings.Repeat("a", 80), 1),
		"Tool: read_file\nGrant this action?\n❯ 1. Yes\n  2. No (esc)",
	} {
		a, p, frames := newJevFullHarness(t, tail)
		a.jevClientFactory = func(string) jevSystemOneClient { t.Fatalf("excluded text reached Jev: %q", tail); return nil }
		a.evaluateObservation(context.Background(), p.ID, p.Title, "")
		expectNoFrame(t, frames, "excluded menu")
	}
}

func TestJevFullChangedPromptNeverFallsBackOrWrites(t *testing.T) {
	a, p, frames := newJevFullHarness(t, jevNovelToolMenu)
	a.jevClientFactory = func(string) jevSystemOneClient {
		return ariaJevSafetyClient{call: func(context.Context, string, any, map[string]typesafe.Question) (*typesafe.Response, error) {
			changed := strings.Replace(jevNovelToolMenu, "read_file\n  src/app.go", "shell\n  git push origin main", 1)
			appendDirectAutoMenuRedraw(t, p, changed)
			seedGridMenu(t, p, changed)
			return jevDetectYesResponse(false), nil
		}}
	}
	a.evaluateObservation(context.Background(), p.ID, p.Title, "")
	expectNoFrame(t, frames, "changed action")
}

func TestJevFullCancelledEvaluationNeverFallsBackOrWrites(t *testing.T) {
	a, p, frames := newJevFullHarness(t, jevNovelToolMenu)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a.jevClientFactory = func(string) jevSystemOneClient {
		return ariaJevSafetyClient{call: func(context.Context, string, any, map[string]typesafe.Question) (*typesafe.Response, error) {
			cancel()
			return nil, context.Canceled
		}}
	}
	a.evaluateObservation(ctx, p.ID, p.Title, "")
	expectNoFrame(t, frames, "cancelled evaluation")
}

func TestJevFullPrivateKnownActionsStayLocal(t *testing.T) {
	for _, action := range []string{
		"sha256sum /tmp/capture.png # " + strings.Repeat("a19f", 16),
		"printf '%s' 'sk-proj-" + strings.Repeat("x", 80) + "' > /tmp/fixture",
	} {
		t.Run(action[:10], func(t *testing.T) {
			tail := strings.Replace(codexCommandApprovalScreen, "go test -C daemon ./...", action, 1)
			a, p, frames := newJevFullHarness(t, tail)
			evidence := ariaJevMenuEvidence(ariaJevPermissionTail(p, true), true)
			if redactTerminalTail(evidence) == evidence && !opaqueSecretLike(evidence) {
				t.Fatal("fixture must exercise provider privacy guard")
			}
			a.jevClientFactory = func(string) jevSystemOneClient { t.Fatal("private action reached provider"); return nil }
			a.evaluateObservation(context.Background(), p.ID, p.Title, "")
			expectFrame(t, frames, "private recognized permission handled locally", "\r")
			if got := a.recentEvalOutcomes(p.ID)[0]; got.Model != "direct" || got.Kind != "fired" {
				t.Fatalf("incorrect attribution: %s/%s", got.Model, got.Kind)
			}
		})
	}
}

func TestJevPrivateLocalFallbackPreservesQuestionAndCancellationGuards(t *testing.T) {
	private := strings.Replace(codexCommandApprovalScreen, "go test -C daemon ./...", "echo sk-proj-"+strings.Repeat("x", 80), 1)
	for _, mode := range []string{"question", "cancelled"} {
		t.Run(mode, func(t *testing.T) {
			tail := private
			if mode == "question" {
				tail += "\nEnter to select"
			}
			a, p, frames := newJevFullHarness(t, tail)
			a.jevClientFactory = func(string) jevSystemOneClient { t.Fatal("private text reached provider"); return nil }
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if mode == "cancelled" {
				cancel()
			}
			a.evaluateObservation(ctx, p.ID, p.Title, "")
			expectNoFrame(t, frames, "private action does not bypass guards")
		})
	}
}
