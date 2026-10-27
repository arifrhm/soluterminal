package main

import (
	"encoding/json"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

// These fixtures are REAL captured lines copied verbatim from
// docs/native-phase0/vocab-thinking.jsonl and docs/native-phase0/approval-fire.jsonl
// (Phase-0 captured bytes — authoritative). Each constant is exactly one JSONL
// line as emitted by claude 2.1.183 --output-format stream-json.

const (
	// vocab-thinking.jsonl L1 — system/init
	fxInit = `{"type":"system","subtype":"init","cwd":"/tmp/tmp.263v7tmtnp","session_id":"286a3205-807e-4de4-840c-01f546cc8fa7","tools":["Task","Bash","Read"],"mcp_servers":[{"name":"playwright","status":"pending"}],"model":"claude-opus-4-8[1m]","permissionMode":"auto","slash_commands":["verify","debug"],"claude_code_version":"2.1.183"}`

	// vocab-thinking.jsonl L2 — system/status
	fxStatus = `{"type":"system","subtype":"status","status":"requesting","uuid":"07715efe","session_id":"286a3205"}`

	// vocab-thinking.jsonl L3 — rate_limit_event
	fxRateLimit = `{"type":"rate_limit_event","rate_limit_info":{"status":"allowed","resetsAt":1781949600,"rateLimitType":"five_hour"},"uuid":"fdb40b11","session_id":"286a3205"}`

	// vocab-thinking.jsonl L6 — system/thinking_tokens (NOT in plan §3 table)
	fxThinkingTokens = `{"type":"system","subtype":"thinking_tokens","estimated_tokens":50,"estimated_tokens_delta":50,"uuid":"d058a94b","session_id":"286a3205"}`

	// vocab-thinking.jsonl L7 — content_block_delta / thinking_delta (empty text => dropped)
	fxThinkingDeltaEmpty = `{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"","estimated_tokens":50}},"session_id":"286a3205","parent_tool_use_id":null,"uuid":"aefb4a3b"}`

	// vocab-thinking.jsonl L8/9 — content_block_delta / signature_delta
	fxSignatureDelta = `{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"EqwDCmMIDhgC"}},"session_id":"286a3205","parent_tool_use_id":null,"uuid":"bece27e2"}`

	// vocab-thinking.jsonl L10 — user replay echo (isReplay:true => dropped)
	fxUserReplay = `{"type":"user","message":{"role":"user","content":"Think step by step"},"session_id":"286a3205","parent_tool_use_id":null,"uuid":"9fa20030","timestamp":"2026-06-20T07:45:03.073Z","isReplay":true}`

	// vocab-thinking.jsonl L14 — content_block_delta / text_delta
	fxTextDelta = `{"type":"stream_event","event":{"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"**Answer: After 3 days.**"}},"session_id":"286a3205","parent_tool_use_id":null,"uuid":"05114b76"}`

	// vocab-thinking.jsonl L21 — finalized assistant message with text block
	fxAssistantText = `{"type":"assistant","message":{"model":"claude-opus-4-8","id":"msg_01Qta4tisVy7nF1BoSnGgber","type":"message","role":"assistant","content":[{"type":"text","text":"**Answer: After 3 days.**"}],"usage":{"input_tokens":2964,"cache_read_input_tokens":15626,"output_tokens":345}},"parent_tool_use_id":null,"session_id":"286a3205","uuid":"889ba2be","request_id":"req_011CcE92vd51DSTr6NjqgxHB"}`

	// vocab-thinking.jsonl L25 — result success
	fxResultSuccess = `{"type":"result","subtype":"success","is_error":false,"duration_ms":4903,"num_turns":1,"result":"**Answer: After 3 days.**","total_cost_usd":0.051819,"permission_denials":[]}`

	// approval-fire.jsonl L9 — content_block_start tool_use (structural no-op)
	fxToolUseStart = `{"type":"stream_event","event":{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_01EgPPcKVwkPTB6LRF52HJog","name":"Bash","input":{},"caller":{"type":"direct"}}},"session_id":"9c5e1b44","parent_tool_use_id":null,"uuid":"aeb2447d"}`

	// approval-fire.jsonl L11 — input_json_delta (streaming args => dropped standalone)
	fxInputJSONDelta = `{"type":"stream_event","event":{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"command\": \"echo hi"}},"session_id":"9c5e1b44","parent_tool_use_id":null,"uuid":"4f02ec30"}`

	// approval-fire.jsonl L15 — finalized assistant tool_use
	fxAssistantToolUse = `{"type":"assistant","message":{"model":"claude-opus-4-8","id":"msg_01HkTF4RcEHUUJkzsS1TAddL","type":"message","role":"assistant","content":[{"type":"tool_use","id":"toolu_01EgPPcKVwkPTB6LRF52HJog","name":"Bash","input":{"command":"echo hi > /tmp/tmp.hJc98qZbec/probe.txt","description":"Write hi to probe.txt"},"caller":{"type":"direct"}}],"usage":{"input_tokens":2819,"cache_read_input_tokens":15626,"output_tokens":2}},"parent_tool_use_id":null,"session_id":"9c5e1b44","uuid":"b24fce58","request_id":"req_011CcEArG9VtyBfuHBCPnev7"}`

	// approval-fire.jsonl L17 — control_request can_use_tool (THE marquee event)
	fxControlRequest = `{"type":"control_request","request_id":"019c298e-9707-43f3-bf84-5ac76a05883e","request":{"subtype":"can_use_tool","tool_name":"Bash","display_name":"Bash","input":{"command":"echo hi > /tmp/tmp.hJc98qZbec/probe.txt","description":"Write hi to probe.txt"},"description":"Write hi to probe.txt","blocked_path":"/tmp/tmp.hJc98qZbec/probe.txt","tool_use_id":"toolu_01EgPPcKVwkPTB6LRF52HJog"}}`

	// approval-stdin.jsonl L20 — user tool_result
	fxToolResult = `{"type":"user","message":{"role":"user","content":[{"tool_use_id":"toolu_01ADZH69nyRQcrqxThU63FTa","type":"tool_result","content":"(Bash completed with no output)","is_error":false}]},"parent_tool_use_id":null,"session_id":"db10d108","uuid":"21330463"}`

	// approval-fire.jsonl L18 — message_delta (structural no-op)
	fxMessageDelta = `{"type":"stream_event","event":{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"input_tokens":2819,"output_tokens":126}},"session_id":"9c5e1b44","parent_tool_use_id":null,"uuid":"26fc94fd"}`

	// Finalized assistant message whose sole content block is a TodoWrite tool_use.
	// input.todos[] carries {content,status,activeForm} per the Claude CLI shape:
	// one completed, one in_progress, one pending, plus one with an UNKNOWN status
	// (must normalize to "pending"). Order must be preserved.
	fxAssistantTodoWrite = `{"type":"assistant","message":{"model":"claude-opus-4-8","id":"msg_01TodoWriteAbc","type":"message","role":"assistant","content":[{"type":"tool_use","id":"toolu_01Todo","name":"TodoWrite","input":{"todos":[{"content":"Read the spec","status":"completed","activeForm":"Reading the spec"},{"content":"Wire the daemon","status":"in_progress","activeForm":"Wiring the daemon"},{"content":"Add tests","status":"pending","activeForm":"Adding tests"},{"content":"Ship it","status":"weird_future_status","activeForm":"Shipping it"}]}}],"usage":{"input_tokens":100,"cache_read_input_tokens":200,"output_tokens":50}},"parent_tool_use_id":null,"session_id":"todo-sess","uuid":"todo-uuid","request_id":"req_todo"}`

	// TodoWrite whose input is malformed for the todos schema (todos is a string,
	// not an array). The tool_call must still emit; NO todo event; no crash.
	fxAssistantTodoMalformed = `{"type":"assistant","message":{"model":"claude-opus-4-8","id":"msg_01TodoBad","type":"message","role":"assistant","content":[{"type":"tool_use","id":"toolu_01TodoBad","name":"TodoWrite","input":{"todos":"oops not an array"}}]}}`

	// Captured prompt_suggestion line — Claude's own model-generated next-prompt
	// suggestion (the gray ghost-text the interactive TUI shows, Tab to accept).
	// It is a BACKGROUND model call that lands ~10-20s AFTER result and not every
	// turn yields one. Requires the --prompt-suggestions argv flag. Text = the
	// suggested prompt.
	fxPromptSuggestion = `{"type":"prompt_suggestion","suggestion":"write me a helper to decode json bodies","uuid":"31fd396d-a26d-4edc-abf9-ddccbb91606a","session_id":"4db90ab9-f1a2-4574-ae0e-e15726a007e9"}`
)

// ev builds a NativeEvent for comparison. Helper to keep cases readable.
func raw(s string) json.RawMessage {
	if s == "" {
		return nil
	}
	return json.RawMessage(s)
}

func TestClaudeAdapterFeed(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		line string
		want []NativeEvent
	}{
		{
			name: "system/init",
			line: fxInit,
			want: []NativeEvent{{
				Type:           EvSessionInit,
				SessionID:      "286a3205-807e-4de4-840c-01f546cc8fa7",
				Model:          "claude-opus-4-8[1m]",
				Cwd:            "/tmp/tmp.263v7tmtnp",
				PermissionMode: "auto",
				Tools:          []string{"Task", "Bash", "Read"},
				MCPServers:     []any{map[string]any{"name": "playwright", "status": "pending"}},
				SlashCommands:  []string{"verify", "debug"},
			}},
		},
		{
			name: "system/status",
			line: fxStatus,
			want: []NativeEvent{{Type: EvStatus, Subtype: "requesting"}},
		},
		{
			name: "rate_limit_event",
			line: fxRateLimit,
			want: []NativeEvent{{Type: EvRateLimit, ResetsAt: 1781949600, RateLimitKind: "five_hour"}},
		},
		{
			name: "system/thinking_tokens -> usage",
			line: fxThinkingTokens,
			want: []NativeEvent{{Type: EvUsage, ThinkingTok: 50}},
		},
		{
			name: "thinking_delta empty -> dropped",
			line: fxThinkingDeltaEmpty,
			want: nil,
		},
		{
			name: "signature_delta -> thinking with signature only",
			line: fxSignatureDelta,
			want: []NativeEvent{{Type: EvThinking, Signature: "EqwDCmMIDhgC"}},
		},
		{
			name: "user replay echo -> user_text",
			line: fxUserReplay,
			want: []NativeEvent{{Type: EvUserText, Text: "Think step by step"}},
		},
		{
			name: "text_delta",
			line: fxTextDelta,
			want: []NativeEvent{{Type: EvTextDelta, Text: "**Answer: After 3 days.**"}},
		},
		{
			name: "assistant text + usage",
			line: fxAssistantText,
			want: []NativeEvent{
				{Type: EvText, Text: "**Answer: After 3 days.**", MessageID: "msg_01Qta4tisVy7nF1BoSnGgber"},
				{Type: EvUsage, InputTokens: 2964, CachedTokens: 15626, OutputTokens: 345},
			},
		},
		{
			name: "result success -> turn_end only",
			line: fxResultSuccess,
			want: []NativeEvent{{
				Type: EvTurnEnd, Subtype: "success", NumTurns: 1, DurationMs: 4903,
				TotalCostUSD: 0.051819, PermissionDenied: []string{},
			}},
		},
		{
			name: "content_block_start tool_use -> no-op",
			line: fxToolUseStart,
			want: nil,
		},
		{
			name: "input_json_delta -> dropped",
			line: fxInputJSONDelta,
			want: nil,
		},
		{
			name: "assistant tool_use -> tool_call + usage",
			line: fxAssistantToolUse,
			want: []NativeEvent{
				{
					Type: EvToolCall, CallID: "toolu_01EgPPcKVwkPTB6LRF52HJog", Name: "Bash",
					ToolKind: "exec", MessageID: "msg_01HkTF4RcEHUUJkzsS1TAddL",
					Args: raw(`{"command":"echo hi > /tmp/tmp.hJc98qZbec/probe.txt","description":"Write hi to probe.txt"}`),
				},
				{Type: EvUsage, InputTokens: 2819, CachedTokens: 15626, OutputTokens: 2},
			},
		},
		{
			name: "control_request can_use_tool -> permission_request",
			line: fxControlRequest,
			want: []NativeEvent{{
				Type:      EvPermissionRequest,
				RequestID: "019c298e-9707-43f3-bf84-5ac76a05883e",
				ToolName:  "Bash",
				CallID:    "toolu_01EgPPcKVwkPTB6LRF52HJog",
				Args:      raw(`{"command":"echo hi > /tmp/tmp.hJc98qZbec/probe.txt","description":"Write hi to probe.txt"}`),
				RawEvent:  raw(`{"subtype":"can_use_tool","tool_name":"Bash","display_name":"Bash","input":{"command":"echo hi > /tmp/tmp.hJc98qZbec/probe.txt","description":"Write hi to probe.txt"},"description":"Write hi to probe.txt","blocked_path":"/tmp/tmp.hJc98qZbec/probe.txt","tool_use_id":"toolu_01EgPPcKVwkPTB6LRF52HJog"}`),
			}},
		},
		{
			name: "user tool_result",
			line: fxToolResult,
			want: []NativeEvent{{
				Type: EvToolResult, CallID: "toolu_01ADZH69nyRQcrqxThU63FTa",
				IsError: false, Content: "(Bash completed with no output)",
			}},
		},
		{
			name: "message_delta -> no-op",
			line: fxMessageDelta,
			want: nil,
		},
		{
			name: "prompt_suggestion -> suggestion text",
			line: fxPromptSuggestion,
			want: []NativeEvent{{Type: EvPromptSuggestion, Text: "write me a helper to decode json bodies"}},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := NewClaudeAdapter()
			got := a.Feed([]byte(tc.line))
			assertEventsEqual(t, got, tc.want)
		})
	}
}

