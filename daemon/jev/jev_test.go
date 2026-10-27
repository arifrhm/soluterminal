package jev

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"soluterminal-shared/typesafe"
)

func testClassifier(t *testing.T, answers map[string]any, usage map[string]any) *Classifier {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/systemone" {
			t.Errorf("path = %q", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"model":   "jev-1.13.0",
			"answers": answers,
			"usage":   usage,
		})
	}))
	t.Cleanup(srv.Close)
	client := typesafe.New("k")
	client.BaseURL = srv.URL
	client.HTTPClient = srv.Client()
	return New(client, Config{Enabled: true, Model: "jev-latest", MinTierConfidence: 0.45, FileThreshold: 0.5})
}

const survey = "Top-level entries: cmd/, docs/, shared/, vendor, …(+3 more)\n\nREADME (head):\nhello"

func TestClassifyTierHappyPath(t *testing.T) {
	clf := testClassifier(t, map[string]any{
		"tier":   map[string]any{"type": "choice", "choice": "standard", "probabilities": map[string]float64{"small": 0.1, "standard": 0.7, "complex": 0.2}, "confidence": 0.62},
		"risk":   map[string]any{"type": "noul", "noul": 0.18},
		"path_0": map[string]any{"type": "noul", "noul": 0.81},
		"path_1": map[string]any{"type": "noul", "noul": 0.22},
		"path_2": map[string]any{"type": "noul", "noul": 0.64},
		"path_3": map[string]any{"type": "noul", "noul": 0.05},
	}, map[string]any{"input_tokens": int64(2000), "output_tokens": int64(30)})

	v, meta, err := clf.ClassifyTier(context.Background(), Input{Prompt: "do a thing", Deliverable: "change", Survey: survey})
	if err != nil {
		t.Fatalf("ClassifyTier: %v", err)
	}
	if v.Tier != "standard" {
		t.Errorf("tier = %q", v.Tier)
	}
	if v.RiskBehaviorChange {
		t.Error("risk = true, want false")
	}
	if len(v.FilesLikelyTouched) != 2 || v.FilesLikelyTouched[0] != "cmd" || v.FilesLikelyTouched[1] != "shared" {
		t.Errorf("files = %#v, want [cmd shared]", v.FilesLikelyTouched)
	}
	if v.Reasoning == "" {
		t.Error("reasoning is empty")
	}
	if meta.TierConfidence != 0.62 {
		t.Errorf("confidence = %v", meta.TierConfidence)
	}
	if meta.RunnerUp != "complex" || meta.RunnerUpProbability != 0.2 {
		t.Errorf("runner-up = %q %v", meta.RunnerUp, meta.RunnerUpProbability)
	}
	if meta.Usage.InputTokens != 2000 || meta.EstimatedCostUSD <= 0 {
		t.Errorf("usage/cost = %+v %v", meta.Usage, meta.EstimatedCostUSD)
	}
	if meta.QuestionCount != 6 {
		t.Errorf("question count = %d, want 6", meta.QuestionCount)
	}
}

func TestLowConfidenceFallsBack(t *testing.T) {
	clf := testClassifier(t, map[string]any{
		"tier": map[string]any{"type": "choice", "choice": "small", "probabilities": map[string]float64{"small": 0.4, "standard": 0.35, "complex": 0.25}, "confidence": 0.2},
		"risk": map[string]any{"type": "noul", "noul": 0.5},
	}, map[string]any{})
	_, meta, err := clf.ClassifyTier(context.Background(), Input{Prompt: "x", Survey: survey})
	if !errors.Is(err, ErrLowConfidence) {
		t.Fatalf("err = %v, want ErrLowConfidence", err)
	}
	if meta.TierConfidence != 0.2 {
		t.Errorf("meta confidence = %v", meta.TierConfidence)
	}
}

func TestMalformedTierAnswer(t *testing.T) {
	clf := testClassifier(t, map[string]any{
		"risk": map[string]any{"type": "noul", "noul": 0.5},
	}, map[string]any{})
	if _, _, err := clf.ClassifyTier(context.Background(), Input{Prompt: "x", Survey: survey}); err == nil {
		t.Fatal("expected error for missing tier answer")
	}
}

func TestUnknownTierRejected(t *testing.T) {
	clf := testClassifier(t, map[string]any{
		"tier": map[string]any{"type": "choice", "choice": "gigantic", "probabilities": map[string]float64{"gigantic": 1}, "confidence": 1},
		"risk": map[string]any{"type": "noul", "noul": 0},
	}, map[string]any{})
	if _, _, err := clf.ClassifyTier(context.Background(), Input{Prompt: "x", Survey: survey}); err == nil {
		t.Fatal("expected error for unknown tier")
	}
}

func TestRiskTrue(t *testing.T) {
	clf := testClassifier(t, map[string]any{
		"tier": map[string]any{"type": "choice", "choice": "critical", "probabilities": map[string]float64{"critical": 0.9}, "confidence": 0.9},
		"risk": map[string]any{"type": "noul", "noul": 0.93},
	}, map[string]any{})
	v, _, err := clf.ClassifyTier(context.Background(), Input{Prompt: "rotate secrets", Survey: survey})
	if err != nil {
		t.Fatalf("ClassifyTier: %v", err)
	}
	if !v.RiskBehaviorChange {
		t.Error("risk = false, want true")
	}
}

