// nativeTranscript.ts — pure transcript-shaping helpers for the native pane.
// Kept out of the React component so they can be unit-tested in isolation:
//   • buildTranscript: folds the NativeEvent stream into render rows, pairing
//     tool_result → tool_call, hoisting usage/rate_limit to the footer, and
//     INSERTING a turn-separator row before each user turn (after the first) so
//     long scrollback is scannable.
//   • relativeTime: a "just now / 2m ago" formatter shared by the separators.
//
// These mirror the contract NativePane previously inlined; the only new shape is
// the RenderRow union (an event row OR a turn separator).

import type { NativeEvent } from './nativeEvents';

// A render row is either a real event to dispatch to a sub-view, a synthetic
// turn separator (a subtle divider + relative time) marking a user-turn boundary,
// or a synthetic GROUP that folds a run of adjacent tool calls / thinking blocks
// into one compact, expandable block (see groupRows).
export type RenderRow =
  | { kind: 'event'; event: NativeEvent }
  | { kind: 'turn-sep'; key: string; clientTs?: number; fromReplay: boolean }
  | { kind: 'tool-group'; key: string; events: NativeEvent[] }
  | { kind: 'edit-group'; key: string; events: NativeEvent[] }
  // step-group — an interleaved run of thinking + groupable tool calls (the
  // think→tool→think→tool ladder), folded into ONE group; events are in stream order.
  | { kind: 'step-group'; key: string; events: NativeEvent[] }
  | { kind: 'thinking-group'; key: string; events: NativeEvent[] };

export interface Transcript {
  rows: RenderRow[];
  resultByCallId: Map<string, NativeEvent>;
  // childrenByParent — nested sub-agent tool_calls grouped by their parent Task's
  // callId (their parentCallId), in stream order. TaskView renders these INDENTED
  // under the parent Task instead of flat at top level. Absent for a Task that
  // captured no children → it degrades to a plain tool row.
  childrenByParent: Map<string, NativeEvent[]>;
  usage?: NativeEvent;
  rateLimit?: NativeEvent;
  // Cumulative session cost (USD) — the latest turn_end's total_cost_usd, which the
  // CLI reports as a running session total (not per-turn). undefined until a turn
  // completes. Hoisted here so the metabar can render it without re-scanning events.
  cost?: number;
  // contextCompacted — true when a /compact (compact_boundary, tagged slash_result
  // subtype:"compact") is the LATEST context-relevant event, i.e. no context-bearing
  // usage event has landed since. Older boundaries without postTokens leave the
  // last `usage` above as STALE pre-compact occupancy. Current boundaries append
  // a real postTokens usage, which clears this marker immediately. The metabar
  // must not derive ctx% while the fallback marker remains.
  contextCompacted: boolean;
}

// Is this tool_call a TodoWrite (the agent's plan-update tool)? The daemon now
// emits a dedicated `todo` event for the plan list, which TodoView renders, so the
// raw TodoWrite tool_call+result card is a DUPLICATE of that plan card and we drop
// it here. Match by either wire key the tool name can land on (name | toolName).
function isTodoWriteCall(e: NativeEvent): boolean {
  return e.type === 'tool_call' && (e.name === 'TodoWrite' || e.toolName === 'TodoWrite');
}

// --- Background-shell live status -------------------------------------------
// A `Bash` with run_in_background:true launches DETACHED and returns immediately with a
// result like "Command running in background with ID: <id>. Output is being written to:
// …/tasks/<id>.output. You will be notified when it completes." The agent learns the
// exit only LATER, via a BashOutput poll (or the task is reaped). We correlate by <id>
// and stamp the LAUNCH result with `bgShell` once an EXIT is seen, so the "background"
// badge reads running → exited. Pure derivation; FAILS SAFE — no completion signal (the
// agent never polled, or a format we don't recognise) → no annotation → badge stays
// "running" (never a false "exited"). Daemon does no special handling; all the data
// rides generic tool_result content the client already has.

function extractBashId(content: string): string | null {
  const m =
    content.match(/with ID:\s*([A-Za-z0-9_-]+)/i) || content.match(/tasks\/([A-Za-z0-9_-]+)\.output/i);
  return m ? m[1] : null;
}

