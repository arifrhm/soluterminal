package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"log"
	"strings"
	"sync"
)

// native_claude.go implements NativeAdapter for the Claude CLI's stream-json
// output (claude -p --output-format stream-json --input-format stream-json ...).
//
// It maps the REAL captured Claude stream-json vocabulary (see the Phase-0
// fixtures under docs/native-phase0/*.jsonl) to the normalized NativeEvent
// schema defined in agentevent.go. The mapping is keyed on the ACTUAL observed
// event types — including two NOT in the original plan §3 table that were seen
// live and MUST be handled or they are silently swallowed:
//
//   - system/thinking_tokens          -> usage{ThinkingTok}   (vocab-thinking.jsonl)
//   - stream_event input_json_delta   -> accumulated tool args (approval-fire.jsonl)
//
// CONTRACT (per the NativeAdapter interface): Feed() is PURE — one complete
// JSONL line in, 0..n events out. It assigns NO Seq/PaneID/FromReplay (that is
// the parser goroutine's job in pane_native.go), has no I/O side effects, never
// errors out the stream, never re-orders, never blocks. An unrecognized or
// malformed line is logged-once-and-dropped and bumps an internal
// unknown_event{key} counter (visible via UnknownCounts for /debug).
//
// Cross-chunk line carry (buffering partial lines) lives in the parser
// goroutine, NOT here — Feed assumes it receives exactly one complete line.

// ClaudeAdapter maps claude 2.1.183 stream-json -> NativeEvent.
//
// It is otherwise stateless except for a small unknown-event bookkeeping map
// (rate-limited logging + a /debug counter) guarded by its own mutex. The map
// does NOT affect the emitted event sequence, so adapter fixture tests stay
// deterministic.
type ClaudeAdapter struct {
	unkMu     sync.Mutex
	unkCounts map[string]uint64 // key = "<type>" or "<type>/<subtype>"; value = total times seen
	unkLogged map[string]bool   // key already logged once (rate-limit the noisy log)

	// curMsgID is the id of the message currently streaming (captured from the
	// stream_event message_start). It is stamped onto text_delta/thinking_delta
	// events so the client can COALESCE a message's deltas into one block by
	// messageId — without it every delta renders as its own paragraph and words
	// split at chunk boundaries. Touched only inside Feed, which the per-pane
	// parser goroutine drives single-threaded, so it needs no lock.
	curMsgID string
}

// NewClaudeAdapter constructs a ready ClaudeAdapter.
func NewClaudeAdapter() *ClaudeAdapter {
	return &ClaudeAdapter{
		unkCounts: make(map[string]uint64),
		unkLogged: make(map[string]bool),
	}
}

// Compile-time assertion that ClaudeAdapter satisfies the interface. (C1
// deliberately omitted this from agentevent.go because ClaudeAdapter did not
// exist yet; it lands here now.)
var _ NativeAdapter = (*ClaudeAdapter)(nil)

func (a *ClaudeAdapter) SupportsInteractiveApproval() bool { return true }

// dropUnknown records (and, once per key, logs) an unrecognized event key and
// returns nil. NEVER crashes, NEVER re-orders.
func (a *ClaudeAdapter) dropUnknown(key string) []NativeEvent {
	a.unkMu.Lock()
	a.unkCounts[key]++
	first := !a.unkLogged[key]
	if first {
		a.unkLogged[key] = true
	}
	a.unkMu.Unlock()
	if first {
		log.Printf("native(claude): dropping unknown event %q (logging once)", key)
	}
	return nil
}

// UnknownCounts returns a snapshot of the unknown-event counters for /debug.
func (a *ClaudeAdapter) UnknownCounts() map[string]uint64 {
	a.unkMu.Lock()
	defer a.unkMu.Unlock()
	out := make(map[string]uint64, len(a.unkCounts))
	for k, v := range a.unkCounts {
		out[k] = v
	}
	return out
}

