// nativeEvents.ts — TS mirror of the daemon's normalized native-renderer event
// schema. The authoritative source is daemon/agentevent.go: every per-agent
// adapter (Claude stream-json today; Codex/Gemini later) emits this ONE shape,
// so the web client consumes a single union regardless of backend.
//
// The wire is a FLAT omitempty superset, NOT a true tagged union: every payload
// field is top-level on every event and only the four envelope fields
// (seq/paneId/type/fromReplay) are guaranteed present. Clients discriminate on
// `type` and read only the fields relevant to that type. We model that exactly:
// a base envelope + an all-optional payload. JSON keys are verbatim from the Go
// `json:"..."` tags, including the renames where the Go field name differs from
// the wire key (CachedTokens→cachedInputTokens, ThinkingTok→reasoningTokens,
// TotalCostUSD→totalCostUsd, RateLimitKind→rateLimitKind,
// PermissionDenied→permissionDenials).

// EventType is the discriminator. Values match agentevent.go's EventType const
// block (the wire `type` field).
export type EventType =
  | 'session_init'
  | 'text_delta'
  | 'text'
  | 'user_text'
  | 'thinking_delta'
  | 'thinking'
  | 'tool_call'
  | 'tool_result'
  | 'todo'
  | 'permission_request'
  | 'permission_resolved'
  | 'usage'
  | 'rate_limit'
  | 'status'
  | 'turn_end'
  | 'error'
  | 'slash_result'
  | 'prompt_suggestion';

// TodoItem is one entry in a normalized todo list (todo event payload).
export interface TodoItem {
  text: string;
  completed?: boolean;
  // Tri-state status from the agent's TodoWrite (pending|in_progress|completed).
  // `completed` stays the boolean source of truth for the checkbox; `status`
  // adds the in_progress signal so the plan card can show what's active.
  status?: 'pending' | 'in_progress' | 'completed';
}

// NativeEvent is the normalized daemon-side event. Envelope fields are always
// present; payload fields are omitempty on the wire (hence all optional here).
// Discriminate on `type`. See agentevent.go for which fields each type carries.
export interface NativeEvent {
  // --- envelope (always present) ---
  seq: number; // monotonic per-pane; gap-detect + resync via /native/history?since=
  paneId: string;
  type: EventType;
  fromReplay: boolean; // true = seeded from on-disk transcript (not live)

  // --- session_init ---
  sessionId?: string;
  model?: string;
  cwd?: string;
  permissionMode?: string; // "auto" => UI should warn approvals won't fire
  tools?: string[];
  mcpServers?: unknown[];
  slashCommands?: string[];

  // --- text / text_delta / thinking / thinking_delta / user_text ---
  text?: string;
  messageId?: string;
  signature?: string; // thinking signature (real even when text redacted)

  // --- client-side annotations (NOT wire fields) ---
  // localMsgId: legacy optimistic-echo disambiguator. The daemon does NOT stamp
  // this on the streamed `user_text` echo (it only returns it in the /native/input
  // POST response), so reconcile is purely content-FIFO and NOTHING reads this off
  // a wire event. Kept only as an optional annotation for symmetry with PendingMsg;
  // do not reintroduce an id-match reconcile branch keyed on it.
  localMsgId?: string;
  // optimistic: a synthetic user row rendered from the pending list before its
  // user_text replay echo lands (never folded into the seq cache).
  optimistic?: boolean;
  // streaming: set on the in-progress coalesced text/thinking block; cleared
  // when the authoritative event or a turn boundary lands. Drives the caret.
  streaming?: boolean;
  // clientTs: best-effort arrival time (ms epoch) stamped in the hook's fold for
  // LIVE events only (replayed transcript events have no meaningful arrival
  // time). Not a wire field. Drives the user-turn relative-time separators and
  // the per-tool duration (result.clientTs − call.clientTs).
  clientTs?: number;
  // clientTsEnd: best-effort settle time stamped on a streaming text/thinking
  // block when it stops streaming (authoritative supersede or turn boundary).
  // Drives the "thought for Xs" summary on a collapsed thinking block.
  clientTsEnd?: number;
  // interrupted: a CLIENT-SIDE synthetic marker (never a wire field), set by
  // buildTranscript on a FABRICATED tool_result for a tool_call that has no real
  // result while the daemon reports the turn settled (agentMidTurn===false). That
  // result will never arrive — the turn was cut off mid-flight (classically: an
  // auto-update respawned the headless agent via `claude --resume`, stranding an
  // in-flight Task/Bash). Flips the tool row from a perpetual "running…" spinner
  // to a quiet "interrupted" state. Self-correcting: vanishes on the next rebuild
  // if the daemon reports the agent working again.
  interrupted?: boolean;