// Does this payload report the shell EXITED? Returns the exit code (undefined when
// known-exited but the code isn't present, e.g. a reap, or a stop/kill that carries no
// code). Two sources, both keyed by the shell id: a BashOutput/TaskOutput tool_result
// (`<status>completed|failed</status>` + `<exit_code>N</exit_code>`), OR the
// `<task-notification>` completion message Claude Code injects as a user turn
// (`<status>…</status>` + an optional "…exit code N" summary, no `<exit_code>` tag). A
// reaped task returns "No task found with ID: <id>". Null = no signal.
//
// TERMINAL statuses are the FULL Claude Code set — completed / failed / stopped / killed
// (mirror of daemon/native_claude.go:218, which passes tn.Status through verbatim).
// Matching only completed|failed left a shell that was STOPPED or KILLED — the harness
// terminating it, a reap on a steer/interrupt — permanently un-annotated: its completion
// arrived as `<status>stopped</status>`, so the exit was never recorded and both the
// running→exited badge AND the "waiting on N background shells" footer stuck FOREVER (a
// real bug: a self-expiring background poller showed "waiting on 2" long after it died).
// `running` (an interim BashOutput poll on a still-live shell) is deliberately EXCLUDED
// so a mid-flight poll never reads as exited.
function bashExitFromResult(content: string, bashId: string): { exitCode?: number } | null {
  if (!content.includes(bashId)) return null;
  if (new RegExp(`No task found with ID:\\s*${bashId}\\b`, 'i').test(content)) return {};
  if (/<status>\s*(?:completed|failed|stopped|killed)\s*<\/status>/i.test(content)) {
    // <exit_code>N</exit_code> (tool result) OR "exit code N" (task-notification summary).
    const m = content.match(/<exit_code>\s*(-?\d+)\s*<\/exit_code>/i) || content.match(/exit code\s+(-?\d+)/i);
    return { exitCode: m ? parseInt(m[1], 10) : undefined };
  }
  return null;
}

// Module-level cache so an EXITED background shell yields a STABLE annotated-result
// REF across buildTranscript rebuilds. annotateBackgroundShells otherwise spreads a
// FRESH `{ ...launch, bgShell }` object every rebuild — and buildTranscript re-runs
// on every streamed token, so the group-view memo comparators (which compare result
// refs by identity) would re-render any group holding a settled bg shell on every
// token, defeating the memo for those groups. Keyed by callId; the stored ref is
// reused only when the SAME launch ref + exitCode reproduce the SAME derived value
// (so a genuinely new launch/exit still re-renders). Bounded like the diff cache.
const BG_ANNOT_CACHE_MAX = 256;
const bgAnnotCache = new Map<string, { launch: NativeEvent; exitCode?: number; annotated: NativeEvent }>();

// Annotate each background bash's launch result with `bgShell` when a later result
// reports its shell exited. Correlates by the shell id; keeps the LATEST signal.
function annotateBackgroundShells(events: NativeEvent[], resultByCallId: Map<string, NativeEvent>): void {
  const bg: { callId: string; bashId: string }[] = [];
  for (const e of events) {
    if (e.type !== 'tool_call' || !e.callId) continue;
    const a = e.args;
    if (!(a && typeof a === 'object' && (a as Record<string, unknown>).run_in_background)) continue;
    const launch = resultByCallId.get(e.callId);
    const id = launch?.content ? extractBashId(launch.content) : null;
    if (id) bg.push({ callId: e.callId, bashId: id });
  }
  if (!bg.length) return;
  for (const { callId, bashId } of bg) {
    let exit: { exitCode?: number } | null = null;
    for (const e of events) {
      if (e.callId === callId) continue; // skip the launch result itself
      // A tool_result (the agent polled BashOutput/TaskOutput) OR a user_text
      // <task-notification> (the completion Claude Code injects even if it never polled).
      const c = e.type === 'tool_result' ? e.content : e.type === 'user_text' ? e.text : undefined;
      if (!c) continue;
      const hit = bashExitFromResult(c, bashId);
      if (hit) exit = hit; // LATEST completion signal wins
    }
    if (exit) {
      const launch = resultByCallId.get(callId);
      if (launch) {
        // Reuse the cached annotated object when the SAME launch ref + exitCode
        // already produced it, so the result REF is stable across rebuilds (the
        // group-view memo comparators can then skip a settled bg-shell group).
        // Only rebuild the spread when the derived value actually changed.
        const prev = bgAnnotCache.get(callId);
        let annotated: NativeEvent;
        if (prev && prev.launch === launch && prev.exitCode === exit.exitCode) {
          annotated = prev.annotated;
        } else {
          annotated = { ...launch, bgShell: { state: 'exited', exitCode: exit.exitCode } };
          if (bgAnnotCache.size >= BG_ANNOT_CACHE_MAX) bgAnnotCache.clear();
          bgAnnotCache.set(callId, { launch, exitCode: exit.exitCode, annotated });
        }
        resultByCallId.set(callId, annotated);
      }
    }
  }
}