// rawLine is the minimal top-level shape shared by all claude stream-json lines.
type rawLine struct {
	Type    string          `json:"type"`
	Subtype string          `json:"subtype"`
	Event   json.RawMessage `json:"event"`   // for type=="stream_event"
	Message json.RawMessage `json:"message"` // for assistant/user
	Request json.RawMessage `json:"request"` // for type=="control_request"

	// flat fields used by several types
	RequestID    string `json:"request_id"`
	SessionID    string `json:"session_id"`
	Status       string `json:"status"`           // system/status
	EstTokens    int    `json:"estimated_tokens"` // system/thinking_tokens
	IsReplay     bool   `json:"isReplay"`
	ParentToolID string `json:"parent_tool_use_id"` // threads sub-agent/Task tree onto tool_call

	// result/* fields
	IsError    bool     `json:"is_error"`
	NumTurns   int      `json:"num_turns"`
	DurationMs int      `json:"duration_ms"`
	Result     string   `json:"result"`
	TotalCost  float64  `json:"total_cost_usd"`
	Denials    []string `json:"permission_denials"`
}

// nativeEndTurnLine reports whether a raw stream-json line is a message_delta whose
// stop_reason marks the assistant DONE (end_turn / stop_sequence) — the model's
// positive turn-finished signal, which lands ~ms before the `result` line.
// processNativeLine settles nativeInTurn on it so the spinner clears on the
// done-signal deterministically instead of waiting on the idle-timeout safety net.
// Mid-turn message_deltas carry stop_reason=tool_use (more tool rounds coming) and
// are deliberately ignored — the model is NOT done. A pure raw-line peek (no
// NativeEvent, so nothing reaches the client/ring — purely a daemon settle signal),
// mirroring isNativeCancelLine.
func nativeEndTurnLine(line []byte) bool {
	var probe struct {
		Type  string `json:"type"`
		Event struct {
			Type  string `json:"type"`
			Delta struct {
				StopReason string `json:"stop_reason"`
			} `json:"delta"`
		} `json:"event"`
	}
	if json.Unmarshal(line, &probe) != nil {
		return false
	}
	if probe.Type != "stream_event" || probe.Event.Type != "message_delta" {
		return false
	}
	return probe.Event.Delta.StopReason == "end_turn" || probe.Event.Delta.StopReason == "stop_sequence"
}

