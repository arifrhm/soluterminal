package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func codexFixtureLines(t *testing.T, b []byte) []map[string]any {
	t.Helper()
	var out []map[string]any
	s := bufio.NewScanner(bytes.NewReader(b))
	for s.Scan() {
		var line map[string]any
		if err := json.Unmarshal(s.Bytes(), &line); err != nil {
			t.Fatalf("bad encoded frame: %v", err)
		}
		out = append(out, line)
	}
	if err := s.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func codexResponse(t *testing.T, a *CodexAdapter, id any, result string) []NativeEvent {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"id": id, "result": json.RawMessage(result)})
	if err != nil {
		t.Fatal(err)
	}
	return a.Feed(raw)
}

func codexReadyFixture(t *testing.T, threadID string) *CodexAdapter {
	t.Helper()
	a := NewCodexAdapter()
	init := codexFixtureLines(t, a.Bootstrap("/work", threadID, "gpt-5.3-codex", "high", ""))
	if len(init) != 1 || init[0]["method"] != "initialize" {
		t.Fatalf("bootstrap=%v", init)
	}
	initID, ok := init[0]["id"].(string)
	if !ok || !strings.HasPrefix(initID, "solu-") {
		t.Fatalf("initialize id=%v", init[0]["id"])
	}
	if events := codexResponse(t, a, initID, `{"userAgent":"codex_cli_rs/0.144.1"}`); len(events) != 0 {
		t.Fatalf("initialize must not mark pane in-turn: %+v", events)
	}
	next := codexFixtureLines(t, a.DrainOutbound())
	if len(next) != 2 || next[0]["method"] != "initialized" {
		t.Fatalf("post-initialize=%v", next)
	}
	wantMethod := "thread/resume"
	if threadID == "" {
		wantMethod = "thread/start"
		threadID = "thr-01"
	}
	if next[1]["method"] != wantMethod {
		t.Fatalf("thread request=%v", next[1])
	}
	if wantMethod == "thread/resume" && next[1]["params"].(map[string]any)["excludeTurns"] != true {
		t.Fatalf("thread/resume must defer history to bounded thread/read: %v", next[1])
	}
	events := codexResponse(t, a, next[1]["id"], `{"thread":{"id":"`+threadID+`","cwd":"/work","turns":[]},"model":"gpt-5.3-codex","cwd":"/work","approvalPolicy":"on-request","approvalsReviewer":"user","modelProvider":"openai","sandbox":{}}`)
	if len(events) != 1 || events[0].Type != EvSessionInit || events[0].SessionID != threadID {
		t.Fatalf("thread response=%+v", events)
	}
	if !a.Ready() || a.ThreadID() != threadID {
		t.Fatalf("ready=%v thread=%q", a.Ready(), a.ThreadID())
	}
	return a
}

func TestCodexBootstrapCarriesDeveloperInstructions(t *testing.T) {
	for _, tt := range []struct {
		name, threadID, instructions, wantMethod string
		wantInstructions                         bool
	}{
		{name: "start", instructions: orchestratorBrief, wantMethod: "thread/start", wantInstructions: true},
		{name: "resume", threadID: "thr-01", instructions: orchestratorBrief, wantMethod: "thread/resume", wantInstructions: true},
		{name: "plain start", wantMethod: "thread/start"},
		{name: "plain resume", threadID: "thr-01", wantMethod: "thread/resume"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			a := NewCodexAdapter()
			init := codexFixtureLines(t, a.Bootstrap("/work", tt.threadID, "m", "high", tt.instructions))[0]
			_ = codexResponse(t, a, init["id"], `{}`)
			frames := codexFixtureLines(t, a.DrainOutbound())
			if len(frames) != 2 || frames[1]["method"] != tt.wantMethod {
				t.Fatalf("bootstrap frames = %#v", frames)
			}
			params := frames[1]["params"].(map[string]any)
			got, present := params["developerInstructions"]
			if present != tt.wantInstructions || (present && got != orchestratorBrief) {
				t.Fatalf("developerInstructions = %#v present=%v", got, present)
			}
			if tt.wantMethod == "thread/resume" && params["excludeTurns"] != true {
				t.Fatal("resume lost excludeTurns")
			}
		})
	}
}

