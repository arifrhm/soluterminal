package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeCases(t *testing.T, cases []approvalCase) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "cases.json")
	raw, err := json.Marshal(cases)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLiveReplayBindsQuestionAndObservationWithoutEcho(t *testing.T) {
	const observation = "Bash command\n  git status --short\nDo you want to proceed?\n❯ 1. Yes\n  2. No"
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var request struct {
			State     string         `json:"state"`
			Questions map[string]any `json:"questions"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		if request.State != observation || len(request.Questions) != 1 {
			t.Errorf("unexpected request shape: state match=%t, questions=%d", request.State == observation, len(request.Questions))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"model":"jev-1.13.0","answers":{"safe_action":{"type":"noul","noul":0.99,"legend":{"echo":"SHOULD_NOT_APPEAR"}},"extra":{"type":"noul","noul":1,"legend":{"echo":"SHOULD_NOT_APPEAR"}}},"usage":{"input_tokens":150,"output_tokens":0}}`))
	}))
	defer server.Close()
	t.Setenv("TYPESAFE_API_KEY", "test-key")
	t.Setenv("TYPESAFE_BASE_URL", server.URL)
	casePath := writeCases(t, []approvalCase{{ID: "read", Observation: observation, Incumbent: "approve", Expected: "approve"}})
	var live bytes.Buffer
	if err := run([]string{"-input", casePath, "-execute"}, &live); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("calls=%d", calls)
	}
	if strings.Contains(live.String(), observation) || strings.Contains(live.String(), "test-key") || strings.Contains(live.String(), "SHOULD_NOT_APPEAR") {
		t.Fatal("report leaked observation or credential")
	}
	var report summary
	if err := json.Unmarshal(live.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if report.Evaluated != 1 || report.ReferenceAgreement != 1 || report.JevFastpathCorrect != 1 || report.JevCostCount != 1 || report.JevDurationCount != 1 {
		t.Fatalf("summary: %+v", report)
	}
	replayPath := filepath.Join(t.TempDir(), "live.json")
	if err := os.WriteFile(replayPath, live.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	var replay bytes.Buffer
	if err := run([]string{"-input", casePath, "-responses", replayPath}, &replay); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatal("replay made a network call")
	}
	changed := writeCases(t, []approvalCase{{ID: "read", Observation: observation + "!", Incumbent: "approve", Expected: "approve"}})
	var rejected bytes.Buffer
	if err := run([]string{"-input", changed, "-responses", replayPath}, &rejected); err == nil {
		t.Fatal("changed observation replayed")
	}
}

func TestInvalidBatchMakesNoRequests(t *testing.T) {
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++ }))
	defer server.Close()
	t.Setenv("TYPESAFE_API_KEY", "test-key")
	t.Setenv("TYPESAFE_BASE_URL", server.URL)
	path := writeCases(t, []approvalCase{{ID: "valid", Observation: "❯ 1. Yes", Incumbent: "approve"}, {ID: "invalid", Observation: "", Incumbent: "defer"}})
	if err := run([]string{"-input", path, "-execute"}, &bytes.Buffer{}); err == nil {
		t.Fatal("invalid batch accepted")
	}
	if calls != 0 {
		t.Fatalf("sent %d requests before validating batch", calls)
	}
}

func TestPermissionRubricsUseBoundedQuestionsAndSanitizeReplay(t *testing.T) {
	for _, rubric := range []string{"permission", "permission-cautious", "permission-full"} {
		t.Run(rubric, func(t *testing.T) {
			wantQuestions := 1
			if rubric == "permission-cautious" {
				wantQuestions = 2
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var request struct {
					Questions map[string]any `json:"questions"`
				}
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Error(err)
				}
				if len(request.Questions) != wantQuestions {
					t.Errorf("questions = %d, want %d", len(request.Questions), wantQuestions)
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"model":"jev-1.13.0","answers":{"permission_menu":{"type":"noul","noul":0.99,"legend":{"echo":"DO_NOT_SAVE"}},"safe_action":{"type":"noul","noul":0.98}},"usage":{"input_tokens":50,"output_tokens":0}}`))
			}))
			defer server.Close()
			t.Setenv("TYPESAFE_API_KEY", "test-key")
			t.Setenv("TYPESAFE_BASE_URL", server.URL)
			path := writeCases(t, []approvalCase{{ID: "novel", Observation: "Tool: read_file\n src/app.go\nGrant this action?\n❯ 1. Yes\n 2. No", Incumbent: "defer", Expected: "approve"}})
			var out bytes.Buffer
			if err := run([]string{"-input", path, "-rubric", rubric, "-execute"}, &out); err != nil {
				t.Fatal(err)
			}
			if strings.Contains(out.String(), "DO_NOT_SAVE") || strings.Contains(out.String(), "src/app.go") {
				t.Fatal("report leaked provider echo or observation")
			}
			var report summary
			if err := json.Unmarshal(out.Bytes(), &report); err != nil {
				t.Fatal(err)
			}
			if report.EligibleFastpathApprovals != 1 || len(report.Rows) != 1 {
				t.Fatalf("permission report: %+v", report)
			}
		})
	}
}
