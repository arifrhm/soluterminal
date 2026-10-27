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

func inputFile(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "input.json")
	if err := os.WriteFile(p, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	return p
}

const selectionCase = `[{"id":"a","mode":"selection","state":{"evidence":"private-evidence-sentinel"},"candidates":{"candidate":"Relevant file"},"expected":"candidate"}]`

func TestDryRunDoesNotCallAPIOrPrintState(t *testing.T) {
	t.Setenv("TYPESAFE_API_KEY", "")
	var out bytes.Buffer
	if err := run([]string{"-input", inputFile(t, selectionCase)}, &out); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "private-evidence-sentinel") {
		t.Fatal("state leaked")
	}
	var r report
	if err := json.Unmarshal(out.Bytes(), &r); err != nil {
		t.Fatal(err)
	}
	if r.Mode != "dry_run" || r.Evaluated != 0 || !r.AdvisoryOnly {
		t.Fatalf("report: %+v", r)
	}
}
func TestLiveShadowAndOfflineReplayBindEvidence(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var req map[string]any
		_ = json.NewDecoder(r.Body).Decode(&req)
		raw, _ := json.Marshal(req)
		if strings.Contains(string(raw), `"expected"`) {
			t.Error("label sent to provider")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"model":"jev-1.13.0","answers":{"decision":{"type":"choice","choice":"candidate","confidence":0.9,"probabilities":{"candidate":0.9,"unknown":0.1}}},"usage":{"input_tokens":100}}`))
	}))
	defer srv.Close()
	t.Setenv("TYPESAFE_API_KEY", "test")
	t.Setenv("TYPESAFE_BASE_URL", srv.URL)
	input := inputFile(t, selectionCase)
	var out bytes.Buffer
	if err := run([]string{"-input", input, "-execute"}, &out); err != nil {
		t.Fatal(err)
	}
	var r report
	if err := json.Unmarshal(out.Bytes(), &r); err != nil {
		t.Fatal(err)
	}
	if r.Correct != 1 || r.Evaluated != 1 || calls != 1 {
		t.Fatalf("report=%+v calls=%d", r, calls)
	}
	replay := inputFile(t, out.String())
	out.Reset()
	if err := run([]string{"-input", input, "-responses", replay, "-threshold", "0.95"}, &out); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(out.Bytes(), &r); err != nil {
		t.Fatal(err)
	}
	if calls != 1 || r.Abstained != 1 || r.LabelledDecisions != 0 {
		t.Fatal("replay called API or failed to abstain")
	}
	out.Reset()
	changed := inputFile(t, strings.Replace(selectionCase, "private-evidence-sentinel", "different", 1))
	if err := run([]string{"-input", changed, "-responses", replay}, &out); err == nil {
		t.Fatal("replay accepted changed evidence")
	}
}
func TestWholeBatchValidationBeforeNetwork(t *testing.T) {
	cases := strings.TrimSuffix(selectionCase, "]") + `,{"id":"invalid","mode":"approve","state":{}}]`
	t.Setenv("TYPESAFE_API_KEY", "")
	var out bytes.Buffer
	err := run([]string{"-input", inputFile(t, cases), "-execute"}, &out)
	if err == nil || !strings.Contains(err.Error(), "unknown shadow mode") {
		t.Fatalf("did not validate batch first: %v", err)
	}
	for _, args := range [][]string{{"-execute", "-responses", "anything"}, {"-model", "jev-latest"}, {"-max-calls", "0"}, {"-threshold", "NaN"}} {
		if err := run(append([]string{"-input", inputFile(t, selectionCase)}, args...), &out); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
}

func TestCredentialResolutionUsesOnlyTypeSafeAndEnvironmentPrecedence(t *testing.T) {
	dir := t.TempDir()
	raw := `{"runtimes":{"typesafe-api":{"TYPESAFE_API_KEY":"stored-sentinel"},"other":{"TYPESAFE_API_KEY":"wrong-provider"}}}`
	if err := os.WriteFile(filepath.Join(dir, "workflow_secrets.json"), []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TYPESAFE_API_KEY", "")
	key, err := apiKey(dir)
	if err != nil || key != "stored-sentinel" {
		t.Fatal("stored TypeSafe resolution failed")
	}
	t.Setenv("TYPESAFE_API_KEY", "override-sentinel")
	key, err = apiKey(dir)
	if err != nil || key != "override-sentinel" {
		t.Fatal("environment precedence failed")
	}
	t.Setenv("TYPESAFE_API_KEY", "")
	if err := os.WriteFile(filepath.Join(dir, "workflow_secrets.json"), []byte(`{"secret":"secret-sentinel`), 0600); err != nil {
		t.Fatal(err)
	}
	_, err = apiKey(dir)
	if err == nil || strings.Contains(err.Error(), "secret-sentinel") {
		t.Fatal("unsafe malformed-store error")
	}
	var out bytes.Buffer
	if err := run([]string{"-input", inputFile(t, selectionCase), "-data", dir}, &out); err != nil {
		t.Fatal("offline mode opened malformed credential store")
	}
}