// TestPromptSuggestionDroppedWhenEmpty verifies an empty/whitespace-only
// suggestion emits nothing (no point surfacing a blank ghost-text) but does not
// crash. A real suggestion still passes through.
func TestPromptSuggestionDroppedWhenEmpty(t *testing.T) {
	t.Parallel()
	a := NewClaudeAdapter()
	if got := a.Feed([]byte(`{"type":"prompt_suggestion","suggestion":""}`)); got != nil {
		t.Fatalf("empty suggestion: want nil, got %+v", got)
	}
	if got := a.Feed([]byte(`{"type":"prompt_suggestion","suggestion":"   "}`)); got != nil {
		t.Fatalf("whitespace suggestion: want nil, got %+v", got)
	}
	got := a.Feed([]byte(fxPromptSuggestion))
	if len(got) != 1 || got[0].Type != EvPromptSuggestion || got[0].Text != "write me a helper to decode json bodies" {
		t.Fatalf("real suggestion: got %+v", got)
	}
}

// TestCompactionSuppressed locks in the /compact fix. The LIVE stream emits a
// compact_boundary system event, then replays the compaction summary and a
// "<local-command-stdout>Compacted" stub as user lines WITHOUT the
// isCompactSummary/isMeta flags the transcript carries — so the live path must
// recognize them by content. compact_boundary becomes a clean slash_result
// confirmation; the two scaffolding lines are dropped instead of leaking as
// user_text bubbles; a genuine human turn still passes through.
func TestCompactionSuppressed(t *testing.T) {
	t.Parallel()
	a := NewClaudeAdapter()

	got := a.Feed([]byte(`{"type":"system","subtype":"compact_boundary"}`))
	if len(got) != 1 || got[0].Type != EvSlashResult || !strings.Contains(got[0].Text, "compacted") {
		t.Fatalf("compact_boundary: got %+v, want one slash_result mentioning 'compacted'", got)
	}
	// Subtype "compact" tags the marker so the client resets the context-window
	// vital (no fresh usage event follows a compaction — see native_claude.go).
	if got[0].Subtype != "compact" {
		t.Fatalf("compact_boundary: subtype=%q, want %q (drives client ctx%% reset)", got[0].Subtype, "compact")
	}

	dropped := []struct {
		name string
		line string
	}{
		{"compaction summary (flags stripped live)", `{"type":"user","isReplay":false,"message":{"role":"user","content":"This session is being continued from a previous conversation that ran out of context. The summary below..."}}`},
		{"local-command-stdout stub", `{"type":"user","isReplay":true,"message":{"role":"user","content":"<local-command-stdout>Compacted </local-command-stdout>"}}`},
		{"command-name wrapper stub", `{"type":"user","message":{"role":"user","content":"<command-name>/compact</command-name>"}}`},
	}
	for _, d := range dropped {
		if got := a.Feed([]byte(d.line)); got != nil {
			t.Fatalf("%s: got %+v, want nil (dropped)", d.name, got)
		}
	}

	// A genuine human turn — even one that references a /path mid-sentence —
	// must still surface as user_text.
	got = a.Feed([]byte(`{"type":"user","message":{"role":"user","content":"check /etc/hosts and fix the bug"}}`))
	if len(got) != 1 || got[0].Type != EvUserText || got[0].Text != "check /etc/hosts and fix the bug" {
		t.Fatalf("human turn: got %+v, want one user_text passthrough", got)
	}
}