// Feed maps ONE captured claude stream-json line to NativeEvents. The switch
// arms are grounded in the real fixtures.
func (a *ClaudeAdapter) Feed(line []byte) []NativeEvent {
	var r rawLine
	if err := json.Unmarshal(line, &r); err != nil {
		// Malformed line: log-once-and-drop, keyed generically so a flood of
		// junk doesn't spam the log per-distinct-byte.
		return a.dropUnknown("<malformed>")
	}

	switch r.Type {

	case "system":
		switch r.Subtype {
		case "init":
			// Store session_id + permissionMode IMMEDIATELY.
			var init struct {
				SessionID      string   `json:"session_id"`
				Model          string   `json:"model"`
				Cwd            string   `json:"cwd"`
				PermissionMode string   `json:"permissionMode"`
				Tools          []string `json:"tools"`
				MCPServers     []any    `json:"mcp_servers"`
				SlashCommands  []string `json:"slash_commands"`
			}
			_ = json.Unmarshal(line, &init)
			return []NativeEvent{{
				Type: EvSessionInit, SessionID: init.SessionID, Model: init.Model, Cwd: init.Cwd,
				PermissionMode: init.PermissionMode, Tools: init.Tools, MCPServers: init.MCPServers,
				SlashCommands: init.SlashCommands,
			}}
		case "status":
			// {status:"requesting"} — drives the spinner.
			return []NativeEvent{{Type: EvStatus, Subtype: r.Status}}
		case "thinking_tokens":
			// NOT in plan §3 — a running token estimate. Fold into usage.
			return []NativeEvent{{Type: EvUsage, ThinkingTok: r.EstTokens}}
		case "compact_boundary":
			// /compact (or auto-compaction) summarized the conversation. The CLI
			// then replays the summary + a "<local-command-stdout>Compacted…"
			// stub as user lines — BOTH are dropped by the user-case synthetic
			// filter below. Surface a clean confirmation in their place. We reuse
			// slash_result (no new event type / schema churn): the /compact result
			// line carries an EMPTY .result with num_turns:0, so the existing
			// slash_result gate never fires and the user would otherwise see
			// nothing but a context-% drop.
			//
			// Current CLIs include the post-compaction occupancy in boundary metadata.
			// Accept both the on-disk camelCase and wire snake_case spellings: the
			// trailing usage deliberately follows the reset marker so the client first
			// clears stale occupancy, then immediately installs the real new reading.
			var boundary struct {
				CompactMetadata struct {
					PreTokens  int `json:"preTokens"`
					PostTokens int `json:"postTokens"`
				} `json:"compactMetadata"`
				CompactMetadataSnake struct {
					PreTokens  int `json:"pre_tokens"`
					PostTokens int `json:"post_tokens"`
				} `json:"compact_metadata"`
			}
			_ = json.Unmarshal(line, &boundary)
			postTokens := boundary.CompactMetadata.PostTokens
			if postTokens == 0 {
				postTokens = boundary.CompactMetadataSnake.PostTokens
			}
			events := []NativeEvent{{Type: EvSlashResult, Subtype: "compact", Text: "✓ Earlier conversation compacted into a summary to free up context."}}
			if postTokens > 0 {
				events = append(events, NativeEvent{Type: EvUsage, InputTokens: postTokens})
			}
			return events
		case "task_notification":
			// Background-shell COMPLETION (a run_in_background Bash / Task finished).
			// On the live stream-json wire this arrives as a SYSTEM event, but the
			// on-disk transcript normalizes the SAME notification to a `type:"user"`
			// line whose content is a `<task-notification>` blob — and the client's
			// background-shell tracker (nativeTranscript.ts) is built around THAT
			// form: it scans user_text for `<task-notification>`, correlates by task
			// id, marks the launch "exited", and clears the "waiting on N background
			// shell" footer. Dropping this (the old default) meant the LIVE path never
			// delivered a completion, so the footer counter stuck forever — an agent
			// that ran a background bash kept showing "waiting on 1 background shell"
			// even after it exited 0. Bridge it: re-emit the wire event as the same
			// `<task-notification>` user_text the transcript produces. The client
			// hides this from the visible rows (its `^<task-notification` filter) and
			// uses it only to correlate the exit by task id. Terminal statuses are
			// completed/stopped/killed/failed; the summary carries "(exit code N)".
			var tn struct {
				TaskID     string `json:"task_id"`
				ToolUseID  string `json:"tool_use_id"`
				Status     string `json:"status"`
				OutputFile string `json:"output_file"`
				Summary    string `json:"summary"`
			}
			_ = json.Unmarshal(line, &tn)
			if tn.TaskID == "" {
				return a.dropUnknown("system/task_notification(no-id)")
			}
			text := "<task-notification>\n<task-id>" + tn.TaskID +
				"</task-id>\n<tool-use-id>" + tn.ToolUseID +
				"</tool-use-id>\n<output-file>" + tn.OutputFile +
				"</output-file>\n<status>" + tn.Status +
				"</status>\n<summary>" + tn.Summary +
				"</summary>\n</task-notification>"
			return []NativeEvent{{Type: EvUserText, Text: text}}
		default:
			return a.dropUnknown("system/" + r.Subtype)
		}

	case "rate_limit_event":
		var rl struct {
			RateLimitInfo struct {
				ResetsAt      int64  `json:"resetsAt"`
				RateLimitType string `json:"rateLimitType"`
			} `json:"rate_limit_info"`
		}
		_ = json.Unmarshal(line, &rl)
		return []NativeEvent{{Type: EvRateLimit, ResetsAt: rl.RateLimitInfo.ResetsAt, RateLimitKind: rl.RateLimitInfo.RateLimitType}}

	case "stream_event":
		// The streaming layer (needs --include-partial-messages).
		return a.mapStreamEvent(r.Event)

	case "assistant":
		// Finalized message; fan out content blocks. Deltas already streamed via
		// stream_event; the client reconciles preview deltas against finalized
		// blocks by message.id.
		return a.mapAssistant(r.Message, r.ParentToolID)

	case "user":
		// A user line is EITHER a human turn (message.content is a STRING) or a
		// batch of tool_result blocks (message.content is a LIST). Map the string
		// shape to a user_text event so live + transcript-seed render the user's
		// own prompt the same way; map the list shape to tool_result events.
		//
		// The --replay-user-messages echo of THIS turn also arrives as a string
		// user line (r.IsReplay); emit it as user_text too and carry the message id
		// so the client can dedup it against its optimistic composer turn.
		if txt, msgID, ok := userMessageText(r.Message); ok {
			if isSyntheticUserText(txt) {
				// CLI-injected scaffolding, NOT a human turn: the compaction
				// summary and the command-wrapper / local-command stubs. The
				// transcript-seed path drops these via the isMeta/isCompactSummary
				// FLAGS (native_transcript.go), but the LIVE replay strips those
				// flags, so the live path must recognize them by content to stay
				// at parity. Without this, /compact leaks the raw summary +
				// "<local-command-stdout>Compacted</local-command-stdout>" bubbles.
				return nil
			}
			if ctx, user, ok := splitAmbientDelegation(txt); ok {
				return []NativeEvent{
					{Type: EvUserText, Text: ctx, MessageID: msgID},
					{Type: EvUserText, Text: user, MessageID: msgID},
				}
			}
			return []NativeEvent{{Type: EvUserText, Text: txt, MessageID: msgID}}
		}
		// tool_result lines.
		return a.mapUserToolResults(r.Message)

	case "control_request":
		// THE marquee event — the can_use_tool permission fork.
		return a.mapControlRequest(r.RequestID, r.Request)

	case "control_cancel_request":
		// The agent cancels its OWN pending approval (it emits this after we send
		// an interrupt). Known-handled: the parser/approvals layer clears
		// nativePendingApproval on this signal. We emit nothing here (no
		// client-facing native event), but it is NOT an unknown-type drop.
		return nil

	case "result":
		// Per TURN, not per session.
		sub := r.Subtype
		ev := NativeEvent{
			Type: EvTurnEnd, Subtype: sub, NumTurns: r.NumTurns, DurationMs: r.DurationMs,
			TotalCostUSD: r.TotalCost, PermissionDenied: r.Denials,
		}
		if r.IsError || sub != "success" {
			// Also emit an error so the UI can surface it distinctly.
			return []NativeEvent{ev, {Type: EvError, Message: r.Result, Code: sub}}
		}
		// A headless slash command (/context, /cost, /config, …) is intercepted by
		// the CLI and returns its ENTIRE output in result.result with num_turns==0
		// and NO assistant text events. For a normal turn (num_turns>=1) result
		// merely DUPLICATES the already-streamed assistant text, so it stays dropped.
		// Surface the slash output as a dedicated renderable event, emitted BEFORE
		// the turn_end so it gets the earlier seq and renders above the boundary.
		if r.NumTurns == 0 && strings.TrimSpace(r.Result) != "" {
			return []NativeEvent{{Type: EvSlashResult, Text: r.Result}, ev}
		}
		return []NativeEvent{ev}

	case "prompt_suggestion":
		// Claude's own next-prompt suggestion (background model call, lands
		// ~10-20s after `result`, not every turn). Text = the suggested prompt.
		// Requires --prompt-suggestions on the argv (see nativeClaudeArgv).
		var ps struct {
			Suggestion string `json:"suggestion"`
		}
		_ = json.Unmarshal(line, &ps)
		if s := strings.TrimSpace(ps.Suggestion); s != "" {
			return []NativeEvent{{Type: EvPromptSuggestion, Text: s}}
		}
		return nil

	default:
		// Unknown top-level type. The live vocabulary is open and grows on CLI
		// upgrades — log-once-and-drop, never crash.
		return a.dropUnknown(r.Type)
	}
}

