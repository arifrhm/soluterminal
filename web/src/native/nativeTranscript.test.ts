// Unit tests for the pure transcript-shaping helpers: buildTranscript's
// row-grouping + turn-separator insertion, and the relative-time / duration
// formatters that drive the scannable turn boundaries.

import { describe, it, expect } from 'vitest';
import {
  buildTranscript,
  groupRows,
  isUserDenial,
  isErroredResult,
  isApiErrorText,
  isTransientOverload,
  relativeTime,
  fmtDuration,
  computeNativeWorking,
  lastTurnDuration,
  idleBackgroundShells,
  idleBackgroundAgents,
} from './nativeTranscript';
import type { NativeEvent } from './nativeEvents';

function ev(partial: Partial<NativeEvent> & { type: NativeEvent['type']; seq: number }): NativeEvent {
  return { paneId: 'p', fromReplay: false, ...partial } as NativeEvent;
}

describe('background-shell status (annotateBackgroundShells)', () => {
  const bgBash = (seq: number, callId: string) =>
    ev({ type: 'tool_call', seq, callId, name: 'Bash', toolKind: 'exec', args: { command: 'sleep 90', run_in_background: true } });
  const launch = (seq: number, callId: string, id: string) =>
    ev({ type: 'tool_result', seq, callId, content: `Command running in background with ID: ${id}. Output is being written to: /tmp/x/tasks/${id}.output. You will be notified when it completes.` });
  const taskOut = (seq: number, callId: string, content: string) =>
    ev({ type: 'tool_result', seq, callId, content });

  it('flips a background bash to exited 0 when a completed output result is seen', () => {
    const { resultByCallId } = buildTranscript([
      bgBash(1, 'bg1'),
      launch(2, 'bg1', 'abc123'),
      ev({ type: 'tool_call', seq: 3, callId: 'bo1', name: 'BashOutput', args: { bash_id: 'abc123' } }),
      taskOut(4, 'bo1', '<retrieval_status>success</retrieval_status>\n<task_id>abc123</task_id>\n<status>completed</status>\n<exit_code>0</exit_code>\n<output>done</output>'),
    ]);
    expect(resultByCallId.get('bg1')?.bgShell).toEqual({ state: 'exited', exitCode: 0 });
  });

  it('captures a NON-ZERO exit code', () => {
    const { resultByCallId } = buildTranscript([
      bgBash(1, 'bg1'),
      launch(2, 'bg1', 'abc123'),
      taskOut(4, 'bo1', '<task_id>abc123</task_id>\n<status>failed</status>\n<exit_code>2</exit_code>'),
    ]);
    expect(resultByCallId.get('bg1')?.bgShell).toEqual({ state: 'exited', exitCode: 2 });
  });

  it('treats a reaped task ("No task found") as exited with no code', () => {
    const { resultByCallId } = buildTranscript([
      bgBash(1, 'bg1'),
      launch(2, 'bg1', 'abc123'),
      taskOut(4, 'bo1', '<tool_use_error>No task found with ID: abc123</tool_use_error>'),
    ]);
    expect(resultByCallId.get('bg1')?.bgShell).toEqual({ state: 'exited' });
  });

  it('leaves bgShell UNSET when no completion signal is seen (badge stays "running")', () => {
    const { resultByCallId } = buildTranscript([bgBash(1, 'bg1'), launch(2, 'bg1', 'abc123')]);
    expect(resultByCallId.get('bg1')?.bgShell).toBeUndefined();
  });

  it('does not annotate a normal (foreground) bash', () => {
    const { resultByCallId } = buildTranscript([
      ev({ type: 'tool_call', seq: 1, callId: 'fg', name: 'Bash', args: { command: 'ls' } }),
      ev({ type: 'tool_result', seq: 2, callId: 'fg', content: 'a b c' }),
    ]);
    expect(resultByCallId.get('fg')?.bgShell).toBeUndefined();
  });

  it('reads a <task-notification> as a completion signal even without a BashOutput poll', () => {
    const { resultByCallId } = buildTranscript([
      bgBash(1, 'bg1'),
      launch(2, 'bg1', 'abc123'),
      ev({
        type: 'user_text',
        seq: 3,
        text: '<task-notification> <task-id>abc123</task-id> <status>failed</status> <summary>Background command "B" failed with exit code 7</summary> </task-notification>',
      }),
    ]);
    expect(resultByCallId.get('bg1')?.bgShell).toEqual({ state: 'exited', exitCode: 7 });
  });

  it('treats a <status>stopped</status> task-notification as exited (harness terminated it)', () => {
    // Regression for the "waiting on N background shells" phantom: a self-expiring
    // background poller is STOPPED (not "completed"), so its notification carries
    // <status>stopped</status>. Recognizing only completed|failed left it un-annotated
    // and the footer stuck forever. task-id == the bash id (the real wire shape).
    const { resultByCallId } = buildTranscript([
      bgBash(1, 'bg1'),
      launch(2, 'bg1', 'abc123'),
      ev({
        type: 'user_text',
        seq: 3,
        text: '<task-notification>\n<task-id>abc123</task-id>\n<status>stopped</status>\n<summary>Run pairing harvester</summary>\n</task-notification>',
      }),
    ]);
    // Stop carries no exit code → exited with no code (same shape as a reap).
    expect(resultByCallId.get('bg1')?.bgShell).toEqual({ state: 'exited' });
  });

  it('treats a <status>killed</status> task-notification as exited', () => {
    const { resultByCallId } = buildTranscript([
      bgBash(1, 'bg1'),
      launch(2, 'bg1', 'abc123'),
      ev({
        type: 'user_text',
        seq: 3,
        text: '<task-notification>\n<task-id>abc123</task-id>\n<status>killed</status>\n</task-notification>',
      }),
    ]);
    expect(resultByCallId.get('bg1')?.bgShell).toEqual({ state: 'exited' });
  });

  it('does NOT treat a <status>running</status> interim poll as exited', () => {
    // An in-flight BashOutput poll reports running — the badge must stay "running".
    const { resultByCallId } = buildTranscript([
      bgBash(1, 'bg1'),
      launch(2, 'bg1', 'abc123'),
      taskOut(4, 'bo1', '<task_id>abc123</task_id>\n<status>running</status>\n<output>…</output>'),
    ]);
    expect(resultByCallId.get('bg1')?.bgShell).toBeUndefined();
  });

  it('returns a STABLE annotated-result ref across rebuilds for an exited bg shell', () => {
    // Regression guard for the annotate cache: an unchanged exited shell must yield
    // the SAME annotated object on a rebuild (same `events` refs), so the group-view
    // memo comparators (which compare result refs by identity) can skip a settled
    // bg-shell group instead of re-rendering it on every streamed token. Pass the
    // SAME events array both times so the launch ref is stable (the cache key).
    const events = [
      bgBash(1, 'bg1'),
      launch(2, 'bg1', 'abc123'),
      taskOut(4, 'bo1', '<task_id>abc123</task_id>\n<status>completed</status>\n<exit_code>0</exit_code>'),
    ];
    const first = buildTranscript(events).resultByCallId.get('bg1');
    const second = buildTranscript(events).resultByCallId.get('bg1');
    expect(first?.bgShell).toEqual({ state: 'exited', exitCode: 0 });
    expect(second).toBe(first); // same REF — not a fresh spread each rebuild
  });
});

