package wire

import (
	"bytes"
	"encoding/json"
	"testing"
)

// capturedCCUsageBatchPayload was produced by json.Marshal of the daemon's
// ccUsageBatch at base commit 0f0ca1af8c567817de043676e39cdbf0c1aea500.
const capturedCCUsageBatchPayload = `{"records":[{"event_ts":"2026-06-20T12:34:56.789Z","session_id":"session-captured-1","message_id":"message-captured-2","request_id":"request-captured-3","model":"claude-opus-4-8","in_app":true,"is_sidechain":true,"account":"alice@example.com","workflow_invocation_id":"wfi_123","workflow_role":"reviewer:spec_compliance","cache_ttl_class":"5m","cache_ttl_control":"applied","long_gap_observed":true,"input_tokens":101,"output_tokens":202,"cache_read_tokens":303,"cache_create_tokens":404,"cache_create_5m_tokens":505,"cache_create_1h_tokens":606,"total_tokens":707,"service_tier":"priority","speed":"fast","cwd":"/workspace/project","git_branch":"feature/wire-dtos","git_repo":"https://github.com/example/project","cc_version":"1.2.3","web_search_requests":8,"web_fetch_requests":9,"stop_reason":"end_turn"},{"event_ts":"","session_id":"","message_id":"","request_id":"","model":"","in_app":false,"is_sidechain":false,"account":"","workflow_invocation_id":"","workflow_role":"","cache_ttl_class":"","cache_ttl_control":"","long_gap_observed":false,"input_tokens":0,"output_tokens":0,"cache_read_tokens":0,"cache_create_tokens":0,"cache_create_5m_tokens":0,"cache_create_1h_tokens":0,"total_tokens":0,"service_tier":"","speed":"","cwd":"","git_branch":"","git_repo":"","cc_version":"","web_search_requests":0,"web_fetch_requests":0,"stop_reason":""}]}`

var capturedCCUsageBatchValue = CCUsageBatch{Records: []CCUsageRecord{
	{
		EventTS:              "2026-06-20T12:34:56.789Z",
		SessionID:            "session-captured-1",
		MessageID:            "message-captured-2",
		RequestID:            "request-captured-3",
		Model:                "claude-opus-4-8",
		InApp:                true,
		IsSidechain:          true,
		Account:              "alice@example.com",
		WorkflowInvocationID: "wfi_123",
		WorkflowRole:         "reviewer:spec_compliance",
		CacheTTLClass:        "5m",
		CacheTTLControl:      "applied",
		LongGapObserved:      true,
		InputTokens:          101,
		OutputTokens:         202,
		CacheReadTokens:      303,
		CacheCreateTokens:    404,
		CacheCreate5mTokens:  505,
		CacheCreate1hTokens:  606,
		TotalTokens:          707,
		ServiceTier:          "priority",
		Speed:                "fast",
		Cwd:                  "/workspace/project",
		GitBranch:            "feature/wire-dtos",
		GitRepo:              "https://github.com/example/project",
		CCVersion:            "1.2.3",
		WebSearchRequests:    8,
		WebFetchRequests:     9,
		StopReason:           "end_turn",
	},
	{},
}}

func TestCCUsageBatch_DecodeCapturedPayload(t *testing.T) {
	var got CCUsageBatch
	if err := json.Unmarshal([]byte(capturedCCUsageBatchPayload), &got); err != nil {
		t.Fatalf("unmarshal captured payload: %v", err)
	}
	if len(got.Records) != len(capturedCCUsageBatchValue.Records) {
		t.Fatalf("decoded %d records, want %d", len(got.Records), len(capturedCCUsageBatchValue.Records))
	}
	for i := range got.Records {
		if got.Records[i] != capturedCCUsageBatchValue.Records[i] {
			t.Errorf("decoded record %d mismatch:\n got %#v\nwant %#v", i, got.Records[i], capturedCCUsageBatchValue.Records[i])
		}
	}

	for _, payload := range []string{`{"records":null}`, `{}`} {
		var nilRecords CCUsageBatch
		if err := json.Unmarshal([]byte(payload), &nilRecords); err != nil {
			t.Fatalf("unmarshal %s: %v", payload, err)
		}
		if nilRecords.Records != nil {
			t.Errorf("unmarshal %s records = %#v, want nil", payload, nilRecords.Records)
		}
	}
}

func TestCCUsageBatch_EncodeByteExact(t *testing.T) {
	got, err := json.Marshal(capturedCCUsageBatchValue)
	if err != nil {
		t.Fatalf("marshal captured value: %v", err)
	}
	if !bytes.Equal(got, []byte(capturedCCUsageBatchPayload)) {
		t.Fatalf("encoded payload mismatch:\n got %s\nwant %s", got, capturedCCUsageBatchPayload)
	}

	for name, tc := range map[string]struct {
		value CCUsageBatch
		want  string
	}{
		"nil records":   {value: CCUsageBatch{}, want: `{"records":null}`},
		"empty records": {value: CCUsageBatch{Records: []CCUsageRecord{}}, want: `{"records":[]}`},
	} {
		encoded, err := json.Marshal(tc.value)
		if err != nil {
			t.Fatalf("marshal %s: %v", name, err)
		}
		if !bytes.Equal(encoded, []byte(tc.want)) {
			t.Errorf("marshal %s = %s, want %s", name, encoded, tc.want)
		}
	}
}

func TestCCUsageRecord_AllKeysAlwaysEmitted(t *testing.T) {
	encoded, err := json.Marshal(CCUsageRecord{})
	if err != nil {
		t.Fatalf("marshal zero record: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(encoded, &got); err != nil {
		t.Fatalf("unmarshal zero record: %v", err)
	}
	want := map[string]struct{}{
		"event_ts": {}, "session_id": {}, "message_id": {}, "request_id": {},
		"model": {}, "in_app": {}, "is_sidechain": {}, "account": {},
		"workflow_invocation_id": {}, "workflow_role": {}, "cache_ttl_class": {},
		"cache_ttl_control": {}, "long_gap_observed": {},
		"input_tokens": {}, "output_tokens": {}, "cache_read_tokens": {},
		"cache_create_tokens": {}, "cache_create_5m_tokens": {},
		"cache_create_1h_tokens": {}, "total_tokens": {}, "service_tier": {},
		"speed": {}, "cwd": {}, "git_branch": {}, "git_repo": {},
		"cc_version": {}, "web_search_requests": {}, "web_fetch_requests": {},
		"stop_reason": {},
	}
	if len(got) != len(want) {
		t.Fatalf("zero record emitted %d keys, want %d: %s", len(got), len(want), encoded)
	}
	for key := range got {
		if _, ok := want[key]; !ok {
			t.Errorf("zero record emitted unexpected key %q", key)
		}
	}
	for key := range want {
		if _, ok := got[key]; !ok {
			t.Errorf("zero record omitted key %q", key)
		}
	}
}