// mapStreamEvent handles the Anthropic-SSE wrapper {type:"stream_event",event:{...}}.
func (a *ClaudeAdapter) mapStreamEvent(raw json.RawMessage) []NativeEvent {
	var ev struct {
		Type    string `json:"type"`
		Index   int    `json:"index"`
		Message struct {
			ID string `json:"id"`
		} `json:"message"` // present on message_start — the streaming message id
		Delta struct {
			Type        string `json:"type"` // text_delta|thinking_delta|signature_delta|input_json_delta
			Text        string `json:"text"`
			Thinking    string `json:"thinking"`
			Signature   string `json:"signature"`
			PartialJSON string `json:"partial_json"`
		} `json:"delta"`
	}
	if err := json.Unmarshal(raw, &ev); err != nil {
		return nil
	}

	switch ev.Type {
	case "content_block_delta":
		switch ev.Delta.Type {
		case "text_delta":
			// Stamp the streaming message id so the client coalesces this
			// message's deltas into ONE block (else words split at chunk seams).
			return []NativeEvent{{Type: EvTextDelta, Text: ev.Delta.Text, MessageID: a.curMsgID}}
		case "thinking_delta":
			// Empty text on redacted accounts — emit ONLY if non-empty.
			if ev.Delta.Thinking == "" {
				return nil
			}
			return []NativeEvent{{Type: EvThinkingDelta, Text: ev.Delta.Thinking, MessageID: a.curMsgID}}
		case "signature_delta":
			// Attach to the open thinking block; signature is real even when
			// the thinking text is redacted/empty. NO messageId — a messageId'd
			// terminal thinking event REPLACES the coalesced block, and this
			// carries no text, so stamping it would wipe the thinking text.
			return []NativeEvent{{Type: EvThinking, Signature: ev.Delta.Signature}}
		case "input_json_delta":
			// Streaming tool args. Standalone emits nothing — the finalized
			// assistant tool_use block carries the complete Args. (A future
			// per-(messageId,index) accumulator could preview args before the
			// final block; not needed for Phase 1.)
			return nil
		}
		return nil
	case "message_start":
		// Capture the streaming message id for the deltas that follow.
		if ev.Message.ID != "" {
			a.curMsgID = ev.Message.ID
		}
		return nil
	case "message_stop":
		a.curMsgID = ""
		return nil
	case "content_block_start", "content_block_stop", "message_delta":
		// Structural framing; semantic no-ops.
		return nil
	}
	return nil
}