describe('task-notification rows (hidden, never user prompts)', () => {
  it('hides a <task-notification> user message but KEEPS the real prompt', () => {
    const { rows } = buildTranscript([
      ev({ type: 'user_text', seq: 1, text: 'do the thing' }),
      ev({
        type: 'user_text',
        seq: 2,
        text: '<task-notification> <task-id>x</task-id> <status>completed</status> </task-notification>',
      }),
    ]);
    const userTexts = rows
      .filter((r) => r.kind === 'event' && r.event.type === 'user_text')
      .map((r) => (r.kind === 'event' ? r.event.text : ''));
    expect(userTexts).toEqual(['do the thing']); // prompt stays; the notification is gone
  });

  it('does not hide a normal user prompt', () => {
    const { rows } = buildTranscript([ev({ type: 'user_text', seq: 1, text: 'normal prompt' })]);
    expect(
      rows.some((r) => r.kind === 'event' && r.event.type === 'user_text' && r.event.text === 'normal prompt'),
    ).toBe(true);
  });
});

describe('buildTranscript', () => {
  it('drops footer/envelope events and pairs results to calls', () => {
    const events: NativeEvent[] = [
      ev({ type: 'session_init', seq: 1, model: 'sonnet' }),
      ev({ type: 'user_text', seq: 2, text: 'hi' }),
      ev({ type: 'tool_call', seq: 3, callId: 'c1', name: 'Bash' }),
      ev({ type: 'tool_result', seq: 4, callId: 'c1', content: 'ok' }),
      ev({ type: 'usage', seq: 5, inputTokens: 10 }),
      ev({ type: 'rate_limit', seq: 6, resetsAt: 123 }),
      ev({ type: 'turn_end', seq: 7, subtype: 'success' }),
    ];
    const t = buildTranscript(events);
    // Only the user_text + tool_call survive as event rows (no separator: the
    // user turn is the FIRST rendered row).
    const eventRows = t.rows.filter((r) => r.kind === 'event');
    expect(eventRows.map((r) => (r.kind === 'event' ? r.event.type : ''))).toEqual([
      'user_text',
      'tool_call',
    ]);
    expect(t.resultByCallId.get('c1')?.content).toBe('ok');
    expect(t.usage?.inputTokens).toBe(10);
    expect(t.rateLimit?.resetsAt).toBe(123);
    expect(t.contextCompacted).toBe(false);
  });

  it('ignores sub-agent (parentCallId) usage for the footer ctx%', () => {
    // With --include-partial-messages the CLI streams a Task's internal turns with
    // their own usage, tagged parent_tool_use_id. Adopting that would flicker the
    // metabar to the subagent's (separate, smaller) window mid-Task. The footer must
    // keep the main thread's last reading.
    const events: NativeEvent[] = [
      ev({ type: 'usage', seq: 1, inputTokens: 50_000 }), // main thread
      ev({ type: 'tool_call', seq: 2, callId: 'T', name: 'Task' }),
      ev({ type: 'usage', seq: 3, inputTokens: 9_000, parentCallId: 'T' }), // subagent turn
      ev({ type: 'usage', seq: 4, inputTokens: 9_200, parentCallId: 'T' }), // subagent turn
    ];
    const t = buildTranscript(events);
    expect(t.usage?.inputTokens).toBe(50_000);
  });

  it('keeps the last context-bearing window when reasoning-only usage trails (GLM ctx% bug)', () => {
    // GLM streams hundreds of system/thinking_tokens pings → main-thread usage
    // events that carry reasoningTokens but NO input/cache. A plain last-wins
    // adopt zeroed the window (occ→0) and hid the ctx% chip. The window reading
    // must survive; only the reasoning count updates.
    const events: NativeEvent[] = [
      ev({ type: 'usage', seq: 1, inputTokens: 5_000, cachedInputTokens: 100_000 }), // real window
      ev({ type: 'usage', seq: 2, reasoningTokens: 2_400 }), // GLM thinking ping (no input/cache)
      ev({ type: 'usage', seq: 3, reasoningTokens: 2_410 }),
    ];
    const t = buildTranscript(events);
    expect(t.usage?.inputTokens).toBe(5_000);
    expect(t.usage?.cachedInputTokens).toBe(100_000);
    expect(t.usage?.reasoningTokens).toBe(2_410); // latest reasoning count folded in
  });

  // An older /compact boundary without postTokens leaves the trailing `usage` as
  // the STALE pre-compact reading. The compact marker flags that safe fallback.
  it('flags contextCompacted when a compact boundary trails the last usage', () => {
    const events: NativeEvent[] = [
      ev({ type: 'user_text', seq: 1, text: 'hi' }),
      ev({ type: 'usage', seq: 2, inputTokens: 50_000, cachedInputTokens: 100_000 }),
      ev({ type: 'slash_result', seq: 3, subtype: 'compact', text: '✓ compacted' }),
    ];
    const t = buildTranscript(events);
    expect(t.contextCompacted).toBe(true);
    // usage is still hoisted (the token chip keeps the last-turn ↑↓); only the ctx
    // derivation is gated, in NativePane, on contextCompacted.
    expect(t.usage?.inputTokens).toBe(50_000);
  });

  it('clears contextCompacted once a context-bearing usage lands after the compact', () => {
    const events: NativeEvent[] = [
      ev({ type: 'usage', seq: 1, inputTokens: 50_000, cachedInputTokens: 100_000 }),
      ev({ type: 'slash_result', seq: 2, subtype: 'compact', text: '✓ compacted' }),
      ev({ type: 'user_text', seq: 3, text: 'next' }),
      ev({ type: 'usage', seq: 4, inputTokens: 2, cacheCreationTokens: 8_000 }), // post-compact re-measure
    ];
    const t = buildTranscript(events);
    expect(t.contextCompacted).toBe(false);
    expect(t.usage?.cacheCreationTokens).toBe(8_000);
  });

  it('adopts compact-boundary postTokens immediately while preserving the older fallback', () => {
    const measured = buildTranscript([
      ev({ type: 'usage', seq: 1, inputTokens: 400_000 }),
      ev({ type: 'slash_result', seq: 2, subtype: 'compact', text: '✓ compacted' }),
      ev({ type: 'usage', seq: 3, inputTokens: 9_514 }),
    ]);
    expect(measured.contextCompacted).toBe(false);
    expect(measured.usage?.inputTokens).toBe(9_514);

    const fallback = buildTranscript([
      ev({ type: 'usage', seq: 1, inputTokens: 400_000 }),
      ev({ type: 'slash_result', seq: 2, subtype: 'compact', text: '✓ compacted' }),
    ]);
    expect(fallback.contextCompacted).toBe(true);
  });

  it('a thinking-only usage (occ 0) after a compact does NOT clear the reset', () => {
    // system/thinking_tokens folds into a usage event with no window tokens — it must
    // not be mistaken for the next turn's re-measure.
    const events: NativeEvent[] = [
      ev({ type: 'usage', seq: 1, inputTokens: 50_000 }),
      ev({ type: 'slash_result', seq: 2, subtype: 'compact', text: '✓ compacted' }),
      ev({ type: 'usage', seq: 3, reasoningTokens: 120 }), // thinking estimate, occ 0
    ];
    const t = buildTranscript(events);
    expect(t.contextCompacted).toBe(true);
  });

  // /clear rotates the session in place (no respawn → same epoch + ring) but frees
  // the whole window with no fresh usage — the same fallback as an older
  // /compact boundary without postTokens.
  // A second session_init with a NEW id is the rotation marker.
  it('flags contextCompacted when a session_init rotates the id after the last usage', () => {
    const events: NativeEvent[] = [
      ev({ type: 'session_init', seq: 1, sessionId: 'aaa', model: 'sonnet' }),
      ev({ type: 'usage', seq: 2, inputTokens: 50_000, cachedInputTokens: 100_000 }),
      ev({ type: 'session_init', seq: 3, sessionId: 'bbb', model: 'sonnet' }), // /clear
    ];
    expect(buildTranscript(events).contextCompacted).toBe(true);
  });

  it('clears contextCompacted once a context-bearing usage lands after a /clear rotation', () => {
    const events: NativeEvent[] = [
      ev({ type: 'session_init', seq: 1, sessionId: 'aaa' }),
      ev({ type: 'usage', seq: 2, inputTokens: 50_000 }),
      ev({ type: 'session_init', seq: 3, sessionId: 'bbb' }), // /clear
      ev({ type: 'usage', seq: 4, inputTokens: 2, cacheCreationTokens: 8_000 }), // re-measure
    ];
    expect(buildTranscript(events).contextCompacted).toBe(false);
  });

  it('a re-emitted session_init with the SAME id (reconnect replay) is not a rotation', () => {
    const events: NativeEvent[] = [
      ev({ type: 'session_init', seq: 1, sessionId: 'aaa' }),
      ev({ type: 'usage', seq: 2, inputTokens: 50_000 }),
      ev({ type: 'session_init', seq: 3, sessionId: 'aaa' }), // dup replay, not /clear
    ];
    expect(buildTranscript(events).contextCompacted).toBe(false);
  });

  // A NON-compact slash_result (e.g. /context output) must not trip the reset.
  it('a plain slash_result does not flag contextCompacted', () => {
    const events: NativeEvent[] = [
      ev({ type: 'usage', seq: 1, inputTokens: 50_000 }),
      ev({ type: 'slash_result', seq: 2, text: 'context: 42%' }),
    ];
    expect(buildTranscript(events).contextCompacted).toBe(false);
  });

  it('inserts a turn separator before each user turn after the first', () => {
    const events: NativeEvent[] = [
      ev({ type: 'user_text', seq: 1, text: 'first' }),
      ev({ type: 'text', seq: 2, text: 'reply', messageId: 'm1' }),
      ev({ type: 'user_text', seq: 3, text: 'second', clientTs: 999 }),
      ev({ type: 'text', seq: 4, text: 'reply2', messageId: 'm2' }),
      ev({ type: 'user_text', seq: 5, text: 'third', clientTs: 1500 }),
    ];
    const t = buildTranscript(events);
    const kinds = t.rows.map((r) => r.kind);
    // first user (no sep) → text → SEP → second user → text → SEP → third user
    expect(kinds).toEqual([
      'event',
      'event',
      'turn-sep',
      'event',
      'event',
      'turn-sep',
      'event',
    ]);
    const seps = t.rows.filter((r) => r.kind === 'turn-sep');
    expect(seps).toHaveLength(2);
    if (seps[0].kind === 'turn-sep') expect(seps[0].clientTs).toBe(999);
    if (seps[1].kind === 'turn-sep') expect(seps[1].clientTs).toBe(1500);
  });

  it('does not insert a separator when the user turn is the very first row', () => {
    const events: NativeEvent[] = [ev({ type: 'user_text', seq: 1, text: 'only' })];
    const t = buildTranscript(events);
    expect(t.rows.every((r) => r.kind === 'event')).toBe(true);
  });

  it('suppresses a TodoWrite tool_call + its result while the todo event renders', () => {
    const events: NativeEvent[] = [
      ev({ type: 'user_text', seq: 1, text: 'plan it' }),
      ev({ type: 'tool_call', seq: 2, callId: 'todo1', name: 'TodoWrite' }),
      ev({ type: 'tool_result', seq: 3, callId: 'todo1', content: 'Todos updated' }),
      ev({
        type: 'todo',
        seq: 4,
        items: [
          { text: 'step one', status: 'completed' },
          { text: 'step two', status: 'in_progress' },
        ],
      }),
      // A real (non-todo) tool call must still pair normally.
      ev({ type: 'tool_call', seq: 5, callId: 'b1', name: 'Bash' }),
      ev({ type: 'tool_result', seq: 6, callId: 'b1', content: 'ran' }),
    ];
    const t = buildTranscript(events);
    const eventTypes = t.rows
      .filter((r) => r.kind === 'event')
      .map((r) => (r.kind === 'event' ? r.event.type : ''));
    // The TodoWrite tool_call card is gone; the `todo` event survives in its place.
    expect(eventTypes).toEqual(['user_text', 'todo', 'tool_call']);
    // The surviving tool_call is the Bash one (not TodoWrite).
    const toolRow = t.rows.find((r) => r.kind === 'event' && r.event.type === 'tool_call');
    expect(toolRow && toolRow.kind === 'event' ? toolRow.event.name : '').toBe('Bash');
    // The TodoWrite result is NOT registered (would otherwise orphan); Bash is.
    expect(t.resultByCallId.has('todo1')).toBe(false);
    expect(t.resultByCallId.get('b1')?.content).toBe('ran');
  });

  it('matches TodoWrite on the toolName key too (permission-style payloads)', () => {
    const events: NativeEvent[] = [
      ev({ type: 'tool_call', seq: 1, callId: 't', toolName: 'TodoWrite' }),
      ev({ type: 'tool_result', seq: 2, callId: 't', content: 'ok' }),
    ];
    const t = buildTranscript(events);
    expect(t.rows.filter((r) => r.kind === 'event')).toHaveLength(0);
    expect(t.resultByCallId.has('t')).toBe(false);
  });
});