func TestCompactBoundaryEmitsPostCompactUsage(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		line string
		post int
	}{
		{
			name: "camelCase on-disk metadata",
			line: `{"type":"system","subtype":"compact_boundary","compactMetadata":{"preTokens":188000,"postTokens":9514}}`,
			post: 9514,
		},
		{
			name: "snake_case wire metadata",
			line: `{"type":"system","subtype":"compact_boundary","compact_metadata":{"pre_tokens":188000,"post_tokens":12743}}`,
			post: 12743,
		},
		{
			name: "older boundary without post tokens",
			line: `{"type":"system","subtype":"compact_boundary"}`,
		},
		{
			name: "zero post tokens keeps fallback",
			line: `{"type":"system","subtype":"compact_boundary","compactMetadata":{"postTokens":0}}`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := NewClaudeAdapter().Feed([]byte(tc.line))
			if len(got) == 0 || got[0].Type != EvSlashResult || got[0].Subtype != "compact" {
				t.Fatalf("events=%+v, want compact slash_result first", got)
			}
			if tc.post == 0 {
				if len(got) != 1 {
					t.Fatalf("events=%+v, want fallback marker only", got)
				}
				return
			}
			if len(got) != 2 || got[1].Type != EvUsage || got[1].InputTokens != tc.post {
				t.Fatalf("events=%+v, want trailing usage inputTokens=%d", got, tc.post)
			}
		})
	}
}