// mapAssistant fans out a finalized assistant message's content blocks.
// parentToolID is the line-top-level parent_tool_use_id (sub-agent/Task tree).
func (a *ClaudeAdapter) mapAssistant(raw json.RawMessage, parentToolID string) []NativeEvent {
	var m struct {
		ID      string `json:"id"`
		Content []struct {
			Type      string          `json:"type"` // text|thinking|tool_use
			Text      string          `json:"text"`
			Thinking  string          `json:"thinking"`
			Signature string          `json:"signature"`
			ID        string          `json:"id"`
			Name      string          `json:"name"`
			Input     json.RawMessage `json:"input"`
		} `json:"content"`
		Usage json.RawMessage `json:"usage"`
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil
	}
	var out []NativeEvent
	for _, b := range m.Content {
		switch b.Type {
		case "text":
			out = append(out, NativeEvent{Type: EvText, Text: b.Text, MessageID: m.ID})
		case "thinking":
			// Signature-only when redacted; emit text only if present, always carry signature.
			out = append(out, NativeEvent{Type: EvThinking, Text: b.Thinking, Signature: b.Signature, MessageID: m.ID})
		case "tool_use":
			out = append(out, NativeEvent{
				Type: EvToolCall, CallID: b.ID, Name: b.Name, Args: b.Input,
				ToolKind: classifyTool(b.Name), ParentCallID: parentToolID, MessageID: m.ID,
			})
			// TodoWrite carries a plan in its input.todos[]. Emit a NORMALIZED
			// EvTodo IN ADDITION to the tool_call above (we keep the tool_call so
			// its tool_result still pairs by id and the sequence stays valid; the
			// web client suppresses the duplicate TodoWrite card and renders the
			// EvTodo plan instead). A malformed input yields no todo event (and no
			// crash) — todoItemsFromInput returns nil, which we skip.
			if b.Name == "TodoWrite" {
				if items := todoItemsFromInput(b.Input); len(items) > 0 {
					out = append(out, NativeEvent{Type: EvTodo, Items: items})
				}
			}
		}
	}
	if len(m.Usage) > 0 {
		out = append(out, parseUsage(m.Usage, parentToolID))
	}
	return out
}