describe('buildTranscript ghost-spinner reconciliation', () => {
  // A turn that spawned tools then was cut off mid-flight (e.g. an auto-update
  // respawned the headless agent): some calls got results, one never did.
  const danglingTurn = (): NativeEvent[] => [
    ev({ type: 'user_text', seq: 1, text: 'spawn agents' }),
    ev({ type: 'tool_call', seq: 2, callId: 'a', name: 'Agent' }),
    ev({ type: 'tool_call', seq: 3, callId: 'b', name: 'Agent' }), // never gets a result
    ev({ type: 'tool_call', seq: 4, callId: 'c', name: 'Agent' }),
    ev({ type: 'tool_result', seq: 5, callId: 'a', content: 'done a' }),
    ev({ type: 'tool_result', seq: 6, callId: 'c', content: 'done c' }),
  ];

  it('marks a result-less call interrupted when the daemon says the turn settled', () => {
    const t = buildTranscript(danglingTurn(), false);
    const synthetic = t.resultByCallId.get('b');
    expect(synthetic?.interrupted).toBe(true);
    expect(synthetic?.isError).toBeUndefined(); // interrupted is NOT a failure
    expect(isErroredResult(synthetic)).toBe(false);
    // The real results are untouched (not overwritten by the reconciliation).
    expect(t.resultByCallId.get('a')?.content).toBe('done a');
    expect(t.resultByCallId.get('a')?.interrupted).toBeUndefined();
  });

  it('leaves a result-less call running while the turn is still live (true)', () => {
    const t = buildTranscript(danglingTurn(), true);
    expect(t.resultByCallId.has('b')).toBe(false); // still "running…"
  });

  it('does not reconcile for an old daemon that omits agentMidTurn (undefined)', () => {
    const t = buildTranscript(danglingTurn());
    expect(t.resultByCallId.has('b')).toBe(false);
  });

  it('never fabricates a result for a suppressed TodoWrite call', () => {
    const events: NativeEvent[] = [
      ev({ type: 'tool_call', seq: 1, callId: 'todo1', name: 'TodoWrite' }),
      // no result, turn settled
    ];
    const t = buildTranscript(events, false);
    expect(t.resultByCallId.has('todo1')).toBe(false);
  });
});