// TestTodoWriteEmitsTodoEvent verifies a TodoWrite assistant tool_use produces
// the normal tool_call (so its tool_result still pairs by id) AND an additional
// EvTodo with the normalized items: order preserved, Completed mirrors
// status=="completed", and an unknown status normalizes to "pending". The
// usage block emits its own EvUsage as usual.
func TestTodoWriteEmitsTodoEvent(t *testing.T) {
	t.Parallel()
	a := NewClaudeAdapter()
	got := a.Feed([]byte(fxAssistantTodoWrite))
	want := []NativeEvent{
		{
			Type: EvToolCall, CallID: "toolu_01Todo", Name: "TodoWrite",
			ToolKind: "todo", MessageID: "msg_01TodoWriteAbc",
			Args: raw(`{"todos":[{"content":"Read the spec","status":"completed","activeForm":"Reading the spec"},{"content":"Wire the daemon","status":"in_progress","activeForm":"Wiring the daemon"},{"content":"Add tests","status":"pending","activeForm":"Adding tests"},{"content":"Ship it","status":"weird_future_status","activeForm":"Shipping it"}]}`),
		},
		{
			Type: EvTodo,
			Items: []TodoItem{
				{Text: "Read the spec", Completed: true, Status: "completed"},
				{Text: "Wire the daemon", Completed: false, Status: "in_progress"},
				{Text: "Add tests", Completed: false, Status: "pending"},
				{Text: "Ship it", Completed: false, Status: "pending"}, // unknown -> pending
			},
		},
		{Type: EvUsage, InputTokens: 100, CachedTokens: 200, OutputTokens: 50},
	}
	assertEventsEqual(t, got, want)
}