// idleBackgroundShells — once a turn has SETTLED (not working), how many
// `run_in_background` bashes launched but have NOT reported completion (their launch
// result carries no `bgShell` exit annotation, the same running/exited test
// annotateBackgroundShells uses). >0 means the agent is PARKED: it ended the turn
// after kicking off detached shell(s) and is waiting — Claude Code re-prompts it via a
// <task-notification> when one finishes, so it resumes on its own IF the shell
// completes; a hung shell leaves it waiting indefinitely. Returns 0 while a turn is in
// flight (the spinner already conveys activity). This is the "done, but still tethered
// to a background job" signal that a bare "✓ worked for Xs" otherwise hides — the
// exact ambiguity behind "I can't tell if it's running or stuck". Pure + unit-tested.
export function idleBackgroundShells(
  events: NativeEvent[],
  resultByCallId: Map<string, NativeEvent>,
  working: boolean,
): number {
  if (working) return 0;
  let n = 0;
  for (const e of events) {
    if (e.type !== 'tool_call' || !e.callId) continue;
    // Only LIVE launches count. A run_in_background shell is a child of the agent
    // process, so it dies when the agent is resumed/respawned — and a resume
    // replays the whole prior transcript as fromReplay events. Counting those
    // re-tallies dead prior-session shells as a phantom "waiting on N" that never
    // clears (the bash exit was never recorded before the old session ended).
    // Trade-off: a pure client reconnect also re-feeds live launches as fromReplay,
    // so a genuinely-running shell briefly stops counting — benign (a missing chip,
    // not a phantom one), and the next live event re-surfaces it.
    if (e.fromReplay) continue;
    const a = e.args;
    if (!(a && typeof a === 'object' && (a as Record<string, unknown>).run_in_background)) continue;
    if (!resultByCallId.get(e.callId)?.bgShell) n++; // launched, no exit signal yet → still running
  }
  return n;
}

// idleBackgroundAgents — the Agent-tool sibling of idleBackgroundShells: once the
// turn has settled, how many background subagents launched but haven't completed.
// Detection is result-shape-driven, not tool-name-driven (works for Agent/Task or
// a rename): a background launch's tool_result says "Async agent launched
// successfully" and carries `agentId: <id>`; completion arrives as the
// <task-notification> user_text Claude Code injects, carrying <task-id> (== the
// agentId) and <tool-use-id> (== the launch callId) — correlate on either. A
// plain content.includes(agentId) is NOT enough: the parent can SendMessage the
// agent mid-run, and that result quotes the id while the agent is still alive.
// Caveat: an agent whose notification never arrives (killed with its parent
// claude) would count forever — the fromReplay skip below covers that, since a
// respawn replays the launch as fromReplay. Pure + unit-tested.
export function idleBackgroundAgents(
  events: NativeEvent[],
  resultByCallId: Map<string, NativeEvent>,
  working: boolean,
): number {
  if (working) return 0;
  let n = 0;
  for (const e of events) {
    if (e.type !== 'tool_call' || !e.callId) continue;
    // Same live-launch-only rule as idleBackgroundShells: subagents are children
    // of the claude process, so they die with it — replayed launches from a prior
    // session must not re-tally as phantom "waiting".
    if (e.fromReplay) continue;
    const launch = resultByCallId.get(e.callId)?.content;
    if (!launch || !/Async agent launched successfully/i.test(launch)) continue;
    const idMatch = launch.match(/agentId:\s*([A-Za-z0-9_-]+)/i);
    if (!idMatch) continue;
    const agentId = idMatch[1];
    const taskIdTag = `<task-id>${agentId}</task-id>`;
    const toolUseTag = `<tool-use-id>${e.callId}</tool-use-id>`;
    let done = false;
    for (const later of events) {
      // The completion notification is a user_text; a TaskOutput-style poll that
      // reports the reaped task ("No task found with ID") also proves it's gone.
      const c = later.type === 'user_text' ? later.text : later.type === 'tool_result' ? later.content : undefined;
      if (!c) continue;
      if (c.includes(taskIdTag) || c.includes(toolUseTag) || new RegExp(`No task found with ID:\\s*${agentId}\\b`, 'i').test(c)) {
        done = true;
        break;
      }
    }
    if (!done) n++;
  }
  return n;
}