func TestCodexResponseDrivenBootstrapAndTurnCorrelation(t *testing.T) {
	t.Parallel()
	a := codexReadyFixture(t, "")
	if history := a.ReadThread(); len(history) != 0 {
		t.Fatalf("fresh unmaterialized thread requested includeTurns history: %q", history)
	}
	if drained := a.DrainOutbound(); len(drained) != 0 {
		t.Fatalf("outbound was not one-shot: %q", drained)
	}
	turnFrame := codexFixtureLines(t, a.EncodeUserTurn("hello"))
	if len(turnFrame) != 1 || turnFrame[0]["method"] != "turn/start" {
		t.Fatalf("turn/start=%v", turnFrame)
	}
	if turnFrame[0]["params"].(map[string]any)["threadId"] != "thr-01" {
		t.Fatalf("turn params=%v", turnFrame[0])
	}
	turn := codexResponse(t, a, turnFrame[0]["id"], `{"turn":{"id":"turn-01","status":"inProgress","items":[]}}`)
	if len(turn) != 1 || turn[0].Subtype != "requesting" || turn[0].Detail != "" || a.TurnID() != "turn-01" {
		t.Fatalf("turn response=%+v id=%q", turn, a.TurnID())
	}
	steer := codexFixtureLines(t, a.EncodeUserTurn("also check tests"))
	sp := steer[0]["params"].(map[string]any)
	if steer[0]["method"] != "turn/steer" || sp["expectedTurnId"] != "turn-01" {
		t.Fatalf("steer=%v", steer)
	}
	interrupt := codexFixtureLines(t, a.EncodeInterrupt())
	ip := interrupt[0]["params"].(map[string]any)
	if interrupt[0]["method"] != "turn/interrupt" || ip["threadId"] != "thr-01" || ip["turnId"] != "turn-01" {
		t.Fatalf("interrupt=%v", interrupt)
	}
}

func TestCodexResumedBootstrapCanReadMaterializedHistory(t *testing.T) {
	t.Parallel()
	a := codexReadyFixture(t, "thr-existing")
	request := codexFixtureLines(t, a.ReadThread())
	if len(request) != 1 || request[0]["method"] != "thread/read" {
		t.Fatalf("resumed thread history request = %v", request)
	}
}

func TestCodexCompactUsesDedicatedRPCAndMapsAuthoritativeCompletion(t *testing.T) {
	t.Parallel()
	if got := NewCodexAdapter().EncodeCompact(); got != nil {
		t.Fatalf("compact encoded before bootstrap: %q", got)
	}

	a := codexReadyFixture(t, "thr-compact")
	request := codexFixtureLines(t, a.EncodeCompact())
	if len(request) != 1 || request[0]["method"] != "thread/compact/start" {
		t.Fatalf("compact request=%v", request)
	}
	params := request[0]["params"].(map[string]any)
	if !reflect.DeepEqual(params, map[string]any{"threadId": "thr-compact"}) {
		t.Fatalf("compact params=%v", params)
	}
	if events := codexResponse(t, a, request[0]["id"], `{}`); len(events) != 0 {
		t.Fatalf("compact acknowledgement must wait for lifecycle completion: %+v", events)
	}

	_ = a.Feed([]byte(`{"method":"turn/started","params":{"threadId":"thr-compact","turn":{"id":"turn-compact","status":"inProgress","items":[]}}}`))
	if got := a.EncodeCompact(); got != nil {
		t.Fatalf("second compact encoded during active turn: %q", got)
	}
	if events := a.Feed([]byte(`{"method":"item/started","params":{"threadId":"thr-compact","turnId":"turn-compact","item":{"id":"compact-1","type":"contextCompaction"}}}`)); len(events) != 0 {
		t.Fatalf("contextCompaction start=%+v", events)
	}
	completed := a.Feed([]byte(`{"method":"item/completed","params":{"threadId":"thr-compact","turnId":"turn-compact","item":{"id":"compact-1","type":"contextCompaction"}}}`))
	if len(completed) != 1 || completed[0].Type != EvSlashResult || completed[0].Subtype != "compact" {
		t.Fatalf("contextCompaction completion=%+v", completed)
	}
	// Current app-server versions may also emit this deprecated notification;
	// it must neither duplicate the transcript row nor re-trigger footer reset.
	if duplicate := a.Feed([]byte(`{"method":"thread/compacted","params":{"threadId":"thr-compact","turnId":"turn-compact"}}`)); len(duplicate) != 0 {
		t.Fatalf("deprecated compact duplicate=%+v", duplicate)
	}
	if foreign := a.Feed([]byte(`{"method":"thread/compacted","params":{"threadId":"thr-other","turnId":"turn-compact"}}`)); len(foreign) != 0 {
		t.Fatalf("foreign compact leaked=%+v", foreign)
	}
}