// TestTodoWriteMalformedNoCrash verifies a TodoWrite whose input.todos is not an
// array still emits the tool_call but NO todo event, and does not crash.
func TestTodoWriteMalformedNoCrash(t *testing.T) {
	t.Parallel()
	a := NewClaudeAdapter()
	got := a.Feed([]byte(fxAssistantTodoMalformed))
	want := []NativeEvent{
		{
			Type: EvToolCall, CallID: "toolu_01TodoBad", Name: "TodoWrite",
			ToolKind: "todo", MessageID: "msg_01TodoBad",
			Args: raw(`{"todos":"oops not an array"}`),
		},
	}
	assertEventsEqual(t, got, want)
	for _, ev := range got {
		if ev.Type == EvTodo {
			t.Fatalf("malformed TodoWrite must not emit a todo event, got %+v", got)
		}
	}
}

// TestTodoItemsFromInput unit-tests the parser/normalizer directly, including
// the missing-status, empty, and no-todos edge cases.
func TestTodoItemsFromInput(t *testing.T) {
	t.Parallel()
	// Missing status normalizes to "pending"; order preserved.
	items := todoItemsFromInput(json.RawMessage(`{"todos":[{"content":"a"},{"content":"b","status":"in_progress"}]}`))
	want := []TodoItem{
		{Text: "a", Completed: false, Status: "pending"},
		{Text: "b", Completed: false, Status: "in_progress"},
	}
	if !reflect.DeepEqual(items, want) {
		t.Fatalf("todoItemsFromInput missing-status: got %+v, want %+v", items, want)
	}
	// Empty input, empty todos, and non-todos object all yield nil.
	if got := todoItemsFromInput(nil); got != nil {
		t.Errorf("nil input: want nil, got %+v", got)
	}
	if got := todoItemsFromInput(json.RawMessage(`{"todos":[]}`)); got != nil {
		t.Errorf("empty todos: want nil, got %+v", got)
	}
	if got := todoItemsFromInput(json.RawMessage(`{}`)); got != nil {
		t.Errorf("no todos key: want nil, got %+v", got)
	}
}

// TestResultErrorEmitsTwoEvents verifies an is_error result produces both a
// turn_end and an error event (in that order).
func TestResultErrorEmitsTwoEvents(t *testing.T) {
	t.Parallel()
	line := `{"type":"result","subtype":"error_during_execution","is_error":true,"num_turns":2,"result":"boom"}`
	a := NewClaudeAdapter()
	got := a.Feed([]byte(line))
	want := []NativeEvent{
		{Type: EvTurnEnd, Subtype: "error_during_execution", NumTurns: 2},
		{Type: EvError, Message: "boom", Code: "error_during_execution"},
	}
	assertEventsEqual(t, got, want)
}