func TestDisabled(t *testing.T) {
	clf := New(nil, Config{})
	if _, _, err := clf.ClassifyTier(context.Background(), Input{Prompt: "x"}); !errors.Is(err, ErrDisabled) {
		t.Fatalf("err = %v, want ErrDisabled", err)
	}
}

func TestStateTooLarge(t *testing.T) {
	clf := testClassifier(t, map[string]any{}, map[string]any{})
	clf.Config.MaxStateBytes = 16
	_, _, err := clf.ClassifyTier(context.Background(), Input{Prompt: "this is definitely longer than sixteen bytes"})
	if !errors.Is(err, ErrStateTooLarge) {
		t.Fatalf("err = %v, want ErrStateTooLarge", err)
	}
}

func TestParseSurveyCandidates(t *testing.T) {
	got := ParseSurveyCandidates(survey)
	want := []string{"cmd", "docs", "shared", "vendor"}
	if len(got) != len(want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %#v, want %#v", got, want)
		}
	}
	if got := ParseSurveyCandidates("no survey line here"); got != nil {
		t.Errorf("no line -> %#v", got)
	}
	if got := ParseSurveyCandidates("Top-level entries: README.md, main.go"); len(got) != 2 || got[1] != "main.go" {
		t.Errorf("got %#v", got)
	}
}

func TestConfigFromEnv(t *testing.T) {
	t.Setenv(EnvEnabled, "1")
	t.Setenv(EnvModel, "jev-1.13.0")
	t.Setenv(EnvMinConfidence, "0.7")
	t.Setenv(EnvFileThreshold, "0.6")
	t.Setenv(EnvTimeout, "3s")
	cfg := ConfigFromEnv()
	if !cfg.Enabled || cfg.Model != "jev-1.13.0" || cfg.MinTierConfidence != 0.7 || cfg.FileThreshold != 0.6 || cfg.Timeout != 3*time.Second {
		t.Fatalf("cfg = %+v", cfg)
	}
	// Malformed values fall back to defaults rather than disabling.
	t.Setenv(EnvMinConfidence, "not-a-number")
	if got := ConfigFromEnv().MinTierConfidence; got != defaultMinConf {
		t.Errorf("malformed min confidence = %v, want %v", got, defaultMinConf)
	}
}

func TestBuildQuestionsIncludesEveryCandidate(t *testing.T) {
	cands := ParseSurveyCandidates(survey)
	qs := buildQuestions(cands)
	if len(qs) != len(cands)+2 {
		t.Fatalf("questions = %d, want %d", len(qs), len(cands)+2)
	}
	if qs["tier"].Type != "choice" || qs["risk"].Type != "noul" {
		t.Fatalf("unexpected question types: %#v", qs)
	}
	if err := qs["tier"].Validate(); err != nil {
		t.Errorf("tier: %v", err)
	}
	if err := qs["risk"].Validate(); err != nil {
		t.Errorf("risk: %v", err)
	}
}

func TestClassifyTriageHappyPath(t *testing.T) {
	clf := testClassifier(t, map[string]any{
		"action": map[string]any{
			"type": "choice", "choice": "human",
			"probabilities": map[string]float64{"human": 0.71, "refile": 0.2, "resume": 0.09},
			"confidence":    0.71,
		},
	}, map[string]any{"input_tokens": int64(5000), "output_tokens": int64(0)})

	v, err := clf.ClassifyTriage(context.Background(), "Failure kind: rebase_conflict\nRetained branch: solu/wft_x")
	if err != nil {
		t.Fatalf("ClassifyTriage: %v", err)
	}
	if v.Action != "human" {
		t.Errorf("action = %q, want human", v.Action)
	}
	if v.Confidence != 0.71 {
		t.Errorf("confidence = %v, want 0.71", v.Confidence)
	}
	if v.Probabilities["refile"] != 0.2 {
		t.Errorf("probabilities = %#v", v.Probabilities)
	}
}

func TestClassifyTriageRejectsUnknownAction(t *testing.T) {
	clf := testClassifier(t, map[string]any{
		"action": map[string]any{"type": "choice", "choice": "delete-everything", "confidence": 0.9},
	}, map[string]any{})
	if _, err := clf.ClassifyTriage(context.Background(), "packet"); err == nil {
		t.Fatal("unknown action accepted")
	}
}

func TestClassifyTriageRejectsEmptyPacketAndDisabled(t *testing.T) {
	clf := testClassifier(t, map[string]any{
		"action": map[string]any{"type": "choice", "choice": "human", "confidence": 0.9},
	}, map[string]any{})
	if _, err := clf.ClassifyTriage(context.Background(), "   "); err == nil {
		t.Fatal("empty packet accepted")
	}
	disabled := New(typesafe.New("k"), Config{})
	if _, err := disabled.ClassifyTriage(context.Background(), "packet"); !errors.Is(err, ErrDisabled) {
		t.Fatalf("disabled err = %v, want ErrDisabled", err)
	}
}

func TestTriageQuestionIsAValidChoiceOverTheActionVocabulary(t *testing.T) {
	q := triageQuestion()
	if q.Type != "choice" {
		t.Fatalf("type = %q, want choice", q.Type)
	}
	if err := q.Validate(); err != nil {
		t.Fatalf("question invalid: %v", err)
	}
	for _, action := range TriageActions {
		if !validTriageAction(action) {
			t.Errorf("action %q not valid", action)
		}
	}
	if validTriageAction("nope") {
		t.Error("unknown action accepted by validTriageAction")
	}
}