describe('groupRows (focus terse pass)', () => {
  // Build realistic rows via buildTranscript, then fold them.
  const rowsFor = (events: NativeEvent[]) => buildTranscript(events).rows;
  const exec = (seq: number, callId: string) =>
    ev({ type: 'tool_call', seq, callId, name: 'Bash', toolKind: 'exec' });
  const result = (seq: number, callId: string, extra: Partial<NativeEvent> = {}) =>
    ev({ type: 'tool_result', seq, callId, ...extra });

  it('folds a run of ≥2 adjacent tool calls into one tool-group (focus)', () => {
    const out = groupRows(
      rowsFor([
        ev({ type: 'user_text', seq: 1, text: 'go' }),
        exec(2, 'a'),
        result(3, 'a'),
        exec(4, 'b'),
        result(5, 'b'),
        exec(6, 'c'),
        result(7, 'c'),
      ])
    );
    expect(out.map((r) => r.kind)).toEqual(['event', 'tool-group']);
    const grp = out[1];
    expect(grp.kind === 'tool-group' && grp.events.length).toBe(3);
  });

  it('folds even a single tool call into a tool-group (focus) so its command collapses', () => {
    const out = groupRows(
      rowsFor([ev({ type: 'user_text', seq: 1, text: 'go' }), exec(2, 'a'), result(3, 'a')])
    );
    expect(out.map((r) => r.kind)).toEqual(['event', 'tool-group']);
    const grp = out[1];
    expect(grp.kind === 'tool-group' && grp.events.length).toBe(1);
  });

  it('prose between tool calls breaks the run', () => {
    const out = groupRows(
      rowsFor([
        ev({ type: 'user_text', seq: 1, text: 'go' }),
        exec(2, 'a'),
        result(3, 'a'),
        exec(4, 'b'),
        result(5, 'b'),
        ev({ type: 'text', seq: 6, text: 'a note', messageId: 'm1' }),
        exec(7, 'c'),
        result(8, 'c'),
        exec(9, 'd'),
        result(10, 'd'),
      ])
    );
    // user · group(a,b) · text · group(c,d)
    expect(out.map((r) => r.kind)).toEqual(['event', 'tool-group', 'event', 'tool-group']);
  });

  it('an AskUserQuestion tool_call is never grouped and breaks the run', () => {
    const out = groupRows(
      rowsFor([
        exec(1, 'a'),
        result(2, 'a'),
        exec(3, 'b'),
        result(4, 'b'),
        ev({ type: 'tool_call', seq: 5, callId: 'q', name: 'AskUserQuestion' }),
        exec(6, 'c'),
        result(7, 'c'),
        exec(8, 'd'),
        result(9, 'd'),
      ])
    );
    expect(out.map((r) => r.kind)).toEqual(['tool-group', 'event', 'tool-group']);
    const ask = out[1];
    expect(ask.kind === 'event' && (ask.event.name || ask.event.toolName)).toBe('AskUserQuestion');
  });

  const edit = (seq: number, callId: string) =>
    ev({ type: 'tool_call', seq, callId, name: 'Edit', toolKind: 'file' });

  it('a file-edit tool never folds — it stays standalone (diff visible) and breaks the run', () => {
    const out = groupRows(
      rowsFor([
        ev({ type: 'user_text', seq: 1, text: 'go' }),
        exec(2, 'a'),
        result(3, 'a'),
        edit(4, 'e1'),
        result(5, 'e1'),
        exec(6, 'b'),
        result(7, 'b'),
      ])
    );
    // bash(a) folds, the Edit stays a standalone event, bash(b) folds.
    expect(out.map((r) => r.kind)).toEqual(['event', 'tool-group', 'event', 'tool-group']);
    const editRow = out[2];
    expect(editRow.kind === 'event' && (editRow.event.name || editRow.event.toolName)).toBe('Edit');
  });

  it('a lone file-edit renders standalone, not a tool-group', () => {
    const out = groupRows(
      rowsFor([ev({ type: 'user_text', seq: 1, text: 'go' }), edit(2, 'e1'), result(3, 'e1')])
    );
    expect(out.map((r) => r.kind)).toEqual(['event', 'event']);
  });

  it('consolidates a run of ≥2 consecutive edits into one edit-group (focus)', () => {
    const out = groupRows(
      rowsFor([
        ev({ type: 'user_text', seq: 1, text: 'go' }),
        edit(2, 'e1'),
        result(3, 'e1'),
        edit(4, 'e2'),
        result(5, 'e2'),
        edit(6, 'e3'),
        result(7, 'e3'),
      ])
    );
    expect(out.map((r) => r.kind)).toEqual(['event', 'edit-group']);
    const grp = out[1];
    expect(grp.kind === 'edit-group' && grp.events.length).toBe(3);
  });

  it('a thinking block between edits breaks the edit run (each lone edit stays inline)', () => {
    const out = groupRows(
      rowsFor([
        edit(1, 'e1'),
        result(2, 'e1'),
        ev({ type: 'thinking', seq: 3, text: 'enough words here to be a block' }),
        edit(4, 'e2'),
        result(5, 'e2'),
      ])
    );
    // lone edit · lone thinking · lone edit — nothing consolidates.
    expect(out.map((r) => r.kind)).toEqual(['event', 'event', 'event']);
  });

  it('edits do not mix into an adjacent bash tool-group', () => {
    const out = groupRows(
      rowsFor([
        exec(1, 'a'),
        result(2, 'a'),
        edit(3, 'e1'),
        result(4, 'e1'),
        edit(5, 'e2'),
        result(6, 'e2'),
      ])
    );
    // bash folds on its own; the two edits consolidate into a separate edit-group.
    expect(out.map((r) => r.kind)).toEqual(['tool-group', 'edit-group']);
  });

  it('auto-approve permission_requests between edits do NOT break the run (real-world bug)', () => {
    // An auto-approving pane emits one permission_request per edit; resolved ones
    // render nothing and must not fragment "3 edits" into 3 ungrouped singletons.
    const out = groupRows(
      rowsFor([
        ev({ type: 'user_text', seq: 1, text: 'bump version' }),
        edit(2, 'e1'),
        ev({ type: 'permission_request', seq: 3, callId: 'e1' }),
        result(4, 'e1'),
        edit(5, 'e2'),
        ev({ type: 'permission_request', seq: 6, callId: 'e2' }),
        result(7, 'e2'),
        edit(8, 'e3'),
        ev({ type: 'permission_request', seq: 9, callId: 'e3' }),
        result(10, 'e3'),
      ])
    );
    expect(out.map((r) => r.kind)).toEqual(['event', 'edit-group']);
    const grp = out[1];
    expect(grp.kind === 'edit-group' && grp.events.length).toBe(3);
  });

  it('a status tick between edits does NOT break the run', () => {
    const out = groupRows(
      rowsFor([
        edit(1, 'e1'),
        result(2, 'e1'),
        ev({ type: 'status', seq: 3, subtype: 'requesting' }),
        edit(4, 'e2'),
        result(5, 'e2'),
      ])
    );
    expect(out.map((r) => r.kind)).toEqual(['edit-group']);
    const grp = out[0];
    expect(grp.kind === 'edit-group' && grp.events.length).toBe(2);
  });

  it('drops a RESOLVED permission_request from rows but KEEPS a pending one', () => {
    const resolved = buildTranscript([
      edit(1, 'e1'),
      ev({ type: 'permission_request', seq: 2, callId: 'e1' }),
      result(3, 'e1'),
    ]).rows;
    expect(resolved.some((r) => r.kind === 'event' && r.event.type === 'permission_request')).toBe(false);

    const pending = buildTranscript([
      edit(1, 'e1'),
      ev({ type: 'permission_request', seq: 2, callId: 'e1' }), // no result → still pending
    ]).rows;
    expect(pending.some((r) => r.kind === 'event' && r.event.type === 'permission_request')).toBe(true);
  });

  it('a turn separator breaks a tool run', () => {
    const out = groupRows(
      rowsFor([
        ev({ type: 'user_text', seq: 1, text: 'first' }),
        exec(2, 'a'),
        result(3, 'a'),
        exec(4, 'b'),
        result(5, 'b'),
        ev({ type: 'user_text', seq: 6, text: 'second' }),
        exec(7, 'c'),
        result(8, 'c'),
        exec(9, 'd'),
        result(10, 'd'),
      ])
    );
    // u1 · group(a,b) · turn-sep · u2 · group(c,d)
    expect(out.map((r) => r.kind)).toEqual([
      'event',
      'tool-group',
      'turn-sep',
      'event',
      'tool-group',
    ]);
  });

  it('folds adjacent thinking blocks into one thinking-group', () => {
    const out = groupRows(
      rowsFor([
        ev({ type: 'user_text', seq: 1, text: 'go' }),
        ev({ type: 'thinking', seq: 2, text: 'one two three' }),
        ev({ type: 'thinking', seq: 3, text: 'four five' }),
        ev({ type: 'thinking', seq: 4, text: 'six' }),
      ])
    );
    expect(out.map((r) => r.kind)).toEqual(['event', 'thinking-group']);
    const grp = out[1];
    expect(grp.kind === 'thinking-group' && grp.events.length).toBe(3);
  });

  it('thinking that precedes tools folds into a step-group (no more think/tool ladder)', () => {
    const out = groupRows(
      rowsFor([
        ev({ type: 'thinking', seq: 1, text: 'plan a' }),
        ev({ type: 'thinking', seq: 2, text: 'plan b' }),
        exec(3, 'a'),
        result(4, 'a'),
        exec(5, 'b'),
        result(6, 'b'),
      ])
    );
    // The thinkings are followed by tools (the MEANS) → fold into ONE step-group.
    expect(out.map((r) => r.kind)).toEqual(['step-group']);
    const grp = out[0];
    expect(grp.kind === 'step-group' && grp.events.length).toBe(4);
  });

  it('folds the think→tool→think→tool ladder into one step-group, in order', () => {
    const out = groupRows(
      rowsFor([
        ev({ type: 'user_text', seq: 1, text: 'go' }),
        ev({ type: 'thinking', seq: 2, text: 't1' }),
        exec(3, 'a'),
        result(4, 'a'),
        ev({ type: 'thinking', seq: 5, text: 't2' }),
        exec(6, 'b'),
        result(7, 'b'),
        ev({ type: 'thinking', seq: 8, text: 't3' }),
        exec(9, 'c'),
        result(10, 'c'),
      ])
    );
    expect(out.map((r) => r.kind)).toEqual(['event', 'step-group']);
    const grp = out[1];
    expect(grp.kind === 'step-group' && grp.events.length).toBe(6);
    if (grp.kind === 'step-group') {
      expect(grp.events.map((e) => e.type)).toEqual([
        'thinking', 'tool_call', 'thinking', 'tool_call', 'thinking', 'tool_call',
      ]);
    }
  });

  it('a thinking that precedes the prose reply stays visible (conclusion not folded)', () => {
    const out = groupRows(
      rowsFor([
        ev({ type: 'thinking', seq: 1, text: 'means' }),
        exec(2, 'a'),
        result(3, 'a'),
        ev({ type: 'thinking', seq: 4, text: 'conclusion reasoning' }),
        ev({ type: 'text', seq: 5, text: 'here is the answer', messageId: 'm1' }),
      ])
    );
    // The means-thinking folds with its tool; the trailing thinking (before prose) stays.
    expect(out.map((r) => r.kind)).toEqual(['step-group', 'event', 'event']);
    const grp = out[0];
    expect(grp.kind === 'step-group' && grp.events.length).toBe(2);
    const trailing = out[1];
    expect(trailing.kind === 'event' && trailing.event.type).toBe('thinking');
  });

  it('a lone thinking→tool folds into a step-group', () => {
    const out = groupRows(
      rowsFor([
        ev({ type: 'thinking', seq: 1, text: 'quick thought' }),
        exec(2, 'a'),
        result(3, 'a'),
      ])
    );
    expect(out.map((r) => r.kind)).toEqual(['step-group']);
    const grp = out[0];
    expect(grp.kind === 'step-group' && grp.events.length).toBe(2);
  });
});

