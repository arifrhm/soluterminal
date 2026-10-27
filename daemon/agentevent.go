package main

import "encoding/json"

// agentevent.go defines the daemon-side normalized event model for the native
// renderer (Phase 1). Every per-agent adapter (Claude stream-json today; Codex
// app-server / Gemini stream-json later) emits this ONE schema, so clients
// consume a single shape regardless of backend. The transform happens in the
// daemon (the input choke point + per-agent-difference absorber).
//
// This file is PURE TYPES + the NativeAdapter interface — no behavior. The
// Claude mapping lives in native_claude.go; the parser goroutine that stamps
// Seq/PaneID/FromReplay and owns cross-chunk line carry lives in pane_native.go.
//
// Spec: NATIVE-RENDERER-PLAN.md §3 + the Phase-0 captured-byte additions
// (system/thinking_tokens, stream_event input_json_delta).

// EventType is the discriminator for NativeEvent. Values match plan §3
// "NativeEvent types".
type EventType string

const (
	EvSessionInit        EventType = "session_init"
	EvTextDelta          EventType = "text_delta"
	EvText               EventType = "text"
	EvUserText           EventType = "user_text"
	EvThinkingDelta      EventType = "thinking_delta"
	EvThinking           EventType = "thinking"
	EvToolCall           EventType = "tool_call"
	EvToolResult         EventType = "tool_result"
	EvTodo               EventType = "todo"
	EvPermissionRequest  EventType = "permission_request"
	EvPermissionResolved EventType = "permission_resolved"
	EvUsage              EventType = "usage"
	EvRateLimit          EventType = "rate_limit"
	EvStatus             EventType = "status"
	EvTurnEnd            EventType = "turn_end"
	EvError              EventType = "error"
	// EvSlashResult carries the output of a headless slash command (/context,
	// /cost, /config, …). The CLI intercepts these locally and returns their whole
	// output in the turn's result.result string with num_turns==0 and no assistant
	// text — so we surface it as a dedicated renderable event (Text = the output).
	EvSlashResult EventType = "slash_result"
	// EvPromptSuggestion carries Claude's own model-generated "next prompt"
	// suggestion (the gray ghost-text the interactive TUI shows, accept with Tab).
	// Source: the `prompt_suggestion` stream-json line, emitted by a BACKGROUND
	// model call ~10-20s AFTER the turn's `result` line (so it arrives late and
	// not every turn yields one — treat as best-effort). Requires the
	// --prompt-suggestions argv flag on the headless agent. Text = the suggestion.
	EvPromptSuggestion EventType = "prompt_suggestion"
)

// TodoItem is one entry in a normalized todo list (todo event payload). The
// json tags are verbatim mirrors of web/src/native/nativeEvents.ts TodoItem:
// {text, completed?, status?}. Completed stays the boolean source of truth for
// the checkbox; Status adds the tri-state (pending|in_progress|completed) so the
// plan card can show what's active. Both are derived from the same TodoWrite
// status value (see todoItemsFromInput in native_claude.go).
type TodoItem struct {
	Text      string `json:"text"`
	Completed bool   `json:"completed,omitempty"`
	// Status is the normalized tri-state: "pending" | "in_progress" | "completed".
	// Unknown/missing values normalize to "pending". omitempty so a fully-pending
	// item still carries it (pending is non-empty) — matches the TS optional.
	Status string `json:"status,omitempty"`
}

// NativeApproval is the parked, lock-free-readable pending permission request
// for a native pane. The parser goroutine Stores a pointer to one of these on
// Pane.nativePendingApproval when it sees a control_request[can_use_tool], and
// clears it (Store(nil)) on resolve / control_cancel_request / interrupt.
//
// It is read lock-free at the very top of computeState (a single
// atomic.Pointer.Load): non-nil => StateChoicePrompt, short-circuiting the
// 64KB byte scan. The fields are everything the approval-resolve path needs to
// echo request.input back as updatedInput on allow (RawInput) and to address
// the agent's control_response (RequestID + ToolUseID).
//
// ConfirmID bridges to ARIA's confirm registry so both the lock-screen
// /api/approvals/{id}/resolve path (keyed by RequestID) and the in-app
// /api/aria/confirm path (keyed by ConfirmID) converge on signalConfirm.
type NativeApproval struct {
	RequestID string          `json:"requestId"`           // top-level control_request request_id (lock-screen-facing key)
	ToolUseID string          `json:"toolUseId"`           // request.tool_use_id (inner control_response toolUseID)
	ToolName  string          `json:"toolName"`            // request.tool_name (e.g. "Bash")
	RawInput  json.RawMessage `json:"rawInput"`            // request.input verbatim; echoed as updatedInput on allow
	ConfirmID string          `json:"confirmId,omitempty"` // bridge to ARIA confirm registry
}