// Pair tool_result events to their tool_call by callId (the call card renders its
// result inline), hoist usage/rate_limit to the footer, drop envelope-only events
// (session_init/turn_end), suppress the duplicate TodoWrite tool card (the `todo`
// event renders the plan instead), and insert a turn separator before each
// user_text turn after the first rendered row. Returns rows in transcript order.
//
// agentMidTurn is the daemon's authoritative "is a turn in flight" flag (the DTO
// field). When it is === false, a tool_call still missing its tool_result is a
// GHOST: the turn settled but that call's result will never arrive (the headless
// agent was respawned mid-turn, e.g. by an auto-update). We fabricate an
// `interrupted` tool_result for it so the row flips from a perpetual "running…"
// spinner to a quiet "interrupted" state. Left undefined (old daemon) or true
// (live turn) → no reconciliation, so a genuinely-running tool keeps spinning.
export function buildTranscript(events: NativeEvent[], agentMidTurn?: boolean): Transcript {
  const resultByCallId = new Map<string, NativeEvent>();
  let usage: NativeEvent | undefined;
  let rateLimit: NativeEvent | undefined;
  let cost: number | undefined;
  // True once a /compact boundary (or a /clear session rotation) is seen with no
  // context-bearing usage after it — the trailing `usage` is then a stale reading
  // the metabar must ignore.
  let contextCompacted = false;
  // First session_id seen. A LATER session_init carrying a DIFFERENT id means the
  // agent rotated its session, which only happens on /clear (a respawn resets the
  // pane epoch + event ring instead, so it never double-inits into the same ring).
  let lastSessionId: string | undefined;
  // callIds of suppressed TodoWrite tool_calls, so we can also drop their paired
  // tool_result (which would otherwise survive as an orphaned result row).
  const todoCallIds = new Set<string>();
  for (const e of events) {
    if (isTodoWriteCall(e) && e.callId) todoCallIds.add(e.callId);
  }
  for (const e of events) {
    // A TodoWrite's result is suppressed alongside its call (the `todo` event
    // carries the plan), so don't register it in resultByCallId either.
    if (e.type === 'tool_result' && e.callId && !todoCallIds.has(e.callId))
      resultByCallId.set(e.callId, e);
    else if (e.type === 'usage' && !e.parentCallId) {
      // Only MAIN-thread usage drives the footer ctx%. A parentCallId-tagged usage
      // is a sub-agent/Task turn measuring the subagent's own (separate) context
      // window — adopting it would flicker the metabar to the subagent's window
      // mid-Task, then snap back when the main thread resumes.
      const occ = (e.inputTokens || 0) + (e.cachedInputTokens || 0) + (e.cacheCreationTokens || 0);
      if (occ > 0) {
        // Context-bearing usage (an assistant-message turn re-measured the window):
        // adopt fully, and supersede any prior compact/clear reset.
        usage = e;
        contextCompacted = false;
      } else if (usage) {
        // Reasoning-only usage with NO window reading (occ 0) — notably GLM's
        // streaming system/thinking_tokens pings, which arrive by the hundreds and
        // carry reasoningTokens but no input/cache. A plain `usage = e` here ZEROED
        // the last context reading, so occ→0 and the ctx% chip vanished for GLM
        // Focus panes. Instead KEEP the last context-bearing usage (window intact)
        // and only fold in the fresh reasoning count.
        usage = { ...usage, reasoningTokens: e.reasoningTokens ?? usage.reasoningTokens };
      } else {
        // No context-bearing usage seen yet (pre-first-turn): keep the reasoning-only
        // event so a reasoning display still works; occ stays 0 so ctx% stays hidden
        // (correct — there's no window reading to show yet).
        usage = e;
      }
    } else if (e.type === 'slash_result' && e.subtype === 'compact') contextCompacted = true;
    else if (e.type === 'session_init') {
      // /clear frees the WHOLE window but emits no fresh usage —
      // the trailing `usage` is a stale pre-clear reading. The CLI rotates to a new
      // session_id on /clear; flag the rotation exactly like a compact boundary so
      // the ctx% chip resets (and NativePane drops the persisted snapshot) until
      // the next turn re-measures. A re-emitted init with the SAME id (a reconnect
      // replay) is not a rotation. Cleared by the next context-bearing usage above.
      if (lastSessionId && e.sessionId && e.sessionId !== lastSessionId) contextCompacted = true;
      if (e.sessionId) lastSessionId = e.sessionId;
    } else if (e.type === 'rate_limit') rateLimit = e;
    else if (e.type === 'turn_end' && e.totalCostUsd != null) cost = e.totalCostUsd;
  }
  // Ghost-spinner reconciliation (see header): only when the daemon says the turn
  // has settled. A result-less tool_call then gets a synthetic `interrupted`
  // result so `!result` no longer reads as "running…". isError is left unset, so
  // isErroredResult/isUserDenial both stay false — interrupted is NOT a failure.
  if (agentMidTurn === false) {
    for (const e of events) {
      if (e.type !== 'tool_call' || !e.callId) continue;
      if (isTodoWriteCall(e)) continue; // dropped from rows anyway; never spins
      if (resultByCallId.has(e.callId)) continue; // real result already paired
      resultByCallId.set(e.callId, {
        type: 'tool_result',
        seq: e.seq,
        paneId: e.paneId,
        fromReplay: e.fromReplay,
        callId: e.callId,
        interrupted: true,
      });
    }
  }
  // Background-shell live status: flip a `run_in_background` bash's badge from running
  // to "exited <code>" once a later BashOutput / task-output result reports it done.
  annotateBackgroundShells(events, resultByCallId);
  // Sub-agent nesting: collect nested tool_calls (parentCallId set) under their
  // parent Task's callId, but ONLY when that parent is actually present in this
  // slice — so an orphaned child (parent aged out of the ring) still renders
  // top-level instead of vanishing. The nested calls are then hidden from the
  // top-level rows below (TaskView renders them indented).
  const topLevelCallIds = new Set<string>();
  for (const e of events) {
    if (e.type === 'tool_call' && e.callId && !e.parentCallId) topLevelCallIds.add(e.callId);
  }
  const childrenByParent = new Map<string, NativeEvent[]>();
  for (const e of events) {
    if (e.type === 'tool_call' && e.parentCallId && topLevelCallIds.has(e.parentCallId)) {
      const arr = childrenByParent.get(e.parentCallId) ?? [];
      arr.push(e);
      childrenByParent.set(e.parentCallId, arr);
    }
  }
  const visible = events.filter((e) => {
    // A nested sub-agent tool_call renders INSIDE its parent Task (childrenByParent),
    // not as a top-level row — but only when the parent is present (else the orphan
    // would vanish; keep it top-level).
    if (e.type === 'tool_call' && e.parentCallId && topLevelCallIds.has(e.parentCallId)) return false;
    // Suppress the TodoWrite tool_call card AND its paired tool_result — the
    // dedicated `todo` event (TodoView) renders the plan in its place. Other
    // tools' call/result pairing is untouched.
    if (isTodoWriteCall(e)) return false;
    if (e.type === 'tool_result' && e.callId && todoCallIds.has(e.callId)) return false;
    // A claude API failure ("API Error: 529 …") arrives as BOTH an assistant text
    // block AND a paired `error` event; drop the raw text duplicate so only the
    // single ErrorView banner shows (friendly + retryable for transient overloads).
    if (e.type === 'text' && isApiErrorText(e.text)) return false;
    // A <task-notification> is Claude Code's background-task completion notice, injected
    // as a USER-role message — NOT a real user turn. Rendering it as a prompt bubble (with
    // its own turn separator) created PHANTOM user turns that buried the actual prompt
    // ("my prompt is nowhere to be seen"). Hide it: the background badge already conveys
    // the completion, and annotateBackgroundShells reads it for the running→exited status.
    if (e.type === 'user_text' && /^\s*<task-notification/.test(e.text || '')) return false;
    // A RESOLVED permission_request renders NOTHING (NativePane only draws PENDING
    // approvals, via pendingApprovalSeqs) — once its tool ran, resultByCallId has the
    // callId (auto-approved/allowed/denied/interrupted all land a result). Keeping it
    // as an invisible row split an otherwise-consecutive tool/edit run in groupRows:
    // an auto-approving pane emits ONE permission_request per edit, so "4 edits" came
    // out as 4 ungrouped singletons (each rendering inline) instead of one edit-group.
    // Drop the resolved ones so grouping sees the real adjacency; PENDING stay (the card).
    if (e.type === 'permission_request' && e.callId && resultByCallId.has(e.callId)) return false;
    switch (e.type) {
      case 'tool_result': // folded into its tool_call card
      case 'usage': // footer
      case 'rate_limit': // footer
      case 'session_init': // header chip
      case 'turn_end': // boundary only
      case 'status': // spinner-only (computeNativeWorking reads raw events) — no row
        // renderer (NativePane default→null); kept-as-a-row only SPLIT runs in groupRows.
      case 'prompt_suggestion': // composer ghost-text source (derivePromptSuggestion reads the ring) — never a row
      case 'permission_resolved': // pending-card lifecycle edge; never a transcript row
        return false;
      default:
        return true;
    }
  });
  const rows: RenderRow[] = [];
  for (const e of visible) {
    // A user turn that isn't the first rendered row opens with a separator,
    // carrying the turn's client arrival time (live turns only) for the label.
    if (e.type === 'user_text' && rows.length > 0) {
      rows.push({
        kind: 'turn-sep',
        key: `sep-${e.seq}`,
        clientTs: e.clientTs,
        fromReplay: e.fromReplay,
      });
    }
    rows.push({ kind: 'event', event: e });
  }
  return { rows, resultByCallId, childrenByParent, usage, rateLimit, cost, contextCompacted };
}