// TestTextDeltaInheritsMessageID is the regression for the mid-word-split bug:
// streaming text_delta events must carry the message id captured from
// message_start, so the client coalesces a message's deltas into ONE block
// instead of rendering each chunk as its own paragraph (splitting words at
// chunk seams). message_stop clears it.
func TestTextDeltaInheritsMessageID(t *testing.T) {
	t.Parallel()
	a := NewClaudeAdapter()
	const start = `{"type":"stream_event","event":{"type":"message_start","message":{"id":"msg_ABC","role":"assistant"}}}`
	// Before any message_start, a stray delta has no id (nothing to inherit).
	if got := a.Feed([]byte(fxTextDelta)); len(got) != 1 || got[0].MessageID != "" {
		t.Fatalf("delta before message_start should have empty MessageID, got %+v", got)
	}
	if got := a.Feed([]byte(start)); len(got) != 0 {
		t.Fatalf("message_start should emit nothing, got %+v", got)
	}
	got := a.Feed([]byte(fxTextDelta))
	if len(got) != 1 || got[0].Type != EvTextDelta || got[0].MessageID != "msg_ABC" {
		t.Fatalf("text_delta after message_start must inherit msg_ABC, got %+v", got)
	}
	// message_stop clears it so the next message's deltas don't bleed over.
	const stop = `{"type":"stream_event","event":{"type":"message_stop"}}`
	a.Feed([]byte(stop))
	if got := a.Feed([]byte(fxTextDelta)); got[0].MessageID != "" {
		t.Fatalf("text_delta after message_stop should have empty MessageID, got %+v", got)
	}
}

// TestUnknownTypesDoNotCrash injects an unknown top-level type, an unknown
// system subtype, a control_cancel_request (known-handled), and a malformed
// line, asserting none crash and the unknown counter records the right keys.
func TestUnknownTypesDoNotCrash(t *testing.T) {
	t.Parallel()
	a := NewClaudeAdapter()

	// Unknown top-level type — dropped, counted.
	if got := a.Feed([]byte(`{"type":"brand_new_2027_event","foo":1}`)); got != nil {
		t.Fatalf("unknown top-level type: want nil, got %+v", got)
	}
	// Unknown system subtype — dropped, counted under "system/<sub>".
	if got := a.Feed([]byte(`{"type":"system","subtype":"future_subtype"}`)); got != nil {
		t.Fatalf("unknown system subtype: want nil, got %+v", got)
	}
	// control_cancel_request — KNOWN-handled, emits nothing, NOT counted as unknown.
	if got := a.Feed([]byte(`{"type":"control_cancel_request","request_id":"x"}`)); got != nil {
		t.Fatalf("control_cancel_request: want nil, got %+v", got)
	}
	// Malformed JSON — dropped, counted under "<malformed>".
	if got := a.Feed([]byte(`{not json at all`)); got != nil {
		t.Fatalf("malformed: want nil, got %+v", got)
	}

	counts := a.UnknownCounts()
	if counts["brand_new_2027_event"] != 1 {
		t.Errorf("brand_new_2027_event count = %d, want 1", counts["brand_new_2027_event"])
	}
	if counts["system/future_subtype"] != 1 {
		t.Errorf("system/future_subtype count = %d, want 1", counts["system/future_subtype"])
	}
	if counts["<malformed>"] != 1 {
		t.Errorf("<malformed> count = %d, want 1", counts["<malformed>"])
	}
	if _, ok := counts["control_cancel_request"]; ok {
		t.Errorf("control_cancel_request must NOT be counted as unknown")
	}
}

// TestUnknownLogOnceCounts verifies repeated unknowns of the same key keep
// counting (the log is rate-limited but the counter is not).
func TestUnknownLogOnceCounts(t *testing.T) {
	t.Parallel()
	a := NewClaudeAdapter()
	for i := 0; i < 5; i++ {
		a.Feed([]byte(`{"type":"weird"}`))
	}
	if c := a.UnknownCounts()["weird"]; c != 5 {
		t.Fatalf("weird count = %d, want 5", c)
	}
}

// referenceCarry is a reference cross-chunk line-carry decoder mirroring what
// the parser goroutine (pane_native.go) does: buffer bytes, split on '\n', emit
// complete lines, carry the trailing partial fragment to the next chunk. It is
// implemented HERE (not in the adapter) because line-carry is the parser
// goroutine's job; this lets the test prove Feed's output is invariant under
// arbitrary chunk boundaries.
type referenceCarry struct {
	buf []byte
}

// push appends a chunk and returns any newly-complete lines (no trailing '\n').
func (c *referenceCarry) push(chunk []byte) [][]byte {
	c.buf = append(c.buf, chunk...)
	var lines [][]byte
	for {
		i := indexByte(c.buf, '\n')
		if i < 0 {
			break
		}
		line := make([]byte, i)
		copy(line, c.buf[:i])
		lines = append(lines, line)
		c.buf = c.buf[i+1:]
	}
	return lines
}

func indexByte(b []byte, target byte) int {
	for i := 0; i < len(b); i++ {
		if b[i] == target {
			return i
		}
	}
	return -1
}