// userMessageText reports whether a user line's message.content is a plain
// STRING (a human turn / replay echo) and, if so, returns that text plus the
// message id (when present, for client-side dedup). A LIST content (tool_result
// blocks) returns ok=false so the caller falls through to mapUserToolResults.
func userMessageText(raw json.RawMessage) (text, msgID string, ok bool) {
	var m struct {
		ID      string          `json:"id"`
		Content json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(raw, &m); err != nil || len(m.Content) == 0 {
		return "", "", false
	}
	var s string
	if err := json.Unmarshal(m.Content, &s); err != nil {
		return "", "", false // list content (tool_result) — not a human turn.
	}
	return s, m.ID, true
}

// isSyntheticUserText reports whether a string-content user line is CLI-injected
// scaffolding rather than a genuine human turn. Two kinds leak into the LIVE
// stream after /compact (verified against claude 2.1.185):
//   - the compaction summary (preamble "This session is being continued…"), and
//   - command-wrapper / local-command stubs ("<local-command-stdout>Compacted…",
//     "<command-name>/foo</command-name>", etc.).
//
// The transcript-seed path (native_transcript.go) drops these via the
// isCompactSummary / isMeta FLAGS, but the live replay STRIPS those flags, so we
// must recognize them by content to keep live and seed at parity. Mirrors the
// command-wrapper prefix set used by the conversation endpoint (conversation.go).
func isSyntheticUserText(s string) bool {
	t := strings.TrimLeft(s, " \t\r\n")
	if hasAnyPrefix(t, "<command-name>", "<command-message>", "<command-args>",
		"<local-command-", "<command-stdout>", "<command-stderr>") {
		return true
	}
	return strings.HasPrefix(t, "This session is being continued from a previous conversation")
}

// mapUserToolResults handles user lines whose content is a list of tool_result blocks.
func (a *ClaudeAdapter) mapUserToolResults(raw json.RawMessage) []NativeEvent {
	var m struct {
		Content json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(raw, &m); err != nil || len(m.Content) == 0 {
		return nil
	}
	// content may be a string (human turn) or a list (tool_result blocks).
	var blocks []struct {
		Type      string          `json:"type"`
		ToolUseID string          `json:"tool_use_id"`
		Content   json.RawMessage `json:"content"` // string OR list of {type:text|image}
		Text      string          `json:"text"`    // text blocks (mid-turn task notifications)
		IsError   bool            `json:"is_error"`
	}
	if err := json.Unmarshal(m.Content, &blocks); err != nil {
		return nil // string content: not a tool_result line.
	}
	var out []NativeEvent
	for _, b := range blocks {
		// Mid-turn background-task completion. A PARKED agent gets its
		// completion as system/task_notification (bridged above), but while a
		// turn is IN FLIGHT the harness injects the notification as a text
		// block inside the next list-shaped user message instead. Dropping it
		// (the old behavior: only tool_result blocks survived this loop) meant
		// the client's bg-shell tracker never saw mid-turn completions, so the
		// "waiting on N background shells" footer went STALE — cleared only by
		// a reconnect, whose replay hides the launches wholesale. Emit the
		// <task-notification> slice as user_text: the leading tag is what the
		// client's row filter hides on, and the tracker correlates the exit by
		// task id. Other injected text blocks (system reminders) stay dropped.
		if b.Type == "text" {
			if i := strings.Index(b.Text, "<task-notification>"); i >= 0 {
				out = append(out, NativeEvent{Type: EvUserText, Text: b.Text[i:]})
			}
			continue
		}
		if b.Type != "tool_result" {
			continue
		}
		out = append(out, NativeEvent{
			Type: EvToolResult, CallID: b.ToolUseID, IsError: b.IsError, Content: unquoteContent(b.Content),
		})
	}
	return out
}

// unquoteContent renders a tool_result content field as a plain string. The
// field is JSON that is usually a string ("(Bash completed with no output)")
// but can be a list of content blocks; for the string case we unquote, else we
// keep the raw JSON so nothing is lost.
func unquoteContent(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	return string(raw)
}

// mapControlRequest handles the can_use_tool permission fork. RawEvent is kept
// verbatim so approvals.go can echo request.input back as updatedInput on allow.
func (a *ClaudeAdapter) mapControlRequest(requestID string, raw json.RawMessage) []NativeEvent {
	var req struct {
		Subtype   string          `json:"subtype"`
		ToolName  string          `json:"tool_name"`
		Input     json.RawMessage `json:"input"`
		ToolUseID string          `json:"tool_use_id"`
	}
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil
	}
	switch req.Subtype {
	case "can_use_tool":
		return []NativeEvent{{
			Type: EvPermissionRequest, RequestID: requestID, ToolName: req.ToolName,
			CallID: req.ToolUseID, Args: req.Input, RawEvent: raw,
		}}
	default:
		// initialize / interrupt acks etc. — not client-facing.
		return nil
	}
}

// ---------------------------------------------------------------------------
// Stdin encoders — verbatim shapes proven in the fixtures.
// ---------------------------------------------------------------------------

// EncodeUserTurn: {"type":"user","message":{"role":"user","content":"<text>"}}\n
func (a *ClaudeAdapter) EncodeUserTurn(text string) []byte {
	msg := map[string]any{
		"type":    "user",
		"message": map[string]any{"role": "user", "content": text},
	}
	b, _ := json.Marshal(msg)
	return append(b, '\n')
}

// EncodeApproval: bare control_response, NO initialize handshake. On allow,
// updatedInput MUST echo request.input verbatim. Note the captured key-name
// asymmetry: the INNER response object uses "toolUseID" (camelCase ID) and
// "request_id" (snake) — both load-bearing, do NOT normalize.
func (a *ClaudeAdapter) EncodeApproval(requestID, toolUseID string, allow bool, updatedInput json.RawMessage, denyMsg string) []byte {
	var resp map[string]any
	if allow {
		ui := updatedInput
		if len(ui) == 0 {
			ui = json.RawMessage("{}")
		}
		resp = map[string]any{"behavior": "allow", "updatedInput": ui, "toolUseID": toolUseID}
	} else {
		// A deny ALWAYS yields a tool_result with is_error=true, and the
		// Anthropic API rejects an is_error tool_result whose content is empty
		// ("content cannot be empty if `is_error` is true"). An empty deny
		// message therefore 400s the NEXT turn and poisons the whole
		// conversation (the bad tool_result is replayed in history forever).
		// Never send an empty deny message — default it to a non-empty string.
		msg := denyMsg
		if strings.TrimSpace(msg) == "" {
			msg = "The user denied this request."
		}
		resp = map[string]any{"behavior": "deny", "message": msg, "toolUseID": toolUseID}
	}
	frame := map[string]any{
		"type":     "control_response",
		"response": map[string]any{"subtype": "success", "request_id": requestID, "response": resp},
	}
	b, _ := json.Marshal(frame)
	return append(b, '\n')
}

// EncodeInterrupt: {"type":"control_request","request_id":"<new>","request":{"subtype":"interrupt"}}\n
// The agent responds by emitting control_cancel_request + a result with subtype
// error_during_execution.
func (a *ClaudeAdapter) EncodeInterrupt() []byte {
	frame := map[string]any{
		"type":       "control_request",
		"request_id": newRequestID(),
		"request":    map[string]any{"subtype": "interrupt"},
	}
	b, _ := json.Marshal(frame)
	return append(b, '\n')
}

// newRequestID returns a fresh opaque request_id for daemon->agent control
// frames (interrupts). The exact format is not load-bearing; the agent echoes
// it back. crypto/rand keeps it collision-free across concurrent interrupts.
func newRequestID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand failure is effectively unreachable; a fixed prefix is
		// fine as a last resort since the value is opaque.
		return "req_fallback"
	}
	return "req_" + hex.EncodeToString(b[:])
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func classifyTool(name string) string {
	switch name {
	case "Bash":
		return "exec"
	case "Edit", "Write", "NotebookEdit", "Read":
		return "file"
	case "WebSearch", "WebFetch":
		return "search"
	case "TodoWrite", "TaskCreate", "TaskUpdate":
		return "todo"
	default:
		if len(name) > 5 && name[:5] == "mcp__" {
			return "mcp"
		}
		return "exec"
	}
}