// isUserDenial — a user-initiated deny (Deny button / Esc / interrupt) comes back
// as a tool_result with is_error=true whose content is the daemon's deny string.
// It's a deliberate rejection, NOT a failure, so it must never be counted/painted
// as an error. The regular-tool deny UI has no custom-reason field, so the daemon
// always emits a known string: an empty tool deny defaults to "The user denied
// this request." and an interrupt to "interrupted". Shared by ToolCallView (card
// styling) and groupRows/isErroredResult (the collapsed "N failed" pip), so the
// two can never diverge.
export function isUserDenial(content?: string): boolean {
  if (!content) return false;
  const c = content.trim();
  return /^the user denied this request\.?$/i.test(c) || c === 'interrupted';
}

// isErroredResult — did a tool_call's paired result genuinely FAIL? True only for
// a real error result, never a user denial. (Question results also carry
// isError:true, but AskUserQuestion calls are never grouped, so callers in the
// grouping path don't need the question guard.)
export function isErroredResult(result?: NativeEvent): boolean {
  return !!result && result.isError === true && !isUserDenial(result.content);
}

// isApiErrorText — the claude CLI surfaces an API failure as an assistant text
// block prefixed "API Error: …" AND a paired `error` event (the result→error
// mapping). The text block is a raw DUPLICATE of the error event, so buildTranscript
// drops it and lets ErrorView render the single (and, for overloads, friendly +
// retryable) banner. Matches only the CLI's envelope prefix, never the model's own
// prose that might happen to mention an error.
export function isApiErrorText(text?: string): boolean {
  return !!text && /^\s*API Error\b/i.test(text);
}

