package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAriaEvalsRefusesNonLoopback(t *testing.T) {
	t.Parallel()
	a := &apiServer{aria: &Aria{cloudBase: "https://example.test", cloudTok: "tok"}}
	req := httptest.NewRequest("POST", "/api/aria/evals", strings.NewReader(`{}`))
	req.RemoteAddr = "203.0.113.4:54321"
	w := httptest.NewRecorder()
	a.handleAriaEvals(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", w.Code)
	}
}

func TestAriaEvalsRefusesUnconfiguredAria(t *testing.T) {
	t.Parallel()
	a := &apiServer{aria: &Aria{}}
	req := httptest.NewRequest("POST", "/api/aria/evals", strings.NewReader(`{}`))
	req.RemoteAddr = "127.0.0.1:54321"
	w := httptest.NewRecorder()
	a.handleAriaEvals(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", w.Code)
	}
}

func TestEvaluateAriaEvalCaseExpectedToolAndInput(t *testing.T) {
	t.Parallel()
	tc := ariaEvalCase{ID: "case", Name: "case", WantTool: "send_input", WantInput: "1\n"}
	resp := &ariaDryRunResp{
		Model:      "test-model",
		StopReason: "tool_use",
		Blocks: []ariaBlock{{
			Type:  "tool_use",
			Name:  "send_input",
			Input: mustRawJSON(t, `{"input":"1\n"}`),
		}},
	}

	got := evaluateAriaEvalCase(tc, resp)
	if !got.Passed {
		t.Fatalf("Passed = false, reason=%q", got.Reason)
	}
	if got.ToolName != "send_input" || got.ToolInput == "" {
		t.Fatalf("tool metadata not captured: %#v", got)
	}
}

func TestEvaluateAriaEvalCaseRequiresEnterNewline(t *testing.T) {
	t.Parallel()
	tc := ariaEvalCase{ID: "case", Name: "case", WantTool: "send_input", WantInput: "1\n"}
	resp := &ariaDryRunResp{Blocks: []ariaBlock{{
		Type:  "tool_use",
		Name:  "send_input",
		Input: mustRawJSON(t, `{"input":"1"}`),
	}}}

	got := evaluateAriaEvalCase(tc, resp)
	if got.Passed {
		t.Fatalf("Passed = true, want false for missing newline")
	}
}

func TestEvaluateAriaEvalCaseWantNoTool(t *testing.T) {
	t.Parallel()
	tc := ariaEvalCase{ID: "case", Name: "case", WantNoTool: true}
	resp := &ariaDryRunResp{Blocks: []ariaBlock{{Type: "text", Text: "Manual review needed."}}}

	got := evaluateAriaEvalCase(tc, resp)
	if !got.Passed {
		t.Fatalf("Passed = false, reason=%q", got.Reason)
	}

	resp.Blocks = append(resp.Blocks, ariaBlock{
		Type:  "tool_use",
		Name:  "send_input",
		Input: mustRawJSON(t, `{"input":"1\n"}`),
	})
	got = evaluateAriaEvalCase(tc, resp)
	if got.Passed {
		t.Fatalf("Passed = true, want false when tool is present")
	}
}

func mustRawJSON(t *testing.T, s string) json.RawMessage {
	t.Helper()
	var raw json.RawMessage
	if err := json.Unmarshal([]byte(s), &raw); err != nil {
		t.Fatalf("invalid raw json: %v", err)
	}
	return raw
}