// todoItemsFromInput parses a TodoWrite tool_use input ({"todos":[{content,
// status,activeForm},...]}) into normalized TodoItems, preserving order. It is
// defensive: a malformed/empty input or a missing todos array returns nil (the
// caller then emits NO todo event rather than crashing). Each item's status is
// normalized via normalizeTodoStatus (unknown/missing -> "pending"); Completed
// mirrors status == "completed" so the checkbox stays correct even if a client
// only reads the boolean. The display text prefers content (the imperative form
// shown in Claude's plan); activeForm is ignored (the web card derives "active"
// from status == "in_progress", not from activeForm).
func todoItemsFromInput(raw json.RawMessage) []TodoItem {
	if len(raw) == 0 {
		return nil
	}
	var in struct {
		Todos []struct {
			Content    string `json:"content"`
			Status     string `json:"status"`
			ActiveForm string `json:"activeForm"`
		} `json:"todos"`
	}
	if err := json.Unmarshal(raw, &in); err != nil || len(in.Todos) == 0 {
		return nil
	}
	items := make([]TodoItem, 0, len(in.Todos))
	for _, t := range in.Todos {
		status := normalizeTodoStatus(t.Status)
		items = append(items, TodoItem{
			Text:      t.Content,
			Completed: status == "completed",
			Status:    status,
		})
	}
	return items
}