// isTransientOverload — is this error a temporary, server-side, retry-soon condition
// (Anthropic overload / rate limit / 5xx) rather than a real failure? Drives
// ErrorView's calm "temporary — your prompt is intact" banner instead of the hard
// red error. Checks BOTH the message and the code: the daemon carries the result
// string in `message` and the turn subtype in `code`, and a 529's "529 Overloaded"
// marker can land in either.
export function isTransientOverload(message?: string, code?: string): boolean {
  const s = `${message ?? ''} ${code ?? ''}`.toLowerCase();
  if (!s.trim()) return false;
  return (
    /\b(429|503|529)\b/.test(s) ||
    s.includes('overloaded') ||
    s.includes('rate limit') ||
    s.includes('rate_limit') ||
    s.includes('service unavailable') ||
    s.includes('temporar') // "temporary" / "temporarily"
  );
}

// groupRows — the "terse feed" pass. Folds EVERY run of adjacent tool
// calls — INCLUDING a run of one — into a `tool-group` row, and each run of ≥2
// adjacent thinking blocks into a `thinking-group`. Even a lone tool call collapses
// to a compact "$ bash" / "⎘ Read" toggle that hides its (often long, technical)
// command/args by default — one click expands it — so a single bash never dumps a
// pipeline into the feed. (Thinking stays ≥2 because a lone thinking block already
// collapses via ThinkingView.) File-edit tools NEVER fold (see isUngroupableTool):
// the diff is the deliverable, so a Write/Edit always shows inline. Runs break on
// anything that isn't a groupable event of the same kind — prose, a user turn, an
// error, a todo, a turn separator, a file-edit, or an AskUserQuestion (those last
// two are hard boundaries that stay standalone — the edit so its diff is visible,
// the question so NativePane's approval-suppression guard still sees it).
//
// Runs on the post-/clear RenderRow[] (not raw events), so a group can only ever
// contain already-visible events — the /clear filter never has to descend into a
// group. Pure: it re-derives from scratch on every render.

// isUngroupableTool — tool calls that must NEVER fold into a tool-group; they stay
// standalone (as singles AND in batches) so their high-signal content is always
// visible. The principle is "collapse the MEANS, surface the RESULT": bash/read/
// grep/etc. are how the agent works (fold them), but a file edit IS the deliverable
// — its one-line `path +N −M` diff is the most useful token in the feed, so it's
// always shown inline (click for the full diff). AskUserQuestion is here too (it's
// interactive — the approval gate must see it). Task/Agent too — a Task owns a nested
// sub-thread (TaskView), so it must stay a standalone row, never swept into a sibling
// tool-group. Each acts as a run boundary, like prose.
function isUngroupableTool(e: NativeEvent): boolean {
  const name = e.name || e.toolName || '';
  return (
    name === 'AskUserQuestion' ||
    name === 'Task' ||
    name === 'Agent' ||
    /^(Write|Edit|MultiEdit|NotebookEdit)$/.test(name)
  );
}

// isEditTool — a file-edit tool_call (Write/Edit/MultiEdit/NotebookEdit). A LONE edit
// still renders inline so its `path +N −M` diff is visible (the v0.0.603 principle),
// but a RUN of ≥2 CONSECUTIVE edits consolidates into one `edit-group` (collapsed
// "✎ N edits · files · +X −Y", expand for each diff) so a sequence of edits no longer
// stacks into a wall of repeated "Edit" cards. Handled BEFORE the generic tool branch
// in groupRows, so edits never mix into a bash tool-group; a thinking block or any
// non-edit between two edits breaks the run (they must be sequential).
function isEditTool(e: NativeEvent): boolean {
  if (e.type !== 'tool_call') return false;
  const name = e.name || e.toolName || '';
  return /^(Write|Edit|MultiEdit|NotebookEdit)$/.test(name);
}