// TestLineCarrySplitAtEveryOffset feeds a multi-line stream split at EVERY byte
// offset through the reference carry decoder + adapter, and asserts the produced
// NativeEvent sequence is identical regardless of where the chunk boundary falls.
// This is the chunked-stream-stripper-class test: partial-line bytes must never
// corrupt or drop an event.
func TestLineCarrySplitAtEveryOffset(t *testing.T) {
	t.Parallel()
	// A realistic multi-line stream (newline-terminated, as the PTY delivers it).
	streamLines := []string{
		fxInit,
		fxStatus,
		fxThinkingTokens,
		fxSignatureDelta,
		fxTextDelta,
		fxAssistantText,
		fxAssistantToolUse,
		fxControlRequest,
		fxToolResult,
		fxResultSuccess,
	}
	stream := []byte(strings.Join(streamLines, "\n") + "\n")

	// Baseline: feed each complete line directly.
	baseAdapter := NewClaudeAdapter()
	var baseline []NativeEvent
	for _, l := range streamLines {
		baseline = append(baseline, baseAdapter.Feed([]byte(l))...)
	}
	if len(baseline) == 0 {
		t.Fatal("baseline produced no events — fixture/mapping bug")
	}

	for split := 0; split <= len(stream); split++ {
		a := NewClaudeAdapter()
		carry := &referenceCarry{}
		var got []NativeEvent

		first := stream[:split]
		second := stream[split:]
		for _, line := range carry.push(first) {
			got = append(got, a.Feed(line)...)
		}
		for _, line := range carry.push(second) {
			got = append(got, a.Feed(line)...)
		}
		if len(carry.buf) != 0 {
			t.Fatalf("split=%d: carry buffer not drained: %q", split, carry.buf)
		}
		assertEventsEqualf(t, got, baseline, "split="+strconv.Itoa(split))
	}
}

// TestLineCarryEveryByteChunked is the maximal-stress variant: deliver the whole
// stream ONE BYTE AT A TIME and assert identical output. This exercises the
// carry buffer's cross-call accumulation at every boundary simultaneously.
func TestLineCarryEveryByteChunked(t *testing.T) {
	t.Parallel()
	streamLines := []string{fxInit, fxStatus, fxAssistantToolUse, fxControlRequest, fxResultSuccess}
	stream := []byte(strings.Join(streamLines, "\n") + "\n")

	baseAdapter := NewClaudeAdapter()
	var baseline []NativeEvent
	for _, l := range streamLines {
		baseline = append(baseline, baseAdapter.Feed([]byte(l))...)
	}

	a := NewClaudeAdapter()
	carry := &referenceCarry{}
	var got []NativeEvent
	for i := 0; i < len(stream); i++ {
		for _, line := range carry.push(stream[i : i+1]) {
			got = append(got, a.Feed(line)...)
		}
	}
	if len(carry.buf) != 0 {
		t.Fatalf("carry buffer not drained: %q", carry.buf)
	}
	assertEventsEqual(t, got, baseline)
}