  // bgShell: a CLIENT-SIDE synthetic marker (never a wire field), set by
  // buildTranscript on a background bash's tool_result when a later BashOutput /
  // task-output result (or a "No task found" reap) for its shell id reports the shell
  // EXITED. Lets the "background" badge flip from "running" to "exited <code>". Absent
  // = no completion signal seen → the badge stays "running" (the launch result's own
  // wording). Self-correcting: re-derived from the events on every rebuild.
  bgShell?: { state: 'exited'; exitCode?: number };

  // --- tool_call ---
  callId?: string;
  toolKind?: string; // exec | file | mcp | search | todo
  name?: string;
  args?: unknown; // raw JSON; also populated on permission_request
  parentCallId?: string; // = parent_tool_use_id; keys the sub-agent/Task tree

  // --- tool_result ---
  isError?: boolean;
  content?: string;
  output?: unknown; // tool_use_result{stdout,stderr,exitCode,...}

  // --- todo ---
  items?: TodoItem[];

  // --- permission_request / permission_resolved ---
  requestId?: string; // top-level request_id; passed to /approvals/{reqId}/resolve
  toolName?: string; // request.tool_name (e.g. "Bash")
  rawEvent?: unknown; // verbatim request object
  // The daemon is about to auto-allow this from the pane's allowlist, so no
  // decision card should ever be drawn for it (see useNativePane's
  // pendingApprovals filter). Authoritative — the daemon owns the rule matching,
  // including argument-scoped rules like "Bash(git status)" and the cautious-mode
  // veto. Absent on backlog/replayed requests, which stay user-answerable.
  wouldAutoAllow?: boolean;

  // --- usage / rate_limit ---
  inputTokens?: number;
  cachedInputTokens?: number; // cache_read_input_tokens
  cacheCreationTokens?: number; // cache_creation_input_tokens (omitted by older daemons)
  outputTokens?: number;
  reasoningTokens?: number; // also carries system/thinking_tokens estimate
  totalTokens?: number; // provider-reported occupancy for the latest model call
  contextWindowTokens?: number; // authoritative provider context-window size
  totalCostUsd?: number;
  resetsAt?: number; // rate_limit; unix seconds
  rateLimitKind?: string;

  // --- status / turn_end / error ---
  subtype?: string; // status: "requesting"; turn_end: "success"/"error_*"
  detail?: string;
  numTurns?: number;
  durationMs?: number;
  permissionDenials?: string[];
  message?: string; // error message
  code?: string; // error code
}

// Response of GET /api/panes/{id}/native/history?since=<seq>.
export interface NativeHistoryResponse {
  paneId?: string;
  since: number;
  events: NativeEvent[];
  // true when events[0].seq > since+1: some events aged out of the daemon ring,
  // so a contiguous backfill is impossible — the client must drop history and
  // do a fresh full SSE resnapshot instead of trusting the seq run.
  truncated: boolean;
}

// Response of POST /api/panes/{id}/native/input.
export interface NativeInputResponse {
  bytes: number;
  localMsgId: string;
}

// Response of POST /api/panes/{id}/approvals/{reqId}/resolve. IDEMPOTENT:
// returns {resolved:false, reason} (HTTP 200, NOT 404) for an unknown/
// already-resolved reqId — treat that as a successful no-op.
export interface ResolveApprovalResponse {
  resolved: boolean;
  allow?: boolean;
  reason?: string;
}

// Response of POST /api/panes/{id}/native/interrupt.
export interface NativeInterruptResponse {
  interrupted: boolean;
}

// Response of GET/POST /api/panes/{id}/native/allowlist — the pane's per-tool
// "always allow" set. The GET reads the current list; the POST (body
// {tool, action:"add"|"remove"}) mutates it and returns the UPDATED list, so a
// single response shape covers both. Match is by exact tool name. A tool on this
// list auto-approves future permission_requests for that tool without a card.
export interface NativeAllowlistResponse {
  tools: string[];
}