func TestCodexNotificationsMapLifecycleAndRejectForeignCorrelation(t *testing.T) {
	t.Parallel()
	a := codexReadyFixture(t, "thr-01")
	started := a.Feed([]byte(`{"method":"turn/started","params":{"threadId":"thr-01","turn":{"id":"turn-01","status":"inProgress","items":[]}}}`))
	if len(started) != 1 || started[0].Detail != "" || a.TurnID() != "turn-01" {
		t.Fatalf("turn started=%+v", started)
	}
	fixtures := []struct {
		name, line string
		want       []EventType
	}{
		{"agent delta", `{"method":"item/agentMessage/delta","params":{"threadId":"thr-01","turnId":"turn-01","itemId":"msg-1","delta":"Hello"}}`, []EventType{EvTextDelta}},
		{"agent complete", `{"method":"item/completed","params":{"threadId":"thr-01","turnId":"turn-01","completedAtMs":1,"item":{"id":"msg-1","type":"agentMessage","text":"Hello"}}}`, []EventType{EvText}},
		{"reasoning delta", `{"method":"item/reasoning/summaryTextDelta","params":{"threadId":"thr-01","turnId":"turn-01","itemId":"why-1","delta":"Checking"}}`, []EventType{EvThinkingDelta}},
		{"command start", `{"method":"item/started","params":{"threadId":"thr-01","turnId":"turn-01","startedAtMs":1,"item":{"id":"cmd-1","type":"commandExecution","command":"go test ./...","cwd":"/work","status":"inProgress","commandActions":[]}}}`, []EventType{EvToolCall}},
		{"command output nonterminal", `{"method":"item/commandExecution/outputDelta","params":{"threadId":"thr-01","turnId":"turn-01","itemId":"cmd-1","delta":"ok\n"}}`, []EventType{}},
		{"command complete", `{"method":"item/completed","params":{"threadId":"thr-01","turnId":"turn-01","completedAtMs":2,"item":{"id":"cmd-1","type":"commandExecution","command":"go test ./...","cwd":"/work","status":"completed","aggregatedOutput":"ok\n","exitCode":0,"commandActions":[]}}}`, []EventType{EvToolResult}},
		{"file start", `{"method":"item/started","params":{"threadId":"thr-01","turnId":"turn-01","startedAtMs":3,"item":{"id":"patch-1","type":"fileChange","status":"inProgress","changes":[{"path":"a.go","kind":"update","diff":"+x"}]}}}`, []EventType{EvToolCall}},
		{"patch update nonterminal", `{"method":"item/fileChange/patchUpdated","params":{"threadId":"thr-01","turnId":"turn-01","itemId":"patch-1","changes":[]}}`, []EventType{}},
		{"file complete", `{"method":"item/completed","params":{"threadId":"thr-01","turnId":"turn-01","completedAtMs":4,"item":{"id":"patch-1","type":"fileChange","status":"completed","changes":[]}}}`, []EventType{EvToolResult}},
		{"mcp start", `{"method":"item/started","params":{"threadId":"thr-01","turnId":"turn-01","startedAtMs":5,"item":{"id":"mcp-1","type":"mcpToolCall","server":"docs","tool":"search","arguments":{"q":"x"},"status":"inProgress"}}}`, []EventType{EvToolCall}},
		{"mcp progress nonterminal", `{"method":"item/mcpToolCall/progress","params":{"threadId":"thr-01","turnId":"turn-01","itemId":"mcp-1","message":"working"}}`, []EventType{}},
		{"mcp complete", `{"method":"item/completed","params":{"threadId":"thr-01","turnId":"turn-01","completedAtMs":6,"item":{"id":"mcp-1","type":"mcpToolCall","server":"docs","tool":"search","arguments":{},"status":"completed","result":{"content":[]}}}}`, []EventType{EvToolResult}},
		{"plan", `{"method":"turn/plan/updated","params":{"threadId":"thr-01","turnId":"turn-01","plan":[{"step":"Inspect","status":"completed"},{"step":"Test","status":"inProgress"}]}}`, []EventType{EvTodo}},
		{"turn diff nonterminal", `{"method":"turn/diff/updated","params":{"threadId":"thr-01","turnId":"turn-01","diff":"diff"}}`, []EventType{}},
		{"usage", `{"method":"thread/tokenUsage/updated","params":{"threadId":"thr-01","turnId":"turn-01","tokenUsage":{"last":{"inputTokens":10,"cachedInputTokens":3,"outputTokens":4,"reasoningOutputTokens":2,"totalTokens":14},"total":{"inputTokens":10,"cachedInputTokens":3,"outputTokens":4,"reasoningOutputTokens":2,"totalTokens":14}}}}`, []EventType{EvUsage}},
		{"retry error nonterminal", `{"method":"error","params":{"threadId":"thr-01","turnId":"turn-01","willRetry":true,"error":{"message":"temporary"}}}`, []EventType{}},
		{"fatal error", `{"method":"error","params":{"threadId":"thr-01","turnId":"turn-01","willRetry":false,"error":{"message":"boom","codexErrorInfo":"sandboxError"}}}`, []EventType{EvError}},
		{"warning nonterminal", `{"method":"warning","params":{"message":"heads up"}}`, []EventType{}},
	}
	for _, tc := range fixtures {
		t.Run(tc.name, func(t *testing.T) {
			events := a.Feed([]byte(tc.line))
			got := make([]EventType, len(events))
			for i := range events {
				got[i] = events[i].Type
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("events=%+v, want %v", events, tc.want)
			}
		})
	}
	if got := a.Feed([]byte(`{"method":"item/agentMessage/delta","params":{"threadId":"thr-other","turnId":"turn-01","itemId":"x","delta":"leak"}}`)); len(got) != 0 {
		t.Fatalf("foreign=%+v", got)
	}
	if got := a.Feed([]byte(`{"method":"item/agentMessage/delta","params":{"threadId":"thr-01","turnId":"turn-other","itemId":"x","delta":"leak"}}`)); len(got) != 0 {
		t.Fatalf("wrong turn=%+v", got)
	}
	completed := a.Feed([]byte(`{"method":"turn/completed","params":{"threadId":"thr-01","turn":{"id":"turn-01","status":"completed","durationMs":27,"items":[]}}}`))
	if len(completed) != 1 || completed[0].Type != EvTurnEnd || completed[0].Subtype != "success" || a.TurnID() != "" {
		t.Fatalf("completed=%+v turn=%q", completed, a.TurnID())
	}
	if a.EncodeInterrupt() != nil {
		t.Fatal("interrupt should be nil without an active turn")
	}
}