// TestEncoders verifies the exact stdin frame shapes (byte-grounded).
func TestEncoders(t *testing.T) {
	t.Parallel()
	a := NewClaudeAdapter()

	// User turn.
	ut := a.EncodeUserTurn("hello world")
	if ut[len(ut)-1] != '\n' {
		t.Fatal("user turn must be newline-terminated")
	}
	var utm map[string]any
	if err := json.Unmarshal(ut[:len(ut)-1], &utm); err != nil {
		t.Fatalf("user turn not valid json: %v", err)
	}
	if utm["type"] != "user" {
		t.Errorf("user turn type = %v", utm["type"])
	}
	msg := utm["message"].(map[string]any)
	if msg["role"] != "user" || msg["content"] != "hello world" {
		t.Errorf("user turn message = %v", msg)
	}

	// Approval allow: updatedInput echoes verbatim, inner key is camelCase toolUseID
	// and snake request_id.
	input := json.RawMessage(`{"command":"echo hi"}`)
	al := a.EncodeApproval("req-1", "tuid-1", true, input, "")
	var alm map[string]any
	if err := json.Unmarshal(al[:len(al)-1], &alm); err != nil {
		t.Fatalf("approval not valid json: %v", err)
	}
	if alm["type"] != "control_response" {
		t.Errorf("approval type = %v", alm["type"])
	}
	resp := alm["response"].(map[string]any)
	if resp["subtype"] != "success" {
		t.Errorf("approval subtype = %v", resp["subtype"])
	}
	if resp["request_id"] != "req-1" {
		t.Errorf("approval request_id = %v (must be snake_case)", resp["request_id"])
	}
	inner := resp["response"].(map[string]any)
	if inner["behavior"] != "allow" {
		t.Errorf("allow behavior = %v", inner["behavior"])
	}
	if inner["toolUseID"] != "tuid-1" {
		t.Errorf("allow toolUseID = %v (must be camelCase ID)", inner["toolUseID"])
	}
	ui, _ := json.Marshal(inner["updatedInput"])
	if string(ui) != `{"command":"echo hi"}` {
		t.Errorf("updatedInput must echo input verbatim, got %s", ui)
	}

	// Approval deny: carries message, no updatedInput.
	dn := a.EncodeApproval("req-2", "tuid-2", false, nil, "not allowed")
	var dnm map[string]any
	_ = json.Unmarshal(dn[:len(dn)-1], &dnm)
	dresp := dnm["response"].(map[string]any)["response"].(map[string]any)
	if dresp["behavior"] != "deny" {
		t.Errorf("deny behavior = %v", dresp["behavior"])
	}
	if dresp["message"] != "not allowed" {
		t.Errorf("deny message = %v", dresp["message"])
	}
	if _, has := dresp["updatedInput"]; has {
		t.Errorf("deny must not carry updatedInput")
	}

	// Interrupt: fresh request_id, subtype interrupt.
	it := a.EncodeInterrupt()
	var itm map[string]any
	if err := json.Unmarshal(it[:len(it)-1], &itm); err != nil {
		t.Fatalf("interrupt not valid json: %v", err)
	}
	if itm["type"] != "control_request" {
		t.Errorf("interrupt type = %v", itm["type"])
	}
	if itm["request_id"] == "" || itm["request_id"] == nil {
		t.Error("interrupt must carry a fresh non-empty request_id")
	}
	if itm["request"].(map[string]any)["subtype"] != "interrupt" {
		t.Errorf("interrupt subtype = %v", itm["request"])
	}
	// Two interrupts must not collide.
	it2 := a.EncodeInterrupt()
	var itm2 map[string]any
	_ = json.Unmarshal(it2[:len(it2)-1], &itm2)
	if itm["request_id"] == itm2["request_id"] {
		t.Error("interrupt request_id must be unique per call")
	}
}

func TestClassifyTool(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"Bash":              "exec",
		"Edit":              "file",
		"Write":             "file",
		"Read":              "file",
		"WebSearch":         "search",
		"TodoWrite":         "todo",
		"mcp__render__list": "mcp",
		"SomethingNew":      "exec",
	}
	for name, want := range cases {
		if got := classifyTool(name); got != want {
			t.Errorf("classifyTool(%q) = %q, want %q", name, got, want)
		}
	}
}

// assertEventsEqual compares two NativeEvent slices for deep equality, treating
// nil and empty slices as equal and normalizing json.RawMessage by semantic
// JSON equality (so whitespace/key-order differences in Args/RawEvent don't
// cause false failures).
func assertEventsEqual(t *testing.T, got, want []NativeEvent) {
	t.Helper()
	assertEventsEqualf(t, got, want, "")
}

func assertEventsEqualf(t *testing.T, got, want []NativeEvent, ctx string) {
	t.Helper()
	prefix := ""
	if ctx != "" {
		prefix = ctx + ": "
	}
	if len(got) != len(want) {
		t.Fatalf("%sevent count: got %d, want %d\n got=%+v\nwant=%+v", prefix, len(got), len(want), got, want)
	}
	for i := range want {
		g, w := got[i], want[i]
		// Compare raw-message fields semantically, then blank them for the
		// structural reflect.DeepEqual.
		if !jsonEqual(g.Args, w.Args) {
			t.Errorf("%sevent[%d].Args: got %s, want %s", prefix, i, g.Args, w.Args)
		}
		if !jsonEqual(g.RawEvent, w.RawEvent) {
			t.Errorf("%sevent[%d].RawEvent: got %s, want %s", prefix, i, g.RawEvent, w.RawEvent)
		}
		if !jsonEqual(g.Output, w.Output) {
			t.Errorf("%sevent[%d].Output: got %s, want %s", prefix, i, g.Output, w.Output)
		}
		g.Args, w.Args = nil, nil
		g.RawEvent, w.RawEvent = nil, nil
		g.Output, w.Output = nil, nil
		if !reflect.DeepEqual(g, w) {
			t.Errorf("%sevent[%d]: \n got=%+v\nwant=%+v", prefix, i, g, w)
		}
	}
}

// jsonEqual reports whether two json.RawMessages are semantically equal
// (whitespace/key-order independent). Both empty => equal.
func jsonEqual(a, b json.RawMessage) bool {
	if len(a) == 0 && len(b) == 0 {
		return true
	}
	if len(a) == 0 || len(b) == 0 {
		return false
	}
	var av, bv any
	if err := json.Unmarshal(a, &av); err != nil {
		return false
	}
	if err := json.Unmarshal(b, &bv); err != nil {
		return false
	}
	return reflect.DeepEqual(av, bv)
}
