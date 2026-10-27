package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"soluterminal-shared/machineauth"
)

func TestReportCCUsageBatchPostShape(t *testing.T) {
	var gotBody []byte
	var gotAuth, gotTS, gotSig string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			t.Errorf("method = %s, want POST", r.Method)
		}
		if r.URL.Path != "/api/internal/cc-usage" {
			t.Errorf("path = %s, want /api/internal/cc-usage", r.URL.Path)
		}
		gotBody, _ = io.ReadAll(r.Body)
		gotAuth = r.Header.Get(machineauth.AuthorizationHeader)
		gotTS = r.Header.Get(machineauth.TimestampHeader)
		gotSig = r.Header.Get(machineauth.SignatureHeader)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	recs := []ccUsageRecord{{
		EventTS:              "2026-06-17T11:01:05.277Z",
		SessionID:            "sess-1",
		MessageID:            "msg-1",
		RequestID:            "req-1",
		Model:                "claude-opus-4-8",
		InApp:                true,
		IsSidechain:          false,
		Account:              "alice@example.com",
		WorkflowInvocationID: "wfi_123",
		WorkflowRole:         "reviewer:spec_compliance",
		CacheTTLClass:        "5m",
		CacheTTLControl:      "applied",
		LongGapObserved:      true,
		InputTokens:          10,
		OutputTokens:         20,
		CacheReadTokens:      5,
		CacheCreateTokens:    3,
		CacheCreate5mTokens:  1,
		CacheCreate1hTokens:  2,
		TotalTokens:          38,
		ServiceTier:          "standard",
		Speed:                "fast",
		Cwd:                  "/home/x",
		GitBranch:            "main",
		GitRepo:              "https://github.com/example/project",
		CCVersion:            "1.2.3",
		WebSearchRequests:    1,
		WebFetchRequests:     2,
		StopReason:           "end_turn",
	}}

	err := reportCCUsageBatch(context.Background(), srv.Client(), srv.URL, "tok-abc", recs)
	if err != nil {
		t.Fatalf("reportCCUsageBatch: %v", err)
	}

	// Auth + HMAC headers present.
	if gotAuth != machineauth.BearerPrefix+"tok-abc" {
		t.Fatalf("Authorization = %q, want %q", gotAuth, machineauth.BearerPrefix+"tok-abc")
	}
	if gotTS == "" || gotSig == "" {
		t.Fatalf("missing HMAC headers: ts=%q sig=%q", gotTS, gotSig)
	}
	// Signature must verify against the body the server received.
	if want := machineauth.Signature("tok-abc", gotTS, gotBody); want != gotSig {
		t.Fatalf("signature mismatch: got %q want %q", gotSig, want)
	}

	// Body shape: wrapped in {"records":[...]} with exact snake_case fields.
	var env struct {
		Records []map[string]any `json:"records"`
	}
	if err := json.Unmarshal(gotBody, &env); err != nil {
		t.Fatalf("decode body: %v (body=%s)", err, gotBody)
	}
	if len(env.Records) != 1 {
		t.Fatalf("records len = %d, want 1", len(env.Records))
	}
	rec := env.Records[0]
	checks := map[string]any{
		"event_ts":               "2026-06-17T11:01:05.277Z",
		"session_id":             "sess-1",
		"message_id":             "msg-1",
		"request_id":             "req-1",
		"model":                  "claude-opus-4-8",
		"in_app":                 true,
		"is_sidechain":           false,
		"account":                "alice@example.com",
		"workflow_invocation_id": "wfi_123",
		"workflow_role":          "reviewer:spec_compliance",
		"cache_ttl_class":        "5m",
		"cache_ttl_control":      "applied",
		"long_gap_observed":      true,
		"input_tokens":           float64(10),
		"output_tokens":          float64(20),
		"cache_read_tokens":      float64(5),
		"cache_create_tokens":    float64(3),
		"cache_create_5m_tokens": float64(1),
		"cache_create_1h_tokens": float64(2),
		"total_tokens":           float64(38),
		"service_tier":           "standard",
		"speed":                  "fast",
		"cwd":                    "/home/x",
		"git_branch":             "main",
		"git_repo":               "https://github.com/example/project",
		"cc_version":             "1.2.3",
		"web_search_requests":    float64(1),
		"web_fetch_requests":     float64(2),
		"stop_reason":            "end_turn",
	}
	if len(rec) != len(checks) {
		t.Fatalf("record has %d keys, want exactly %d; body=%s", len(rec), len(checks), gotBody)
	}
	for k, want := range checks {
		got, ok := rec[k]
		if !ok {
			t.Errorf("missing json field %q", k)
			continue
		}
		if got != want {
			t.Errorf("field %q = %v (%T), want %v (%T)", k, got, got, want, want)
		}
	}
}

func TestReportCCUsageBatchStatusCodes(t *testing.T) {
	rec := []ccUsageRecord{{MessageID: "m", RequestID: "r"}}

	// 204 → nil.
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer ok.Close()
	if err := reportCCUsageBatch(context.Background(), ok.Client(), ok.URL, "t", rec); err != nil {
		t.Fatalf("204 should be nil, got %v", err)
	}

	// 500 → error.
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer bad.Close()
	if err := reportCCUsageBatch(context.Background(), bad.Client(), bad.URL, "t", rec); err == nil {
		t.Fatalf("500 should error, got nil")
	}
}

func TestReportCCUsageBatchChunks(t *testing.T) {
	var mu sync.Mutex
	var requestSizes []int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var env ccUsageBatch
		_ = json.Unmarshal(body, &env)
		mu.Lock()
		requestSizes = append(requestSizes, len(env.Records))
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	// 1100 records → 500 + 500 + 100 = 3 requests.
	recs := make([]ccUsageRecord, 1100)
	for i := range recs {
		recs[i] = ccUsageRecord{MessageID: "m", RequestID: "r"}
	}
	if err := reportCCUsageBatch(context.Background(), srv.Client(), srv.URL, "t", recs); err != nil {
		t.Fatalf("reportCCUsageBatch: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(requestSizes) != 3 {
		t.Fatalf("got %d requests, want 3 (sizes=%v)", len(requestSizes), requestSizes)
	}
	want := []int{500, 500, 100}
	for i, n := range want {
		if requestSizes[i] != n {
			t.Fatalf("chunk %d size = %d, want %d", i, requestSizes[i], n)
		}
	}
}

func TestReportCCUsageBatchNoOp(t *testing.T) {
	t.Parallel()
	// Empty inputs are no-ops (nil), and must not panic with nil client.
	if err := reportCCUsageBatch(context.Background(), nil, "", "", nil); err != nil {
		t.Fatalf("empty no-op should be nil, got %v", err)
	}
	if err := reportCCUsageBatch(context.Background(), nil, "https://x", "t", nil); err != nil {
		t.Fatalf("no records should be nil, got %v", err)
	}
}
