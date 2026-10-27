package main

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"sync"
)

// CodexAdapter maps the Codex app-server JSON-RPC protocol to NativeEvent.
// One adapter belongs to one pane/thread. Feed and the encoders may be called
// by different goroutines, so the small amount of correlation state is locked.
type CodexAdapter struct {
	mu sync.Mutex

	pending     map[string]string          // canonical JSON-RPC id -> client method
	approvalIDs map[string]json.RawMessage // UI request id -> original server request id
	outbound    []byte
	bootstrap   struct{ cwd, threadID, model, effort, developerInstructions string }

	initialized bool
	threadID    string
	turnID      string
	model       string
	cwd         string
	permission  string
	sessionSent bool
	// historyReadable is false for a brand-new thread/start until its first user
	// turn materializes the rollout. Asking thread/read(includeTurns) during that
	// window is a Codex protocol error even though the thread already accepts
	// turn/start. Resumed/restored threads are materialized and safe to read.
	historyReadable bool
	// compactResultTurnID deduplicates the current contextCompaction item and
	// the deprecated thread/compacted notification when Codex emits both for the
	// same compaction turn.
	compactResultTurnID string

	unknown map[string]uint64
	logged  map[string]bool
}

func NewCodexAdapter() *CodexAdapter {
	return &CodexAdapter{
		pending:     make(map[string]string),
		approvalIDs: make(map[string]json.RawMessage),
		unknown:     make(map[string]uint64), logged: make(map[string]bool),
	}
}

var _ NativeAdapter = (*CodexAdapter)(nil)

const (
	codexReplayMaxTurns  = 200
	codexReplayMaxEvents = 5000
)

func (a *CodexAdapter) SupportsInteractiveApproval() bool { return true }

// Bootstrap starts the response-driven app-server handshake. The initialized
// notification and thread request are queued only after initialize succeeds;
// callers retrieve them with DrainOutbound.
func (a *CodexAdapter) Bootstrap(cwd, threadID, model, effort, developerInstructions string) []byte {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.cwd, a.model = cwd, model
	a.historyReadable = strings.TrimSpace(threadID) != ""
	a.bootstrap.cwd, a.bootstrap.threadID = cwd, threadID
	a.bootstrap.model, a.bootstrap.effort = model, effort
	a.bootstrap.developerInstructions = developerInstructions
	return a.requestLocked("initialize", map[string]any{
		"clientInfo":   map[string]any{"name": "soluterminal", "title": "SOLU Terminal", "version": "1"},
		"capabilities": map[string]any{"experimentalApi": true},
	})
}

// DrainOutbound returns response-triggered frames exactly once.
func (a *CodexAdapter) DrainOutbound() []byte {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := append([]byte(nil), a.outbound...)
	a.outbound = nil
	return out
}

func (a *CodexAdapter) Ready() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.initialized && a.threadID != ""
}

func (a *CodexAdapter) ThreadID() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.threadID
}

// ReadThread requests a bounded semantic history seed for the active thread.
// It is safe after either a normal bootstrap or RestoreThread. Its response is
// mapped to FromReplay events and never drives live pane state.
func (a *CodexAdapter) ReadThread() []byte {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.threadID == "" || !a.historyReadable {
		return nil
	}
	return a.requestLocked("thread/read", map[string]any{"threadId": a.threadID, "includeTurns": true})
}

// RestoreThread seeds correlation when reattaching to a surviving app-server.
func (a *CodexAdapter) RestoreThread(threadID, cwd, model string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.initialized = true
	a.threadID, a.cwd, a.model = threadID, cwd, model
	a.sessionSent = true
	a.historyReadable = true
}

func (a *CodexAdapter) TurnID() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.turnID
}

func (a *CodexAdapter) UnknownCounts() map[string]uint64 {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make(map[string]uint64, len(a.unknown))
	for k, v := range a.unknown {
		out[k] = v
	}
	return out
}

func (a *CodexAdapter) dropUnknownLocked(key string) []NativeEvent {
	a.unknown[key]++
	if !a.logged[key] {
		a.logged[key] = true
		log.Printf("native(codex): dropping unknown event %q (logging once)", key)
	}
	return nil
}

type codexRPCLine struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Code    json.RawMessage `json:"code"`
		Message string          `json:"message"`
		Data    json.RawMessage `json:"data"`
	} `json:"error"`
}