describe('isUserDenial / isErroredResult', () => {
  it('treats the daemon deny strings as denials, not errors', () => {
    expect(isUserDenial('The user denied this request.')).toBe(true);
    expect(isUserDenial('the user denied this request')).toBe(true);
    expect(isUserDenial('interrupted')).toBe(true);
    expect(isUserDenial('command not found')).toBe(false);
    expect(isUserDenial(undefined)).toBe(false);
  });
  it('isErroredResult is true only for a genuine error result', () => {
    expect(isErroredResult(undefined)).toBe(false);
    expect(isErroredResult(ev({ type: 'tool_result', seq: 1, content: 'ok' }))).toBe(false);
    expect(isErroredResult(ev({ type: 'tool_result', seq: 1, isError: true, content: 'boom' }))).toBe(true);
    // A user denial is isError:true but is NOT a failure.
    expect(
      isErroredResult(ev({ type: 'tool_result', seq: 1, isError: true, content: 'interrupted' })),
    ).toBe(false);
  });
});

describe('relativeTime', () => {
  const now = 1_000_000;
  it('reads "just now" within 5s', () => {
    expect(relativeTime(now - 1000, now)).toBe('just now');
    expect(relativeTime(now, now)).toBe('just now');
  });
  it('formats seconds / minutes / hours / days', () => {
    expect(relativeTime(now - 42_000, now)).toBe('42s ago');
    expect(relativeTime(now - 5 * 60_000, now)).toBe('5m ago');
    expect(relativeTime(now - 3 * 3_600_000, now)).toBe('3h ago');
    expect(relativeTime(now - 2 * 86_400_000, now)).toBe('2d ago');
  });
  it('falls back to "earlier" for undefined (replayed history)', () => {
    expect(relativeTime(undefined, now)).toBe('earlier');
  });
});