// isThinking / isGroupableTool — the two members of a "step" run (see groupRows).
function isThinking(e: NativeEvent): boolean {
  return e.type === 'thinking' || e.type === 'thinking_delta';
}
function isGroupableTool(e: NativeEvent): boolean {
  // isUngroupableTool already excludes edits + AskUserQuestion + Task/Agent.
  return e.type === 'tool_call' && !isUngroupableTool(e);
}

export function groupRows(rows: RenderRow[]): RenderRow[] {
  const out: RenderRow[] = [];
  // A single ORDERED "step" run holds INTERLEAVED thinking + groupable tool calls, so
  // the think→tool→think→tool ladder collapses into ONE group instead of a tall stack
  // of alternating rows. The MEANS-vs-CONCLUSION split is decided at flush time by
  // peeling TRAILING thinking (a thinking NOT followed by a tool = it precedes the
  // prose reply, so it's the conclusion's reasoning → stays visible). Edits keep their
  // own run (a hard boundary; never swept into a step-group).
  let steps: NativeEvent[] = [];
  let edits: NativeEvent[] = [];
  const flushSteps = () => {
    let end = steps.length;
    while (end > 0 && isThinking(steps[end - 1])) end--;
    const core = steps.slice(0, end); // ends at a tool (or empty)
    const trailing = steps.slice(end); // 0+ trailing thinking blocks (conclusion reasoning)
    steps = [];
    if (core.length > 0) {
      // Pure-tool core keeps the existing tool-group shape (no behavior change for a
      // tool run with no interstitial thinking); a mixed think+tool core = step-group.
      out.push(
        core.some(isThinking)
          ? { kind: 'step-group', key: `sgrp-${core[0].seq}`, events: core }
          : { kind: 'tool-group', key: `tgrp-${core[0].seq}`, events: core },
      );
    }
    // Trailing thinking keeps its existing semantics: ≥2 → thinking-group, 1 → lone row.
    if (trailing.length >= 2) out.push({ kind: 'thinking-group', key: `thgrp-${trailing[0].seq}`, events: trailing });
    else if (trailing.length === 1) out.push({ kind: 'event', event: trailing[0] });
  };
  const flushEdits = () => {
    // ≥2 consecutive edits consolidate into one collapsed group; a LONE edit stays
    // inline so its diff is visible (collapse the means, surface the result).
    if (edits.length >= 2) out.push({ kind: 'edit-group', key: `egrp-${edits[0].seq}`, events: edits });
    else if (edits.length === 1) out.push({ kind: 'event', event: edits[0] });
    edits = [];
  };
  for (const row of rows) {
    if (row.kind !== 'event') {
      flushSteps();
      flushEdits();
      out.push(row);
      continue;
    }
    const e = row.event;
    if (isThinking(e)) {
      // Provisionally a step; flushSteps() decides at run-end whether it folds (a tool
      // follows it) or stays visible (it trails → precedes prose/EOF = conclusion).
      flushEdits();
      steps.push(e);
    } else if (isEditTool(e)) {
      // A file edit — its own run (a hard boundary for the step run), so a sequence of
      // edits consolidates while a lone one stays inline.
      flushSteps();
      edits.push(e);
    } else if (isGroupableTool(e)) {
      flushEdits();
      steps.push(e);
    } else {
      // Prose / user turn / todo / error / a permission request / a Task or
      // AskUserQuestion tool_call — a hard boundary, emitted unchanged (questions
      // stay standalone so the approval gate sees them, Tasks own a nested thread).
      flushSteps();
      flushEdits();
      out.push(row);
    }
  }
  flushSteps();
  flushEdits();
  return out;
}

// relativeTime — "just now / 42s ago / 6m ago / 3h ago / 2d ago" from a delta in
// ms. Mirrors the terminal reader-chip formatter so the two read identically.
// `now` is injectable for deterministic tests.
export function relativeTime(ts: number | undefined, now: number = Date.now()): string {
  if (ts == null) return 'earlier';
  const deltaMs = now - ts;
  if (deltaMs < 0) return 'just now';
  if (deltaMs < 5000) return 'just now';
  const s = Math.floor(deltaMs / 1000);
  if (s < 60) return `${s}s ago`;
  const m = Math.floor(s / 60);
  if (m < 60) return `${m}m ago`;
  const h = Math.floor(m / 60);
  if (h < 24) return `${h}h ago`;
  return `${Math.floor(h / 24)}d ago`;
}

// fmtDuration — a compact tool-call duration ("420ms / 1.4s / 2m 5s") from a ms
// delta, or '' when we can't measure it (e.g. either endpoint was replayed).
export function fmtDuration(ms: number | undefined): string {
  if (ms == null || !Number.isFinite(ms) || ms < 0) return '';
  if (ms < 1000) return `${Math.round(ms)}ms`;
  const s = ms / 1000;
  if (s < 60) return `${s < 10 ? s.toFixed(1) : Math.round(s)}s`;
  const m = Math.floor(s / 60);
  const rem = Math.round(s - m * 60);
  return rem ? `${m}m ${rem}s` : `${m}m`;
}