func (a *CodexAdapter) Feed(line []byte) []NativeEvent {
	var rpc codexRPCLine
	if json.Unmarshal(line, &rpc) != nil {
		a.mu.Lock()
		defer a.mu.Unlock()
		return a.dropUnknownLocked("<malformed>")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if rpc.Method != "" {
		if len(rpc.ID) != 0 && string(rpc.ID) != "null" {
			return a.mapServerRequestLocked(&rpc)
		}
		return a.mapNotificationLocked(rpc.Method, rpc.Params)
	}
	if len(rpc.ID) != 0 {
		return a.mapResponseLocked(&rpc)
	}
	return a.dropUnknownLocked("<empty>")
}

func (a *CodexAdapter) mapResponseLocked(rpc *codexRPCLine) []NativeEvent {
	key := codexRPCIDKey(rpc.ID)
	method, ok := a.pending[key]
	if ok {
		delete(a.pending, key)
	}
	if !ok {
		return a.dropUnknownLocked("response/<unmatched>")
	}
	if rpc.Error != nil {
		// A failed steer/interrupt is not a failed model turn. Emitting EvError
		// would settle the active turn in generic pane bookkeeping.
		if method == "turn/steer" || method == "turn/interrupt" {
			return nil
		}
		return []NativeEvent{{Type: EvError, Message: rpc.Error.Message, Code: codexRawString(rpc.Error.Code), Detail: string(rpc.Error.Data)}}
	}
	switch method {
	case "initialize":
		a.initialized = true
		a.outbound = append(a.outbound, codexMarshalLine(map[string]any{"method": "initialized"})...)
		params := map[string]any{"cwd": a.bootstrap.cwd}
		if a.bootstrap.model != "" {
			params["model"] = a.bootstrap.model
		}
		if a.bootstrap.effort != "" {
			params["reasoningEffort"] = a.bootstrap.effort
		}
		// codex app-server generate-json-schema lists developerInstructions on
		// both ThreadStartParams and ThreadResumeParams. An ephemeral start probe
		// on codex-cli 0.149.1 accepted it and returned a normal thread id.
		if a.bootstrap.developerInstructions != "" {
			params["developerInstructions"] = a.bootstrap.developerInstructions
		}
		if a.bootstrap.threadID == "" {
			params["approvalPolicy"], params["sandbox"] = "on-request", "workspace-write"
			a.outbound = append(a.outbound, a.requestLocked("thread/start", params)...)
		} else {
			params["threadId"] = a.bootstrap.threadID
			params["excludeTurns"] = true
			a.outbound = append(a.outbound, a.requestLocked("thread/resume", params)...)
		}
		return nil
	case "thread/start", "thread/resume":
		return a.mapThreadResponseLocked(rpc.Result)
	case "thread/read":
		return a.mapThreadReadResponseLocked(rpc.Result)
	case "turn/start":
		var result struct {
			Turn struct {
				ID     string `json:"id"`
				Status string `json:"status"`
			} `json:"turn"`
		}
		_ = json.Unmarshal(rpc.Result, &result)
		if result.Turn.ID != "" {
			a.turnID = result.Turn.ID
		}
		// Keep the turn id in adapter correlation state, not in the user-facing
		// status detail. NativePane uses Detail as the metabar label; leaking the
		// opaque UUID there painted "019f..." instead of a useful "Thinking…".
		return []NativeEvent{{Type: EvStatus, Subtype: codexStatus(result.Turn.Status)}}
	case "turn/steer":
		return nil
	case "turn/interrupt":
		return []NativeEvent{{Type: EvStatus, Subtype: "interrupting"}}
	default:
		return nil
	}
}

func (a *CodexAdapter) mapThreadResponseLocked(raw json.RawMessage) []NativeEvent {
	var result struct {
		Thread struct {
			ID  string `json:"id"`
			Cwd string `json:"cwd"`
		} `json:"thread"`
		Model          string          `json:"model"`
		Cwd            string          `json:"cwd"`
		ApprovalPolicy json.RawMessage `json:"approvalPolicy"`
	}
	if json.Unmarshal(raw, &result) != nil || result.Thread.ID == "" {
		return []NativeEvent{{Type: EvError, Message: "Codex returned a thread response without a thread id", Code: "invalid_thread_response"}}
	}
	a.threadID, a.model = result.Thread.ID, result.Model
	if result.Cwd != "" {
		a.cwd = result.Cwd
	}
	a.permission = codexRawString(result.ApprovalPolicy)
	if a.sessionSent {
		return nil
	}
	a.sessionSent = true
	return []NativeEvent{{Type: EvSessionInit, SessionID: a.threadID, Model: a.model, Cwd: a.cwd, PermissionMode: a.permission}}
}

func (a *CodexAdapter) mapThreadReadResponseLocked(raw json.RawMessage) []NativeEvent {
	var result struct {
		Thread struct {
			ID    string `json:"id"`
			Turns []struct {
				ID         string            `json:"id"`
				Status     string            `json:"status"`
				DurationMs int               `json:"durationMs"`
				Items      []json.RawMessage `json:"items"`
			} `json:"turns"`
		} `json:"thread"`
	}
	if json.Unmarshal(raw, &result) != nil || result.Thread.ID == "" || result.Thread.ID != a.threadID {
		return nil
	}
	turns := result.Thread.Turns
	if len(turns) > codexReplayMaxTurns {
		turns = turns[len(turns)-codexReplayMaxTurns:]
	}
	chunks := make([][]NativeEvent, 0, len(turns))
	total := 0
	for _, turn := range turns {
		var chunk []NativeEvent
		for _, item := range turn.Items {
			chunk = append(chunk, a.mapReplayItemLocked(item)...)
		}
		// An in-progress historical turn has no terminal boundary yet. Its
		// completed items are still safe display history, but must not alter live
		// state; FromReplay below provides that guarantee.
		if turn.Status != "inProgress" {
			chunk = append(chunk, NativeEvent{
				Type: EvTurnEnd, Subtype: codexTurnSubtype(turn.Status),
				DurationMs: turn.DurationMs, Detail: turn.ID,
			})
		}
		for i := range chunk {
			chunk[i].FromReplay = true
		}
		if len(chunk) > codexReplayMaxEvents {
			// Keep the first event (normally the user prompt) plus the newest
			// tail for a pathological single huge turn.
			bounded := make([]NativeEvent, 0, codexReplayMaxEvents)
			bounded = append(bounded, chunk[0])
			bounded = append(bounded, chunk[len(chunk)-(codexReplayMaxEvents-1):]...)
			chunk = bounded
		}
		chunks = append(chunks, chunk)
		total += len(chunk)
		for total > codexReplayMaxEvents && len(chunks) > 1 {
			total -= len(chunks[0])
			chunks = chunks[1:]
		}
	}
	out := make([]NativeEvent, 0, total)
	for _, chunk := range chunks {
		out = append(out, chunk...)
	}
	return out
}

func (a *CodexAdapter) mapReplayItemLocked(raw json.RawMessage) []NativeEvent {
	var item struct {
		ID      string          `json:"id"`
		Type    string          `json:"type"`
		Content json.RawMessage `json:"content"`
	}
	if json.Unmarshal(raw, &item) != nil || item.ID == "" {
		return nil
	}
	if item.Type == "userMessage" {
		return []NativeEvent{{Type: EvUserText, Text: codexUserText(item.Content), MessageID: item.ID}}
	}
	wrapper, _ := json.Marshal(map[string]json.RawMessage{"item": raw})
	var out []NativeEvent
	// Persisted tools are terminal items, but the normalized renderer needs both
	// lifecycle halves so tool results pair with stable call ids.
	switch item.Type {
	case "commandExecution", "fileChange", "mcpToolCall", "dynamicToolCall", "webSearch":
		out = append(out, a.mapItemLocked(wrapper, false)...)
		out = append(out, a.mapItemLocked(wrapper, true)...)
	default:
		out = append(out, a.mapItemLocked(wrapper, true)...)
	}
	return out
}

func (a *CodexAdapter) mapServerRequestLocked(rpc *codexRPCLine) []NativeEvent {
	switch rpc.Method {
	case "item/commandExecution/requestApproval", "item/fileChange/requestApproval":
		var p struct {
			ThreadID string          `json:"threadId"`
			TurnID   string          `json:"turnId"`
			ItemID   string          `json:"itemId"`
			Command  *string         `json:"command"`
			Cwd      json.RawMessage `json:"cwd"`
			Reason   *string         `json:"reason"`
		}
		if json.Unmarshal(rpc.Params, &p) != nil || !a.correlatesLocked(p.ThreadID, p.TurnID, false) {
			return nil
		}
		requestID := codexRPCIDDisplay(rpc.ID)
		a.approvalIDs[requestID] = append(json.RawMessage(nil), rpc.ID...)
		name := "ApplyPatch"
		args := rpc.Params
		if rpc.Method == "item/commandExecution/requestApproval" {
			name = "Bash"
			arg, _ := json.Marshal(map[string]any{"command": p.Command, "cwd": p.Cwd, "reason": p.Reason})
			args = arg
		}
		return []NativeEvent{{Type: EvPermissionRequest, RequestID: requestID, CallID: p.ItemID, ToolName: name, Args: args, RawEvent: rpc.Params}}
	default:
		return a.dropUnknownLocked(rpc.Method)
	}
}

func (a *CodexAdapter) mapNotificationLocked(method string, raw json.RawMessage) []NativeEvent {
	var corr struct {
		ThreadID string `json:"threadId"`
		TurnID   string `json:"turnId"`
		ItemID   string `json:"itemId"`
		Delta    string `json:"delta"`
	}
	_ = json.Unmarshal(raw, &corr)
	needsCorrelation := (method != "thread/started" && strings.HasPrefix(method, "item/")) ||
		strings.HasPrefix(method, "turn/") || method == "thread/tokenUsage/updated" || method == "error"
	if needsCorrelation {
		if !a.correlatesLocked(corr.ThreadID, corr.TurnID, method == "turn/started") {
			return nil
		}
	}
	switch method {
	case "thread/started":
		var p struct {
			Thread struct {
				ID  string `json:"id"`
				Cwd string `json:"cwd"`
			} `json:"thread"`
		}
		_ = json.Unmarshal(raw, &p)
		if a.threadID == "" {
			a.threadID = p.Thread.ID
		}
		return nil
	case "turn/started":
		var p struct {
			ThreadID string `json:"threadId"`
			Turn     struct {
				ID     string `json:"id"`
				Status string `json:"status"`
			} `json:"turn"`
		}
		_ = json.Unmarshal(raw, &p)
		if p.ThreadID != a.threadID {
			return nil
		}
		a.turnID = p.Turn.ID
		return []NativeEvent{{Type: EvStatus, Subtype: "requesting"}}
	case "item/agentMessage/delta":
		return []NativeEvent{{Type: EvTextDelta, Text: corr.Delta, MessageID: corr.ItemID}}
	case "item/reasoning/textDelta", "item/reasoning/summaryTextDelta":
		return []NativeEvent{{Type: EvThinkingDelta, Text: corr.Delta, MessageID: corr.ItemID}}
	case "item/reasoning/summaryPartAdded":
		return nil
	case "item/started":
		return a.mapItemLocked(raw, false)
	case "item/completed":
		return a.mapItemLocked(raw, true)
	case "item/commandExecution/outputDelta", "item/fileChange/outputDelta":
		return nil
	case "item/fileChange/patchUpdated":
		return nil
	case "item/mcpToolCall/progress":
		return nil
	case "turn/plan/updated":
		var p struct {
			Plan []struct{ Step, Status string } `json:"plan"`
		}
		_ = json.Unmarshal(raw, &p)
		items := make([]TodoItem, 0, len(p.Plan))
		for _, step := range p.Plan {
			status := "pending"
			if step.Status == "inProgress" {
				status = "in_progress"
			} else if step.Status == "completed" {
				status = "completed"
			}
			items = append(items, TodoItem{Text: step.Step, Status: status, Completed: status == "completed"})
		}
		return []NativeEvent{{Type: EvTodo, Items: items}}
	case "turn/diff/updated":
		return nil
	case "thread/tokenUsage/updated":
		// Explicit field names avoid coupling the normalized schema to Codex names.
		var wire struct {
			TokenUsage struct {
				Last struct {
					Total     int `json:"totalTokens"`
					Input     int `json:"inputTokens"`
					Cached    int `json:"cachedInputTokens"`
					Output    int `json:"outputTokens"`
					Reasoning int `json:"reasoningOutputTokens"`
				} `json:"last"`
				ModelContextWindow int `json:"modelContextWindow"`
			} `json:"tokenUsage"`
		}
		_ = json.Unmarshal(raw, &wire)
		return []NativeEvent{{
			Type:        EvUsage,
			InputTokens: wire.TokenUsage.Last.Input, CachedTokens: wire.TokenUsage.Last.Cached,
			OutputTokens: wire.TokenUsage.Last.Output, ThinkingTok: wire.TokenUsage.Last.Reasoning,
			TotalTokens: wire.TokenUsage.Last.Total, ContextWindowTokens: wire.TokenUsage.ModelContextWindow,
		}}
	case "account/rateLimits/updated":
		// Codex publishes real reset epochs separately from token usage. Preserve
		// the earliest active window so the existing user-controlled "Resets"
		// footer field shows actionable data instead of staying blank.
		var p struct {
			RateLimits struct {
				LimitID   string `json:"limitId"`
				LimitName string `json:"limitName"`
				Primary   *struct {
					WindowDurationMins int   `json:"windowDurationMins"`
					ResetsAt           int64 `json:"resetsAt"`
				} `json:"primary"`
				Secondary *struct {
					WindowDurationMins int   `json:"windowDurationMins"`
					ResetsAt           int64 `json:"resetsAt"`
				} `json:"secondary"`
			} `json:"rateLimits"`
		}
		_ = json.Unmarshal(raw, &p)
		type rateWindow struct {
			mins  int
			reset int64
		}
		var windows []rateWindow
		if p.RateLimits.Primary != nil {
			windows = append(windows, rateWindow{p.RateLimits.Primary.WindowDurationMins, p.RateLimits.Primary.ResetsAt})
		}
		if p.RateLimits.Secondary != nil {
			windows = append(windows, rateWindow{p.RateLimits.Secondary.WindowDurationMins, p.RateLimits.Secondary.ResetsAt})
		}
		var resetsAt int64
		var windowMins int
		for _, win := range windows {
			if win.reset > 0 && (resetsAt == 0 || win.reset < resetsAt) {
				resetsAt, windowMins = win.reset, win.mins
			}
		}
		if resetsAt == 0 {
			return nil
		}
		return []NativeEvent{{
			Type: EvRateLimit, ResetsAt: resetsAt,
			RateLimitKind: codexRateLimitKind(p.RateLimits.LimitID, p.RateLimits.LimitName, windowMins),
		}}
	case "error":
		var p struct {
			Error struct {
				Message    string          `json:"message"`
				Additional string          `json:"additionalDetails"`
				Info       json.RawMessage `json:"codexErrorInfo"`
			} `json:"error"`
			WillRetry bool `json:"willRetry"`
		}
		_ = json.Unmarshal(raw, &p)
		if p.WillRetry {
			return nil
		}
		code := codexRawString(p.Error.Info)
		return []NativeEvent{{Type: EvError, Message: p.Error.Message, Detail: p.Error.Additional, Code: code}}
	case "turn/completed":
		var p struct {
			Turn struct {
				ID         string `json:"id"`
				Status     string `json:"status"`
				DurationMs int    `json:"durationMs"`
				Error      *struct {
					Message string          `json:"message"`
					Info    json.RawMessage `json:"codexErrorInfo"`
				} `json:"error"`
			} `json:"turn"`
		}
		_ = json.Unmarshal(raw, &p)
		if a.turnID != "" && p.Turn.ID != a.turnID {
			return nil
		}
		end := NativeEvent{Type: EvTurnEnd, Subtype: codexTurnSubtype(p.Turn.Status), DurationMs: p.Turn.DurationMs, Detail: p.Turn.ID}
		a.turnID = ""
		clear(a.approvalIDs)
		if p.Turn.Status == "failed" && p.Turn.Error != nil {
			return []NativeEvent{end, {Type: EvError, Message: p.Turn.Error.Message, Code: codexRawString(p.Turn.Error.Info)}}
		}
		return []NativeEvent{end}
	case "thread/compacted":
		// Deprecated in favour of the contextCompaction item, but older/current
		// app-server builds may still publish it. Validate the thread without
		// requiring a currently-active turn: this notification can trail
		// turn/completed, which has already cleared a.turnID.
		if corr.ThreadID == "" || corr.ThreadID != a.threadID {
			return nil
		}
		return a.compactResultLocked(corr.TurnID)
	case "warning", "guardianWarning", "deprecationNotice", "configWarning":
		return nil
	default:
		return a.dropUnknownLocked(method)
	}
}

func (a *CodexAdapter) correlatesLocked(threadID, turnID string, adoptTurn bool) bool {
	if a.threadID == "" || threadID == "" || threadID != a.threadID {
		return false
	}
	if turnID == "" {
		return true
	}
	if a.turnID == "" && adoptTurn {
		a.turnID = turnID
	}
	return a.turnID != "" && turnID == a.turnID
}

func (a *CodexAdapter) mapItemLocked(raw json.RawMessage, completed bool) []NativeEvent {
	var p struct {
		Item json.RawMessage `json:"item"`
	}
	if json.Unmarshal(raw, &p) != nil {
		return nil
	}
	var item struct {
		ID, Type, Text, Status, Command, Cwd, AggregatedOutput, Server, Tool, Query string
		Content                                                                     json.RawMessage `json:"content"`
		Summary                                                                     []string        `json:"summary"`
		Changes                                                                     json.RawMessage `json:"changes"`
		Arguments                                                                   json.RawMessage `json:"arguments"`
		Result                                                                      json.RawMessage `json:"result"`
		Error                                                                       json.RawMessage `json:"error"`
		ExitCode                                                                    *int            `json:"exitCode"`
	}
	if json.Unmarshal(p.Item, &item) != nil || item.ID == "" {
		return nil
	}
	switch item.Type {
	case "userMessage":
		if !completed {
			return []NativeEvent{{Type: EvUserText, Text: codexUserText(item.Content), MessageID: item.ID}}
		}
	case "agentMessage":
		if completed {
			return []NativeEvent{{Type: EvText, Text: item.Text, MessageID: item.ID}}
		}
	case "reasoning":
		if completed {
			return []NativeEvent{{Type: EvThinking, Text: strings.Join(append(item.Summary, codexStringList(item.Content)...), "\n"), MessageID: item.ID}}
		}
	case "contextCompaction":
		if completed {
			return a.compactResultLocked(a.turnID)
		}
	case "commandExecution":
		if !completed {
			args, _ := json.Marshal(map[string]any{"command": item.Command, "cwd": item.Cwd})
			return []NativeEvent{{Type: EvToolCall, CallID: item.ID, ToolKind: "exec", Name: "Bash", Args: args}}
		}
		return []NativeEvent{{Type: EvToolResult, CallID: item.ID, Content: item.AggregatedOutput, Output: p.Item, IsError: item.Status == "failed" || item.Status == "declined" || (item.ExitCode != nil && *item.ExitCode != 0)}}
	case "fileChange":
		if !completed {
			return []NativeEvent{{Type: EvToolCall, CallID: item.ID, ToolKind: "file", Name: "ApplyPatch", Args: item.Changes}}
		}
		return []NativeEvent{{Type: EvToolResult, CallID: item.ID, Content: string(item.Changes), Output: p.Item, IsError: item.Status == "failed" || item.Status == "declined"}}
	case "mcpToolCall":
		if !completed {
			return []NativeEvent{{Type: EvToolCall, CallID: item.ID, ToolKind: "mcp", Name: item.Server + "/" + item.Tool, Args: item.Arguments}}
		}
		return []NativeEvent{{Type: EvToolResult, CallID: item.ID, Content: codexRawString(item.Result), Output: p.Item, IsError: item.Status == "failed"}}
	case "dynamicToolCall":
		if !completed {
			return []NativeEvent{{Type: EvToolCall, CallID: item.ID, ToolKind: "mcp", Name: item.Tool, Args: item.Arguments}}
		}
		return []NativeEvent{{Type: EvToolResult, CallID: item.ID, Content: codexRawString(item.Result), Output: p.Item, IsError: item.Status == "failed"}}
	case "webSearch":
		if !completed {
			args, _ := json.Marshal(map[string]string{"query": item.Query})
			return []NativeEvent{{Type: EvToolCall, CallID: item.ID, ToolKind: "search", Name: "WebSearch", Args: args}}
		}
		return []NativeEvent{{Type: EvToolResult, CallID: item.ID, Output: p.Item}}
	}
	return nil
}

func (a *CodexAdapter) EncodeUserTurn(text string) []byte {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.threadID == "" {
		return nil
	}
	params := map[string]any{"threadId": a.threadID, "input": []any{map[string]any{"type": "text", "text": text}}}
	if a.turnID != "" {
		params["expectedTurnId"] = a.turnID
		return a.requestLocked("turn/steer", params)
	}
	return a.requestLocked("turn/start", params)
}

// EncodeCompact invokes Codex app-server's real thread compaction operation.
// Sending "/compact" through EncodeUserTurn merely asks the model to discuss
// compaction; it does not alter the thread context. Compaction is an idle-thread
// control operation, so reject it until bootstrap has completed and while a
// model turn is active.
func (a *CodexAdapter) EncodeCompact() []byte {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.threadID == "" || a.turnID != "" {
		return nil
	}
	return a.requestLocked("thread/compact/start", map[string]any{"threadId": a.threadID})
}

func (a *CodexAdapter) compactResultLocked(turnID string) []NativeEvent {
	if turnID != "" {
		if a.compactResultTurnID == turnID {
			return nil
		}
		a.compactResultTurnID = turnID
	}
	return []NativeEvent{{
		Type: EvSlashResult, Subtype: "compact",
		Text: "✓ Earlier conversation compacted into a summary to free up context.",
	}}
}

func (a *CodexAdapter) EncodeApproval(requestID, _ string, allow bool, _ json.RawMessage, _ string) []byte {
	a.mu.Lock()
	defer a.mu.Unlock()
	id, ok := a.approvalIDs[requestID]
	if !ok {
		return nil
	}
	decision := "decline"
	if allow {
		decision = "accept"
	}
	return codexMarshalLine(map[string]any{"id": id, "result": map[string]any{"decision": decision}})
}

// CommitApproval forgets correlation only after the response reached the
// app-server transport. Keeping it through EncodeApproval lets the pane re-park
// and retry safely if the socket write fails.
func (a *CodexAdapter) CommitApproval(requestID string) {
	a.mu.Lock()
	delete(a.approvalIDs, requestID)
	a.mu.Unlock()
}

func (a *CodexAdapter) EncodeInterrupt() []byte {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.threadID == "" || a.turnID == "" {
		return nil
	}
	return a.requestLocked("turn/interrupt", map[string]any{"threadId": a.threadID, "turnId": a.turnID})
}

func (a *CodexAdapter) requestLocked(method string, params any) []byte {
	id := codexClientRequestID()
	idRaw, _ := json.Marshal(id)
	a.pending[codexRPCIDKey(idRaw)] = method
	return codexMarshalLine(map[string]any{"id": id, "method": method, "params": params})
}

func codexClientRequestID() string {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "solu-fallback"
	}
	return fmt.Sprintf("solu-%x", b[:])
}