describe('fmtDuration', () => {
  it('formats sub-second, seconds, and minutes', () => {
    expect(fmtDuration(420)).toBe('420ms');
    expect(fmtDuration(1400)).toBe('1.4s');
    expect(fmtDuration(42_000)).toBe('42s');
    expect(fmtDuration(125_000)).toBe('2m 5s');
    expect(fmtDuration(120_000)).toBe('2m');
  });
  it('returns empty for missing/invalid input', () => {
    expect(fmtDuration(undefined)).toBe('');
    expect(fmtDuration(-5)).toBe('');
  });
});

describe('computeNativeWorking', () => {
  const work = (events: NativeEvent[], agentMidTurn: boolean | undefined, pendingCount = 0) =>
    computeNativeWorking(events, pendingCount, agentMidTurn, 'p');

  it('a pending optimistic row is always working (fabricated status)', () => {
    expect(work([], false, 1)).toMatchObject({ type: 'status', subtype: 'requesting' });
  });

  it('a TRAILING turn_end is settled — even if agentMidTurn lags true', () => {
    const events = [
      ev({ type: 'user_text', seq: 1 }),
      ev({ type: 'text', seq: 2 }),
      ev({ type: 'turn_end', seq: 3 }),
    ];
    expect(work(events, true)).toBeNull();
    expect(work(events, false)).toBeNull();
    // Usage bursts after the turn_end don't un-trail it.
    const withUsage = [...events, ev({ type: 'usage', seq: 4 }), ev({ type: 'usage', seq: 5 })];
    expect(work(withUsage, true)).toBeNull();
  });

  it('THE BUG (2026-07-09): a self-started turn after turn_end must show working', () => {
    // Real shape from a live pane: Claude backgrounds an Agent, the launch
    // segment ends (turn_end, "worked for 18s"), then it immediately keeps
    // working — tool_call/thinking directly after turn_end, NO user_text until
    // the <task-notification> minutes later. The old "any turn_end since the
    // last user_text settles" rule read that whole continuation as done: a dead
    // "✓ worked for Xs" under live streaming.
    const events = [
      ev({ type: 'user_text', seq: 1 }),
      ev({ type: 'tool_call', seq: 2, name: 'Agent', callId: 'a1' }),
      ev({ type: 'tool_result', seq: 3, callId: 'a1', content: 'Async agent launched successfully.' }),
      ev({ type: 'text', seq: 4 }),
      ev({ type: 'turn_end', seq: 5, durationMs: 18_195 }),
      // — continuation turn, no user_text —
      ev({ type: 'tool_call', seq: 6, name: 'Bash', callId: 'b1' }),
      ev({ type: 'tool_result', seq: 7, callId: 'b1' }),
      ev({ type: 'thinking', seq: 8 }),
    ];
    expect(work(events, true)).toMatchObject({ type: 'status', subtype: 'requesting' });
    // Daemon still the settle authority for the continuation turn.
    expect(work(events, false)).toBeNull();
    // A status event in the continuation segment drives the spinner detail.
    const withStatus = [...events, ev({ type: 'status', seq: 9, subtype: 'forging' })];
    expect(work(withStatus, true)).toMatchObject({ type: 'status', subtype: 'forging' });
  });

  it('THE BUG: a trailing usage BURST mid-turn must NOT blank the spinner (agentMidTurn wins)', () => {
    // Claude emits usage events after every tool round, often in bursts. The old
    // "trailing usage settles" rule returned null whenever the latest event was
    // one of those — the flicker the user reported. With the daemon flag true we
    // trust it: still working.
    const events = [
      ev({ type: 'user_text', seq: 1 }),
      ev({ type: 'tool_call', seq: 2, name: 'Bash' }),
      ev({ type: 'tool_result', seq: 3 }),
      ev({ type: 'status', seq: 4, subtype: 'requesting' }),
      ev({ type: 'usage', seq: 5 }),
      ev({ type: 'usage', seq: 6 }),
      ev({ type: 'usage', seq: 7 }), // <-- latest event is an interim usage
    ];
    // The status detail is still surfaced (it's the real status this turn).
    expect(work(events, true)).toMatchObject({ type: 'status', subtype: 'requesting' });
    // And even with NO status, the flag alone keeps the spinner up.
    const noStatus = [
      ev({ type: 'user_text', seq: 1 }),
      ev({ type: 'tool_result', seq: 2 }),
      ev({ type: 'usage', seq: 3 }),
      ev({ type: 'usage', seq: 4 }),
    ];
    expect(work(noStatus, true)).toMatchObject({ type: 'status', subtype: 'requesting' });
  });

  it('agentMidTurn=false settles even with a trailing usage (daemon is the authority)', () => {
    const events = [
      ev({ type: 'user_text', seq: 1 }),
      ev({ type: 'text', seq: 2 }),
      ev({ type: 'usage', seq: 3 }),
    ];
    expect(work(events, false)).toBeNull();
  });

  it('a real status event drives the spinner detail when working', () => {
    const events = [ev({ type: 'user_text', seq: 1 }), ev({ type: 'status', seq: 2, subtype: 'forging' })];
    expect(work(events, true)).toMatchObject({ type: 'status', subtype: 'forging' });
  });

  it('tool_result with no later event + agentMidTurn → working (event-less gap filled)', () => {
    const events = [
      ev({ type: 'user_text', seq: 1 }),
      ev({ type: 'tool_call', seq: 2, name: 'Bash' }),
      ev({ type: 'tool_result', seq: 3 }),
    ];
    expect(work(events, true)).toMatchObject({ type: 'status', subtype: 'requesting' });
    expect(work(events, false)).toBeNull();
  });

  it('idle: no events in the current turn and agentMidTurn false → null', () => {
    expect(work([ev({ type: 'user_text', seq: 1 })], false)).toBeNull();
  });

  it('old daemon (agentMidTurn undefined): trailing usage settles; status ⇒ working', () => {
    // Back-compat: with no daemon flag, preserve the pre-agentMidTurn heuristic.
    const settled = [
      ev({ type: 'user_text', seq: 1 }),
      ev({ type: 'text', seq: 2 }),
      ev({ type: 'usage', seq: 3 }), // trailing usage settles on an old daemon
    ];
    expect(work(settled, undefined)).toBeNull();
    const working = [
      ev({ type: 'user_text', seq: 1 }),
      ev({ type: 'status', seq: 2, subtype: 'requesting' }),
    ];
    expect(work(working, undefined)).toMatchObject({ type: 'status', subtype: 'requesting' });
  });
});