// lastTurnDuration — the wall-clock duration (ms) of the MOST RECENT completed
// turn, read from the latest turn_end's durationMs. Drives the "✓ worked for Xs"
// resting status (the terminal-style "we know Claude is done" cue). undefined
// when no turn has completed yet (or the turn_end carried no duration, e.g. a
// replayed-history end). Pure + scans newest→oldest so it's O(tail) in practice.
export function lastTurnDuration(events: NativeEvent[]): number | undefined {
  for (let i = events.length - 1; i >= 0; i--) {
    if (events[i].type === 'turn_end') return events[i].durationMs;
  }
  return undefined;
}

// A fabricated "requesting" status used to light the spinner when there's no
// real status event to render (an optimistic pending row, or the event-less
// model-generation gap after a tool_result).
function fabricatedStatus(paneId: string): NativeEvent {
  return { type: 'status', subtype: 'requesting', seq: -1, paneId, fromReplay: false };
}

// computeNativeWorking — is a turn in flight, and if so what status drives the
// spinner? Returns the status NativeEvent (for its detail copy) or a fabricated
// sentinel when working, else null. Pure so it's unit-testable.
//
// The daemon's nativeInTurn flag (`agentMidTurn`) is the AUTHORITY on whether a
// turn is in flight; the event-scan only supplies the spinner's status DETAIL and
// a hard turn_end/error settle. Precedence, scanning back to the last user_text:
//   1. a pending optimistic row → we just sent, nothing's back → working.
//   2. a TRAILING turn_end / error (only usage after it) → SETTLED (null). (The
//      daemon clears agentMidTurn on these too, so the two signals agree.) A
//      non-trailing turn_end is a self-started turn's boundary — see below.
//   3. a `status` event → drives the spinner detail.
//   4. else → trust agentMidTurn: true ⇒ working (fabricated status), false ⇒ idle.
//
// A bare `usage` is DELIBERATELY NOT a settle signal: Claude emits usage events
// mid-turn (after every tool round, often in BURSTS), so the old "trailing usage
// settles" rule blanked the spinner whenever the latest event happened to be one
// of those interim usages — the flicker on every multi-tool turn. We only fall
// back to that heuristic for an OLD daemon that doesn't report agentMidTurn
// (undefined), to preserve its behavior.
//
// A turn_end hard-settles ONLY when it's trailing (nothing but usage after it).
// Claude starts turns WITHOUT a user_text: after backgrounding an Agent it ends
// the launch segment (turn_end) and immediately keeps working — turn_end followed
// directly by tool_call/thinking, no user event until the <task-notification>
// much later. Settling on any turn_end found while scanning back read that whole
// continuation as "done": a dead "✓ worked for Xs" under minutes of live
// streaming (the 2026-07-09 report). A turn_end with non-usage events after it is
// just this turn's START boundary — stop the scan there and let the daemon flag
// decide, exactly as if the boundary were a user_text.
//
// Known edge: the deny-with-reason AskUserQuestion path emits no turn_end and
// leaves nativeInTurn stuck true, so the spinner can linger after a declined
// question until the next turn — rare and transient (the user is mid-interaction;
// the next turn_end clears it), and far better than blanking every multi-tool turn.
export function computeNativeWorking(
  events: NativeEvent[],
  pendingCount: number,
  agentMidTurn: boolean | undefined,
  paneId: string,
): NativeEvent | null {
  if (pendingCount > 0) return fabricatedStatus(paneId);
  let status: NativeEvent | null = null;
  let seenLater = false; // any non-usage event AFTER the candidate boundary
  for (let i = events.length - 1; i >= 0; i--) {
    const e = events[i];
    if (e.type === 'turn_end' || e.type === 'error') {
      if (!seenLater) return null; // trailing → hard settle
      break; // events after it are a self-started turn — this is its boundary
    }
    // Trailing-usage settle: OLD-daemon fallback only. When the daemon reports
    // agentMidTurn we trust it instead (a usage fires mid-turn, so it must not
    // settle a turn the daemon says is still live).
    if (e.type === 'usage' && !seenLater && agentMidTurn === undefined) return null;
    if (e.type === 'status') status = status ?? e;
    if (e.type === 'user_text') break; // reached the turn boundary
    if (e.type !== 'usage') seenLater = true; // interim usage doesn't count as "later"
  }
  if (agentMidTurn === false) return null; // daemon authority: settled
  if (agentMidTurn === true) return status ?? fabricatedStatus(paneId); // working
  // Old daemon (agentMidTurn undefined): the trailing-usage early-return already
  // settled the no-status idle case, so preserve the exact pre-agentMidTurn
  // behavior — working iff we found a status this turn.
  return status;
}