func codexMarshalLine(v any) []byte            { b, _ := json.Marshal(v); return append(b, '\n') }
func codexRPCIDKey(raw json.RawMessage) string { return string(raw) }
func codexRPCIDDisplay(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	return string(raw)
}
func codexRawString(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	return string(raw)
}
func codexStatus(s string) string {
	if s == "inProgress" || s == "" {
		return "requesting"
	}
	return s
}
func codexTurnSubtype(s string) string {
	switch s {
	case "completed":
		return "success"
	case "failed":
		return "error"
	case "interrupted":
		return "error_interrupted"
	default:
		return s
	}
}

func codexRateLimitKind(id, name string, windowMins int) string {
	label := strings.TrimSpace(name)
	if label == "" {
		if strings.EqualFold(strings.TrimSpace(id), "codex") || strings.TrimSpace(id) == "" {
			label = "Codex"
		} else {
			label = strings.TrimSpace(id)
		}
	}
	switch windowMins {
	case 60:
		return label + " hourly limit"
	case 300:
		return label + " 5-hour limit"
	case 7 * 24 * 60:
		return label + " weekly limit"
	default:
		if windowMins > 0 {
			return fmt.Sprintf("%s %d-minute limit", label, windowMins)
		}
		return label + " rate limit"
	}
}
func codexStringList(raw json.RawMessage) []string {
	var out []string
	_ = json.Unmarshal(raw, &out)
	return out
}
func codexUserText(raw json.RawMessage) string {
	var blocks []struct{ Type, Text string }
	_ = json.Unmarshal(raw, &blocks)
	var out []string
	for _, b := range blocks {
		if b.Type == "text" && b.Text != "" {
			out = append(out, b.Text)
		}
	}
	return strings.Join(out, "\n")
}