describe('lastTurnDuration', () => {
  it('returns the most recent turn_end duration', () => {
    const events = [
      ev({ type: 'user_text', seq: 1 }),
      ev({ type: 'turn_end', seq: 2, durationMs: 4200 }),
      ev({ type: 'user_text', seq: 3 }),
      ev({ type: 'text', seq: 4 }),
      ev({ type: 'turn_end', seq: 5, durationMs: 8800 }),
    ];
    expect(lastTurnDuration(events)).toBe(8800);
  });
  it('is undefined before any turn completes', () => {
    expect(lastTurnDuration([ev({ type: 'user_text', seq: 1 }), ev({ type: 'text', seq: 2 })])).toBeUndefined();
  });
  it('is undefined when the latest turn_end carried no duration (replayed history)', () => {
    expect(lastTurnDuration([ev({ type: 'turn_end', seq: 1 })])).toBeUndefined();
  });
});

describe('isApiErrorText', () => {
  it('matches the claude CLI API-error envelope', () => {
    expect(isApiErrorText('API Error: 529 Overloaded. This is a server-side issue.')).toBe(true);
    expect(isApiErrorText('  API Error: 400 Bad Request')).toBe(true);
  });
  it('does not match the model mentioning an error in prose', () => {
    expect(isApiErrorText('The API error handling here looks fine.')).toBe(false);
    expect(isApiErrorText('Here is how to handle an API Error in your code.')).toBe(false);
    expect(isApiErrorText('')).toBe(false);
    expect(isApiErrorText(undefined)).toBe(false);
  });
});

describe('isTransientOverload', () => {
  it('flags overload / rate-limit / 5xx from message OR code', () => {
    expect(isTransientOverload('API Error: 529 Overloaded')).toBe(true);
    expect(isTransientOverload(undefined, 'overloaded_error')).toBe(true);
    expect(isTransientOverload('rate limit exceeded')).toBe(true);
    expect(isTransientOverload('429 Too Many Requests')).toBe(true);
    expect(isTransientOverload('503 Service Unavailable')).toBe(true);
    expect(isTransientOverload('usually temporary, try again')).toBe(true);
  });
  it('does not flag a genuine failure', () => {
    expect(isTransientOverload('API Error: 400 invalid_request_error')).toBe(false);
    expect(isTransientOverload('permission denied', 'error_during_execution')).toBe(false);
    expect(isTransientOverload('', '')).toBe(false);
    expect(isTransientOverload(undefined, undefined)).toBe(false);
  });
});

describe('buildTranscript drops the duplicate API-error text', () => {
  it('suppresses an assistant API-error text block (the error event renders it)', () => {
    const events: NativeEvent[] = [
      ev({ type: 'user_text', seq: 1, text: 'go' }),
      ev({ type: 'text', seq: 2, text: 'API Error: 529 Overloaded. Try again.', messageId: 'm1' }),
      ev({ type: 'turn_end', seq: 3, subtype: 'error' }),
      ev({ type: 'error', seq: 4, message: 'API Error: 529 Overloaded.', code: 'error' }),
    ];
    const t = buildTranscript(events);
    const types = t.rows.filter((r) => r.kind === 'event').map((r) => (r as { event: NativeEvent }).event.type);
    expect(types).not.toContain('text'); // raw duplicate dropped
    expect(types).toContain('error'); // single banner survives
  });
  it('keeps normal assistant text', () => {
    const events: NativeEvent[] = [
      ev({ type: 'user_text', seq: 1, text: 'go' }),
      ev({ type: 'text', seq: 2, text: 'Done — shipped it.', messageId: 'm1' }),
    ];
    const t = buildTranscript(events);
    const types = t.rows.filter((r) => r.kind === 'event').map((r) => (r as { event: NativeEvent }).event.type);
    expect(types).toContain('text');
  });
});

describe('buildTranscript sub-agent nesting', () => {
  const topToolCallIds = (t: ReturnType<typeof buildTranscript>) =>
    t.rows
      .filter((r) => r.kind === 'event' && (r as { event: NativeEvent }).event.type === 'tool_call')
      .map((r) => (r as { event: NativeEvent }).event.callId);

  it('collects nested tool_calls under their parent Task and hides them from top-level rows', () => {
    const events: NativeEvent[] = [
      ev({ type: 'tool_call', seq: 1, callId: 'T', name: 'Task' }),
      ev({ type: 'tool_call', seq: 2, callId: 'b', name: 'Bash', parentCallId: 'T' }),
      ev({ type: 'tool_result', seq: 3, callId: 'b' }),
      ev({ type: 'tool_call', seq: 4, callId: 'r', name: 'Read', parentCallId: 'T' }),
      ev({ type: 'tool_result', seq: 5, callId: 'r' }),
      ev({ type: 'tool_result', seq: 6, callId: 'T' }),
    ];
    const t = buildTranscript(events);
    expect(topToolCallIds(t)).toEqual(['T']); // only the Task is top-level
    expect(t.childrenByParent.get('T')?.map((e) => e.callId)).toEqual(['b', 'r']);
    // Children's results stay paired (so TaskView can render them).
    expect(t.resultByCallId.has('b')).toBe(true);
    expect(t.resultByCallId.has('r')).toBe(true);
  });

  it('keeps an ORPHANED nested call top-level when its parent is absent (no vanish)', () => {
    const events: NativeEvent[] = [
      ev({ type: 'tool_call', seq: 1, callId: 'b', name: 'Bash', parentCallId: 'GONE' }),
      ev({ type: 'tool_result', seq: 2, callId: 'b' }),
    ];
    const t = buildTranscript(events);
    expect(topToolCallIds(t)).toEqual(['b']); // the orphan still renders
    expect(t.childrenByParent.size).toBe(0);
  });

  it('a Task is ungroupable — stays standalone, not folded with a sibling bash', () => {
    const out = groupRows(
      buildTranscript([
        ev({ type: 'tool_call', seq: 1, callId: 'a', name: 'Bash', toolKind: 'exec' }),
        ev({ type: 'tool_result', seq: 2, callId: 'a' }),
        ev({ type: 'tool_call', seq: 3, callId: 'T', name: 'Task' }),
        ev({ type: 'tool_result', seq: 4, callId: 'T' }),
      ]).rows
    );
    expect(out.map((r) => r.kind)).toEqual(['tool-group', 'event']);
    const taskRow = out[1];
    expect(taskRow.kind === 'event' && (taskRow.event.name || taskRow.event.toolName)).toBe('Task');
  });
});