func TestCodexFooterVitalsUseAuthoritativeUsageAndRateLimit(t *testing.T) {
	t.Parallel()
	a := codexReadyFixture(t, "thr-01")
	_ = a.Feed([]byte(`{"method":"turn/started","params":{"threadId":"thr-01","turn":{"id":"turn-01","status":"inProgress","items":[]}}}`))

	usage := a.Feed([]byte(`{"method":"thread/tokenUsage/updated","params":{"threadId":"thr-01","turnId":"turn-01","tokenUsage":{"last":{"inputTokens":145467,"cachedInputTokens":137984,"outputTokens":217,"reasoningOutputTokens":38,"totalTokens":145684},"modelContextWindow":258400}}}`))
	if len(usage) != 1 || usage[0].Type != EvUsage {
		t.Fatalf("usage events=%+v", usage)
	}
	got := usage[0]
	if got.InputTokens != 145467 || got.CachedTokens != 137984 || got.OutputTokens != 217 || got.ThinkingTok != 38 || got.TotalTokens != 145684 || got.ContextWindowTokens != 258400 {
		t.Fatalf("usage=%+v", got)
	}

	rate := a.Feed([]byte(`{"method":"account/rateLimits/updated","params":{"rateLimits":{"limitId":"codex","primary":{"usedPercent":33,"windowDurationMins":10080,"resetsAt":1784487562},"secondary":{"usedPercent":12,"windowDurationMins":300,"resetsAt":1784499999}}}}`))
	if len(rate) != 1 || rate[0].Type != EvRateLimit || rate[0].ResetsAt != 1784487562 || rate[0].RateLimitKind != "Codex weekly limit" {
		t.Fatalf("rate limit=%+v", rate)
	}
}

