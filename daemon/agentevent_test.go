package main

import (
	"encoding/json"
	"sort"
	"testing"
)

// agentevent_test.go is the DAEMON-SIDE golden for the native-renderer event
// schema (the types in agentevent.go). Its cross-language counterpart is
// web/src/native/schemaParity.test.ts, which locks the SAME EventType value set
// and the SAME wire-key sets on the TS NativeEvent mirror. The two goldens sit
// next to each other on purpose: a rename/removal on EITHER side breaks its own
// golden, and a human keeps the two expected sets aligned.
//
// Why this exists: agentevent.go and nativeEvents.ts are hand-kept-in-sync, and
// two drifts have already shipped (the `todo` event and the `localMsgId`
// field). These tests make a future EventType rename/removal or a `json:` tag
// rename fail CI loudly instead of silently breaking one client.
//
// If you intentionally change the schema: update BOTH goldens (here and in
// schemaParity.test.ts) in the same change, on purpose.

// expectedEventTypes is the authoritative set of EventType wire VALUES. Keep in
// lockstep with the `EVENT_TYPES` set in schemaParity.test.ts.
var expectedEventTypes = map[string]bool{
	"session_init":        true,
	"text_delta":          true,
	"text":                true,
	"user_text":           true,
	"thinking_delta":      true,
	"thinking":            true,
	"tool_call":           true,
	"tool_result":         true,
	"todo":                true,
	"permission_request":  true,
	"permission_resolved": true,
	"usage":               true,
	"rate_limit":          true,
	"status":              true,
	"turn_end":            true,
	"error":               true,
	"slash_result":        true,
	"prompt_suggestion":   true,
}

// allEventTypes is every EventType const declared in agentevent.go. This list IS
// the test surface: adding a new EventType const without adding it here (and to
// expectedEventTypes + the TS golden) is the drift we want to catch. If you add
// a const in agentevent.go, add it here too.
var allEventTypes = []EventType{
	EvSessionInit,
	EvTextDelta,
	EvText,
	EvUserText,
	EvThinkingDelta,
	EvThinking,
	EvToolCall,
	EvToolResult,
	EvTodo,
	EvPermissionRequest,
	EvPermissionResolved,
	EvUsage,
	EvRateLimit,
	EvStatus,
	EvTurnEnd,
	EvError,
	EvSlashResult,
	EvPromptSuggestion,
}

func TestEventTypeValuesMatchGolden(t *testing.T) {
	t.Parallel()
	got := map[string]bool{}
	for _, et := range allEventTypes {
		s := string(et)
		if got[s] {
			t.Errorf("duplicate EventType value %q in allEventTypes", s)
		}
		got[s] = true
	}

	// every declared const must be expected
	for s := range got {
		if !expectedEventTypes[s] {
			t.Errorf("EventType %q is declared but NOT in expectedEventTypes golden — "+
				"if this is intentional, update expectedEventTypes here AND the EVENT_TYPES "+
				"set in web/src/native/schemaParity.test.ts", s)
		}
	}
	// every expected value must still be declared
	for s := range expectedEventTypes {
		if !got[s] {
			t.Errorf("EventType %q is in the golden but NO LONGER declared in agentevent.go — "+
				"a rename/removal? update both goldens (here and schemaParity.test.ts)", s)
		}
	}

	if len(got) != len(expectedEventTypes) {
		t.Errorf("EventType count drift: declared=%d golden=%d", len(got), len(expectedEventTypes))
	}
}

// expectedNativeEventKeys is the authoritative set of WIRE KEYS produced by
// marshaling a fully-populated NativeEvent. These are the json: tag names from
// agentevent.go, including the renames that differ from the Go field name
// (cachedInputTokens, reasoningTokens, totalCostUsd, permissionDenials). Keep in
// lockstep with NATIVE_EVENT_KEYS in schemaParity.test.ts.
var expectedNativeEventKeys = []string{
	// envelope (no omitempty — always present)
	"seq", "paneId", "type", "fromReplay",
	// session_init
	"sessionId", "model", "cwd", "permissionMode", "tools", "mcpServers", "slashCommands",
	// text / text_delta / thinking / thinking_delta / user_text
	"text", "messageId", "signature",
	// tool_call
	"callId", "toolKind", "name", "args", "parentCallId",
	// tool_result
	"isError", "content", "output",
	// todo
	"items",
	// permission_request
	"requestId", "toolName", "rawEvent", "wouldAutoAllow",
	// usage / rate_limit / thinking_tokens
	"inputTokens", "cachedInputTokens", "cacheCreationTokens", "outputTokens", "reasoningTokens",
	"totalTokens", "contextWindowTokens",
	"totalCostUsd", "resetsAt", "rateLimitKind",
	// status / turn_end / error
	"subtype", "detail", "numTurns", "durationMs", "permissionDenials", "message", "code",
}