describe('idleBackgroundShells', () => {
  const bg = (seq: number, callId: string): NativeEvent[] => [
    ev({ type: 'tool_call', seq, callId, name: 'Bash', args: { command: 'x', run_in_background: true } }),
    ev({
      type: 'tool_result',
      seq: seq + 1,
      callId,
      content: `Command running in background with ID: id${callId}. Output: /t/id${callId}.output`,
    }),
  ];
  // resultByCallId built the way buildTranscript does, so bgShell annotations land.
  const rbc = (events: NativeEvent[]) => buildTranscript(events).resultByCallId;

  it('counts a launched-but-unexited background shell once the turn is settled', () => {
    const events = bg(1, 'c1');
    expect(idleBackgroundShells(events, rbc(events), false)).toBe(1);
  });

  it('is 0 while the turn is still working (spinner conveys activity)', () => {
    const events = bg(1, 'c1');
    expect(idleBackgroundShells(events, rbc(events), true)).toBe(0);
  });

  it('is 0 once the shell has exited (bgShell set)', () => {
    const events: NativeEvent[] = [
      ev({ type: 'tool_call', seq: 1, callId: 'c1', name: 'Bash', args: { command: 'x', run_in_background: true } }),
      ev({ type: 'tool_result', seq: 2, callId: 'c1', content: 'Command running in background with ID: bg9. /t/bg9.output' }),
      ev({ type: 'tool_result', seq: 3, callId: 'poll', content: 'bg9 <status>completed</status> <exit_code>0</exit_code>' }),
    ];
    expect(idleBackgroundShells(events, rbc(events), false)).toBe(0);
  });

  it('ignores a plain foreground bash', () => {
    const events: NativeEvent[] = [
      ev({ type: 'tool_call', seq: 1, callId: 'c1', name: 'Bash', args: { command: 'ls' } }),
      ev({ type: 'tool_result', seq: 2, callId: 'c1', content: 'files' }),
    ];
    expect(idleBackgroundShells(events, rbc(events), false)).toBe(0);
  });

  it('counts multiple outstanding shells', () => {
    const events = [...bg(1, 'a'), ...bg(3, 'b')];
    expect(idleBackgroundShells(events, rbc(events), false)).toBe(2);
  });

  it('clears the count when a shell exits via <status>stopped</status> (phantom-waiting regression)', () => {
    // The real bug: two self-expiring background pollers were STOPPED, not "completed".
    // Their notifications carried <status>stopped</status>, which the exit detector
    // ignored — so the footer showed "waiting on 2 background shells" for hours after
    // both had died. Both must now read as exited → count 0. Notification task-id is the
    // launched bash id (`id${callId}`, per the bg() helper's launch content).
    const events: NativeEvent[] = [
      ...bg(1, 'a'),
      ...bg(3, 'b'),
      ev({ type: 'user_text', seq: 5, text: '<task-notification>\n<task-id>ida</task-id>\n<status>stopped</status>\n</task-notification>' }),
      ev({ type: 'user_text', seq: 6, text: '<task-notification>\n<task-id>idb</task-id>\n<status>stopped</status>\n</task-notification>' }),
    ];
    expect(idleBackgroundShells(events, rbc(events), false)).toBe(0);
  });
});

describe('idleBackgroundAgents', () => {
  // Real launch-result shape (captured live 2026-07-09): detection is on the
  // "Async agent launched successfully" marker + agentId, not the tool name.
  const launch = (seq: number, callId: string, agentId: string): NativeEvent[] => [
    ev({ type: 'tool_call', seq, callId, name: 'Agent', args: { description: 'research', prompt: 'x' } }),
    ev({
      type: 'tool_result',
      seq: seq + 1,
      callId,
      content: `[{"type":"text","text":"Async agent launched successfully. (internal metadata)\\nagentId: ${agentId} (internal ID - do not mention)"}]`,
    }),
  ];
  const rbc = (events: NativeEvent[]) => buildTranscript(events).resultByCallId;

  it('counts a launched-but-uncompleted background agent once the turn is settled', () => {
    const events = launch(1, 'c1', 'ag1');
    expect(idleBackgroundAgents(events, rbc(events), false)).toBe(1);
  });

  it('is 0 while the turn is still working (spinner conveys activity)', () => {
    const events = launch(1, 'c1', 'ag1');
    expect(idleBackgroundAgents(events, rbc(events), true)).toBe(0);
  });

  it('a <task-notification> user_text with the matching task-id clears it', () => {
    const events = [
      ...launch(1, 'c1', 'ag1'),
      ev({
        type: 'user_text',
        seq: 3,
        text: '<task-notification>\n<task-id>ag1</task-id>\n<tool-use-id>c1</tool-use-id>\n<status>completed</status>\n<summary>done</summary>',
      }),
    ];
    expect(idleBackgroundAgents(events, rbc(events), false)).toBe(0);
  });

  it('a reaped task ("No task found with ID") also clears it', () => {
    const events = [
      ...launch(1, 'c1', 'ag1'),
      ev({ type: 'tool_result', seq: 3, callId: 'poll', content: 'No task found with ID: ag1' }),
    ];
    expect(idleBackgroundAgents(events, rbc(events), false)).toBe(0);
  });

  it('a SendMessage result that merely QUOTES the agentId does NOT clear it', () => {
    const events = [
      ...launch(1, 'c1', 'ag1'),
      ev({ type: 'tool_result', seq: 3, callId: 'sm', content: 'Message sent to agent ag1.' }),
    ];
    expect(idleBackgroundAgents(events, rbc(events), false)).toBe(1);
  });

  it('a sync agent (final-report result, no launch marker) is not counted', () => {
    const events = [
      ev({ type: 'tool_call', seq: 1, callId: 'c1', name: 'Agent', args: { prompt: 'x' } }),
      ev({ type: 'tool_result', seq: 2, callId: 'c1', content: 'Here is the research report: …' }),
    ];
    expect(idleBackgroundAgents(events, rbc(events), false)).toBe(0);
  });

  it('replayed launches from a prior session are skipped (agents died with the old process)', () => {
    const events = launch(1, 'c1', 'ag1').map((e) => ({ ...e, fromReplay: true }));
    expect(idleBackgroundAgents(events, rbc(events), false)).toBe(0);
  });

  it('counts multiple outstanding agents', () => {
    const events = [...launch(1, 'a', 'ag1'), ...launch(3, 'b', 'ag2')];
    expect(idleBackgroundAgents(events, rbc(events), false)).toBe(2);
  });
});