// NativeEvent is the normalized daemon-side event. Envelope fields (Seq, PaneID,
// Type, FromReplay) come first per plan §3; the payload is a flat omitempty
// superset across all event types so the wire stays small. Clients discriminate
// on Type.
//
// Envelope ownership: Seq, PaneID, and FromReplay are stamped by the parser
// goroutine (pane_native.go) on egress, NOT by the adapter. Adapters return
// events carrying only Type + the relevant payload fields; Feed() is a pure
// (line) -> []NativeEvent transform with no monotonic state.
type NativeEvent struct {
	// --- envelope (plan §3) ---
	Seq        uint64    `json:"seq"`        // monotonic per-pane; client gap-detects + resyncs via /native/history?since=
	PaneID     string    `json:"paneId"`     // stamped by the parser goroutine
	Type       EventType `json:"type"`       // discriminator (see EventType constants)
	FromReplay bool      `json:"fromReplay"` // true = seeded from on-disk transcript (not live)

	// fromBacklog (unexported — never marshaled) marks an event parsed from the
	// agent's stdout-ring BACKLOG replay (FrameReplay) rather than fresh output.
	// Backlog events ARE processed as live state (that is what re-parks a
	// still-open approval and re-lights nativeInTurn after a daemon restart /
	// update handoff), but side effects that WRITE to the child must not re-fire
	// on a re-read of old output: emitNativeEvent skips the allowlist auto-allow
	// for backlog permission_requests — the original live pass already answered
	// it, and a duplicate control_response on claude's stdin is a protocol error.
	fromBacklog bool

	// --- session_init (<- system/init) ---
	SessionID      string   `json:"sessionId,omitempty"`
	Model          string   `json:"model,omitempty"`
	Cwd            string   `json:"cwd,omitempty"`
	PermissionMode string   `json:"permissionMode,omitempty"` // surface "auto" so the UI warns approvals won't fire
	Tools          []string `json:"tools,omitempty"`
	MCPServers     []any    `json:"mcpServers,omitempty"`
	SlashCommands  []string `json:"slashCommands,omitempty"`

	// --- text / text_delta / thinking / thinking_delta / user_text ---
	Text      string `json:"text,omitempty"`
	MessageID string `json:"messageId,omitempty"`
	Signature string `json:"signature,omitempty"` // thinking signature (real even when text is redacted)

	// --- tool_call (<- assistant content[].type=tool_use) ---
	CallID       string          `json:"callId,omitempty"`
	ToolKind     string          `json:"toolKind,omitempty"` // exec|file|mcp|search|todo (classified from name)
	Name         string          `json:"name,omitempty"`
	Args         json.RawMessage `json:"args,omitempty"`
	ParentCallID string          `json:"parentCallId,omitempty"` // = parent_tool_use_id; keys the sub-agent/Task tree

	// --- tool_result (<- user content[].type=tool_result) ---
	IsError bool            `json:"isError,omitempty"`
	Content string          `json:"content,omitempty"`
	Output  json.RawMessage `json:"output,omitempty"` // tool_use_result{stdout,stderr,exitCode,...}

	// --- todo (<- TodoWrite tool_use; renderer normalization) ---
	Items []TodoItem `json:"items,omitempty"`

	// --- permission_request / permission_resolved ---
	RequestID string          `json:"requestId,omitempty"` // top-level request_id (lock-screen-facing key)
	ToolName  string          `json:"toolName,omitempty"`  // request.tool_name
	RawEvent  json.RawMessage `json:"rawEvent,omitempty"`  // verbatim request object; approvals echo request.input as updatedInput on allow
	// WouldAutoAllow — this daemon is about to auto-allow the request from the
	// pane's allowlist (see allowlistAutoAllows), so the client should never draw
	// a decision card for it. The authoritative answer to a question the client
	// cannot compute for argument-scoped rules like "Bash(git status)": matching
	// those means parsing shell argv, and a client guess that ran more permissive
	// than the daemon would HIDE a card for a request nobody approved. Set before
	// fanout; absent on replayed/backlog requests, which stay user-answerable.
	WouldAutoAllow bool `json:"wouldAutoAllow,omitempty"`

	// --- usage / rate_limit / thinking_tokens ---
	InputTokens         int     `json:"inputTokens,omitempty"`
	CachedTokens        int     `json:"cachedInputTokens,omitempty"`   // cache_read_input_tokens
	CacheCreationTokens int     `json:"cacheCreationTokens,omitempty"` // cache_creation_input_tokens
	OutputTokens        int     `json:"outputTokens,omitempty"`
	ThinkingTok         int     `json:"reasoningTokens,omitempty"`     // also carries system/thinking_tokens estimate
	TotalTokens         int     `json:"totalTokens,omitempty"`         // provider-reported tokens occupying the current model call/context
	ContextWindowTokens int     `json:"contextWindowTokens,omitempty"` // authoritative provider window; avoids model-name guesses
	TotalCostUSD        float64 `json:"totalCostUsd,omitempty"`
	ResetsAt            int64   `json:"resetsAt,omitempty"`
	RateLimitKind       string  `json:"rateLimitKind,omitempty"` // rate_limit_info.rateLimitType

	// --- status / turn_end / error ---
	Subtype          string   `json:"subtype,omitempty"` // status: "requesting"; turn_end: "success"/"error_*"
	Detail           string   `json:"detail,omitempty"`
	NumTurns         int      `json:"numTurns,omitempty"`
	DurationMs       int      `json:"durationMs,omitempty"`
	PermissionDenied []string `json:"permissionDenials,omitempty"`
	Message          string   `json:"message,omitempty"`
	Code             string   `json:"code,omitempty"`

	// marshaled caches the JSON wire frame so a multi-client live fanout
	// serializes the event ONCE instead of once per subscriber. Unexported, so
	// encoding/json ignores it (the cached bytes are produced by marshaling the
	// event itself). Stamped in emitNativeEvent only when subscribers exist;
	// nil on ring/snapshot entries, which are (re)marshaled on demand via
	// nativeEventFrame for reconnect replay. Immutable once set => safe to share
	// the slice across concurrent subscriber writers.
	marshaled []byte
}