// normalizeTodoStatus clamps a raw TodoWrite status to the tri-state contract
// (pending|in_progress|completed). Anything unrecognized or missing -> pending.
func normalizeTodoStatus(s string) string {
	switch s {
	case "in_progress", "completed":
		return s
	default:
		return "pending"
	}
}

// parseUsage maps an assistant message's usage block. parentToolID is the line's
// parent_tool_use_id: when set, this usage is a SUB-AGENT/Task turn measuring the
// subagent's own (separate) context window, NOT the main thread's — tag it so the
// footer ctx% can ignore it. With --include-partial-messages the CLI streams
// subagent turns with their own usage; without this tag the metabar's
// last-usage-wins logic would flicker to the subagent's window mid-Task.
func parseUsage(raw json.RawMessage, parentToolID string) NativeEvent {
	var u struct {
		InputTokens              int `json:"input_tokens"`
		CacheReadInputTokens     int `json:"cache_read_input_tokens"`
		CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
		OutputTokens             int `json:"output_tokens"`
	}
	_ = json.Unmarshal(raw, &u)
	return NativeEvent{
		Type: EvUsage, InputTokens: u.InputTokens, CachedTokens: u.CacheReadInputTokens,
		CacheCreationTokens: u.CacheCreationInputTokens, OutputTokens: u.OutputTokens,
		ParentCallID: parentToolID,
	}
}