func TestCodexApprovalEchoesOriginalJSONRPCID(t *testing.T) {
	t.Parallel()
	a := codexReadyFixture(t, "thr-01")
	_ = a.Feed([]byte(`{"method":"turn/started","params":{"threadId":"thr-01","turn":{"id":"turn-01","status":"inProgress","items":[]}}}`))
	approval := a.Feed([]byte(`{"id":"approval-7","method":"item/commandExecution/requestApproval","params":{"threadId":"thr-01","turnId":"turn-01","itemId":"cmd-7","startedAtMs":12,"command":"rm generated.tmp","cwd":"/work"}}`))
	if len(approval) != 1 || approval[0].RequestID != "approval-7" || approval[0].CallID != "cmd-7" {
		t.Fatalf("approval=%+v", approval)
	}
	allowed := codexFixtureLines(t, a.EncodeApproval(approval[0].RequestID, approval[0].CallID, true, approval[0].Args, ""))
	if allowed[0]["id"] != "approval-7" || allowed[0]["result"].(map[string]any)["decision"] != "accept" {
		t.Fatalf("allow=%v", allowed)
	}
	if retry := a.EncodeApproval("approval-7", "cmd-7", true, nil, ""); len(retry) == 0 {
		t.Fatal("approval correlation must survive until the transport commits the write")
	}
	a.CommitApproval("approval-7")
	numeric := a.Feed([]byte(`{"id":42,"method":"item/fileChange/requestApproval","params":{"threadId":"thr-01","turnId":"turn-01","itemId":"patch-42","startedAtMs":13}}`))
	denied := codexFixtureLines(t, a.EncodeApproval(numeric[0].RequestID, numeric[0].CallID, false, nil, "no"))
	if denied[0]["id"] != float64(42) || denied[0]["result"].(map[string]any)["decision"] != "decline" {
		t.Fatalf("deny=%v", denied)
	}
	if a.EncodeApproval("approval-7", "cmd-7", true, nil, "") != nil {
		t.Fatal("approval response must be one-shot")
	}
}

func TestCodexRestoreThreadAndUnknownInputs(t *testing.T) {
	t.Parallel()
	a := NewCodexAdapter()
	if a.EncodeUserTurn("too early") != nil {
		t.Fatal("turn encoded before restore")
	}
	a.RestoreThread("thr-restored", "/restored", "gpt-5.3-codex")
	if !a.Ready() || a.ThreadID() != "thr-restored" {
		t.Fatalf("restore ready=%v thread=%q", a.Ready(), a.ThreadID())
	}
	turn := codexFixtureLines(t, a.EncodeUserTurn("continue"))
	if turn[0]["method"] != "turn/start" || turn[0]["params"].(map[string]any)["threadId"] != "thr-restored" {
		t.Fatalf("restored=%v", turn)
	}
	if got := a.Feed([]byte(`not-json`)); got != nil {
		t.Fatalf("malformed=%+v", got)
	}
	if got := a.Feed([]byte(`{"id":"unknown","result":{}}`)); got != nil {
		t.Fatalf("unmatched=%+v", got)
	}
	if got := a.Feed([]byte(`{"id":"foreign","error":{"code":-32603,"message":"not ours"}}`)); got != nil {
		t.Fatalf("unmatched error must not terminate this pane: %+v", got)
	}
	counts := a.UnknownCounts()
	if counts["<malformed>"] != 1 || counts["response/<unmatched>"] != 2 {
		t.Fatalf("counts=%v", counts)
	}
}