// NativeAdapter normalizes one agent backend's CLI/RPC output into the common
// NativeEvent schema and encodes the daemon->agent control frames. One adapter
// per agent (Claude over stream-json today; Codex/Gemini later) — all emit the
// SAME NativeEvent union.
type NativeAdapter interface {
	// Feed consumes ONE complete newline-delimited JSONL line and returns 0..n
	// events. It MUST be pure: no side effects, no Seq/PaneID assignment, no
	// blocking. An unrecognized or malformed line is logged-once-and-dropped
	// (plan §3 unknown-event-type policy) — never crashes, never re-orders,
	// never errors out the stream. Cross-chunk line carry is the caller's job
	// (the parser goroutine), so Feed assumes a complete line.
	Feed(line []byte) []NativeEvent

	// SupportsInteractiveApproval reports whether this adapter can answer a
	// permission_request (Claude: yes, via stdin control frames).
	SupportsInteractiveApproval() bool

	// EncodeUserTurn builds the stdin envelope for a user message.
	EncodeUserTurn(text string) []byte

	// EncodeApproval builds the stdin control_response answering a permission
	// request. On allow, updatedInput MUST echo request.input verbatim.
	EncodeApproval(requestID, toolUseID string, allow bool, updatedInput json.RawMessage, denyMsg string) []byte

	// EncodeInterrupt builds the stdin interrupt control_request (carries a
	// fresh request_id).
	EncodeInterrupt() []byte
}