func TestNativeEventJSONKeysMatchGolden(t *testing.T) {
	t.Parallel()
	// Populate EVERY field with a non-zero representative value so omitempty does
	// not drop any key. A json: tag rename then surfaces as an unexpected key
	// (new name) plus a missing key (old golden name).
	ev := NativeEvent{
		Seq:        1,
		PaneID:     "p1",
		Type:       EvText,
		FromReplay: true,

		SessionID:      "s1",
		Model:          "claude",
		Cwd:            "/tmp",
		PermissionMode: "auto",
		Tools:          []string{"Bash"},
		MCPServers:     []any{"x"},
		SlashCommands:  []string{"/clear"},

		Text:      "hi",
		MessageID: "m1",
		Signature: "sig",

		CallID:       "c1",
		ToolKind:     "exec",
		Name:         "Bash",
		Args:         json.RawMessage(`{"a":1}`),
		ParentCallID: "pc1",

		IsError: true,
		Content: "out",
		Output:  json.RawMessage(`{"stdout":"x"}`),

		Items: []TodoItem{{Text: "do", Completed: true, Status: "completed"}},

		RequestID:      "r1",
		ToolName:       "Bash",
		RawEvent:       json.RawMessage(`{}`),
		WouldAutoAllow: true,

		InputTokens:         1,
		CachedTokens:        2,
		CacheCreationTokens: 3,
		OutputTokens:        4,
		ThinkingTok:         5,
		TotalTokens:         6,
		ContextWindowTokens: 7,
		TotalCostUSD:        0.5,
		ResetsAt:            123,
		RateLimitKind:       "primary",

		Subtype:          "success",
		Detail:           "d",
		NumTurns:         2,
		DurationMs:       10,
		PermissionDenied: []string{"Bash"},
		Message:          "boom",
		Code:             "E1",
	}

	b, err := json.Marshal(ev)
	if err != nil {
		t.Fatalf("marshal NativeEvent: %v", err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("unmarshal NativeEvent: %v", err)
	}

	got := map[string]bool{}
	for k := range m {
		got[k] = true
	}
	want := map[string]bool{}
	for _, k := range expectedNativeEventKeys {
		if want[k] {
			t.Errorf("duplicate key %q in expectedNativeEventKeys golden", k)
		}
		want[k] = true
	}

	for k := range got {
		if !want[k] {
			t.Errorf("NativeEvent produced wire key %q NOT in golden — a json: tag "+
				"rename/addition? update expectedNativeEventKeys here AND "+
				"NATIVE_EVENT_KEYS in web/src/native/schemaParity.test.ts", k)
		}
	}
	for k := range want {
		if !got[k] {
			t.Errorf("golden expects wire key %q but marshaled NativeEvent did NOT "+
				"produce it — a json: tag rename/removal (or a zero value tripping "+
				"omitempty)? update both goldens", k)
		}
	}

	if len(got) != len(want) {
		keys := make([]string, 0, len(got))
		for k := range got {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		t.Errorf("NativeEvent key count drift: produced=%d golden=%d; produced keys=%v",
			len(got), len(want), keys)
	}
}

// expectedTodoItemKeys is the wire-key golden for the nested TodoItem (the todo
// event payload). Keep in lockstep with TODO_ITEM_KEYS in schemaParity.test.ts.
// Note: Completed and Status carry omitempty, so we populate both with non-zero
// values to force every key out.
var expectedTodoItemKeys = []string{"text", "completed", "status"}

func TestTodoItemJSONKeysMatchGolden(t *testing.T) {
	t.Parallel()
	it := TodoItem{Text: "do", Completed: true, Status: "in_progress"}
	b, err := json.Marshal(it)
	if err != nil {
		t.Fatalf("marshal TodoItem: %v", err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("unmarshal TodoItem: %v", err)
	}

	got := map[string]bool{}
	for k := range m {
		got[k] = true
	}
	want := map[string]bool{}
	for _, k := range expectedTodoItemKeys {
		want[k] = true
	}
	for k := range got {
		if !want[k] {
			t.Errorf("TodoItem produced wire key %q NOT in golden — update "+
				"expectedTodoItemKeys here AND TODO_ITEM_KEYS in schemaParity.test.ts", k)
		}
	}
	for k := range want {
		if !got[k] {
			t.Errorf("golden expects TodoItem wire key %q but it was not produced", k)
		}
	}
}