func TestCodexThreadReadMapsReplayWithoutLiveStateEvents(t *testing.T) {
	t.Parallel()
	a := NewCodexAdapter()
	if got := a.ReadThread(); got != nil {
		t.Fatalf("thread/read before restore = %q", got)
	}
	a.RestoreThread("thr-history", "/work", "gpt-5.3-codex")
	request := codexFixtureLines(t, a.ReadThread())
	if len(request) != 1 || request[0]["method"] != "thread/read" {
		t.Fatalf("thread/read request=%v", request)
	}
	params := request[0]["params"].(map[string]any)
	if params["threadId"] != "thr-history" || params["includeTurns"] != true {
		t.Fatalf("thread/read params=%v", params)
	}

	result := `{
		"thread":{"id":"thr-history","turns":[
			{"id":"turn-old-1","status":"completed","durationMs":41,"items":[
				{"id":"user-1","type":"userMessage","content":[{"type":"text","text":"Run the checks"}]},
				{"id":"reason-1","type":"reasoning","summary":["I should test first"],"content":[]},
				{"id":"cmd-1","type":"commandExecution","command":"go test ./...","cwd":"/work","status":"completed","aggregatedOutput":"ok\n","exitCode":0,"commandActions":[]},
				{"id":"msg-1","type":"agentMessage","text":"All checks pass."}
			]},
			{"id":"turn-old-2","status":"failed","durationMs":9,"error":{"message":"historical failure"},"items":[
				{"id":"user-2","type":"userMessage","content":[{"type":"text","text":"Try the release"}]},
				{"id":"msg-2","type":"agentMessage","text":"The release could not complete."}
			]}
		]}}
	`
	events := codexResponse(t, a, request[0]["id"], result)
	wantTypes := []EventType{
		EvUserText, EvThinking, EvToolCall, EvToolResult, EvText, EvTurnEnd,
		EvUserText, EvText, EvTurnEnd,
	}
	gotTypes := make([]EventType, len(events))
	for i, ev := range events {
		gotTypes[i] = ev.Type
		if !ev.FromReplay {
			t.Fatalf("event[%d] was not replay-marked: %+v", i, ev)
		}
		if ev.Type == EvStatus || ev.Type == EvError || ev.Type == EvPermissionRequest {
			t.Fatalf("historical response emitted actionable/live state event: %+v", ev)
		}
	}
	if !reflect.DeepEqual(gotTypes, wantTypes) {
		t.Fatalf("replay types=%v, want %v; events=%+v", gotTypes, wantTypes, events)
	}
	if events[0].MessageID != "user-1" || events[1].MessageID != "reason-1" ||
		events[2].CallID != "cmd-1" || events[3].CallID != "cmd-1" || events[4].MessageID != "msg-1" {
		t.Fatalf("replay lost stable item ids: %+v", events[:5])
	}
	if events[5].Subtype != "success" || events[8].Subtype != "error" {
		t.Fatalf("turn boundaries=%+v / %+v", events[5], events[8])
	}
}

func TestCodexThreadReadRejectsMismatchedThreadResponse(t *testing.T) {
	t.Parallel()
	a := NewCodexAdapter()
	a.RestoreThread("thr-owned", "/work", "gpt-5.3-codex")
	request := codexFixtureLines(t, a.ReadThread())
	events := codexResponse(t, a, request[0]["id"], `{"thread":{"id":"thr-foreign","turns":[{"id":"turn-x","status":"completed","items":[{"id":"msg-x","type":"agentMessage","text":"must not leak"}]}]}}`)
	if len(events) != 0 {
		t.Fatalf("foreign thread/read leaked events: %+v", events)
	}
}
