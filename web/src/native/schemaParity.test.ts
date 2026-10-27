// schemaParity.test.ts is the CLIENT-SIDE golden for the native-renderer event
// schema (the TS mirror in nativeEvents.ts). Its cross-language counterpart is
// daemon/agentevent_test.go, which locks the SAME EventType value set and the
// SAME wire-key sets on the Go NativeEvent struct. The two goldens sit next to
// each other on purpose: a rename/removal on EITHER side breaks its own golden,
// and a human keeps the two expected sets aligned.
//
// Why this exists: nativeEvents.ts and agentevent.go are hand-kept-in-sync, and
// two drifts have already shipped (the `todo` event and the `localMsgId`
// field). These tests make a future EventType rename/removal or a wire-key
// rename fail CI loudly instead of silently breaking one client.
//
// HOW IT CATCHES DRIFT:
//  - EVENT_TYPES is asserted against EVERY EventType literal via an exhaustive
//    typed map (`Record<EventType, true>`); renaming/removing a union member
//    makes that map fail to typecheck, and the runtime set assertion catches a
//    value drift vs the Go golden.
//  - NATIVE_EVENT_KEYS / TODO_ITEM_KEYS are asserted against a sample object
//    typed as NativeEvent / TodoItem (`satisfies`), so a key rename in
//    nativeEvents.ts makes the sample fail to typecheck, and the runtime key
//    assertion catches drift vs the Go golden.
//
// If you intentionally change the schema: update BOTH goldens (here and in
// daemon/agentevent_test.go) in the same change, on purpose.

import { describe, it, expect } from 'vitest';
import type { EventType, NativeEvent, TodoItem } from './nativeEvents';

// ---------------------------------------------------------------------------
// EventType value set. Keep in lockstep with expectedEventTypes in
// daemon/agentevent_test.go.
// ---------------------------------------------------------------------------

// Exhaustive map keyed by EventType: TS requires EVERY union member as a key and
// rejects any non-member, so a renamed/removed union literal breaks compilation
// HERE, in addition to the runtime set check below.
const EVENT_TYPE_PRESENCE: Record<EventType, true> = {
  session_init: true,
  text_delta: true,
  text: true,
  user_text: true,
  thinking_delta: true,
  thinking: true,
  tool_call: true,
  tool_result: true,
  todo: true,
  permission_request: true,
  permission_resolved: true,
  usage: true,
  rate_limit: true,
  status: true,
  turn_end: true,
  error: true,
  slash_result: true,
  prompt_suggestion: true,
};

const EVENT_TYPES: string[] = Object.keys(EVENT_TYPE_PRESENCE).sort();

// The same values as the Go golden, declared independently so a typo on one
// side is caught against the other.
const EXPECTED_EVENT_TYPES: string[] = [
  'error',
  'permission_request',
  'permission_resolved',
  'prompt_suggestion',
  'rate_limit',
  'session_init',
  'status',
  'text',
  'text_delta',
  'thinking',
  'thinking_delta',
  'todo',
  'tool_call',
  'tool_result',
  'turn_end',
  'usage',
  'user_text',
  'slash_result',
].sort();

// ---------------------------------------------------------------------------
// NativeEvent wire-key set. Keep in lockstep with expectedNativeEventKeys in
// daemon/agentevent_test.go. These are the WIRE keys (Go json: tags), including
// the renames (cachedInputTokens, reasoningTokens, totalCostUsd,
// permissionDenials).
//
// NOTE: nativeEvents.ts also carries CLIENT-ONLY annotation fields that are NOT
// wire keys (localMsgId, optimistic, streaming, clientTs, clientTsEnd). Those
// are deliberately EXCLUDED here — they have no Go counterpart, so listing them
// would create a false "Go is missing a key" signal. The sample below populates
// only the wire fields.
// ---------------------------------------------------------------------------

const NATIVE_EVENT_KEYS: string[] = [
  // envelope
  'seq',
  'paneId',
  'type',
  'fromReplay',
  // session_init
  'sessionId',
  'model',
  'cwd',
  'permissionMode',
  'tools',
  'mcpServers',
  'slashCommands',
  // text / text_delta / thinking / thinking_delta / user_text
  'text',
  'messageId',
  'signature',
  // tool_call
  'callId',
  'toolKind',
  'name',
  'args',
  'parentCallId',
  // tool_result
  'isError',
  'content',
  'output',
  // todo
  'items',
  // permission_request
  'requestId',
  'toolName',
  'rawEvent',
  'wouldAutoAllow',
  // usage / rate_limit
  'inputTokens',
  'cachedInputTokens',
  'cacheCreationTokens',
  'outputTokens',
  'reasoningTokens',
  'totalTokens',
  'contextWindowTokens',
  'totalCostUsd',
  'resetsAt',
  'rateLimitKind',
  // status / turn_end / error
  'subtype',
  'detail',
  'numTurns',
  'durationMs',
  'permissionDenials',
  'message',
  'code',
].sort();

// A NativeEvent sample carrying EXACTLY the wire keys above (no client-only
// annotation fields). Typed via `satisfies NativeEvent` so any wire-key rename
// in nativeEvents.ts makes this object fail to typecheck.
const SAMPLE_NATIVE_EVENT = {
  seq: 1,
  paneId: 'p1',
  type: 'text',
  fromReplay: true,

  sessionId: 's1',
  model: 'claude',
  cwd: '/tmp',
  permissionMode: 'auto',
  tools: ['Bash'],
  mcpServers: ['x'],
  slashCommands: ['/clear'],

  text: 'hi',
  messageId: 'm1',
  signature: 'sig',

  callId: 'c1',
  toolKind: 'exec',
  name: 'Bash',
  args: { a: 1 },
  parentCallId: 'pc1',

  isError: true,
  content: 'out',
  output: { stdout: 'x' },

  items: [{ text: 'do', completed: true, status: 'completed' }],

  requestId: 'r1',
  toolName: 'Bash',
  rawEvent: {},
  wouldAutoAllow: true,

  inputTokens: 1,
  cachedInputTokens: 2,
  cacheCreationTokens: 3,
  outputTokens: 4,
  reasoningTokens: 5,
  totalTokens: 6,
  contextWindowTokens: 7,
  totalCostUsd: 0.5,
  resetsAt: 123,
  rateLimitKind: 'primary',

  subtype: 'success',
  detail: 'd',
  numTurns: 2,
  durationMs: 10,
  permissionDenials: ['Bash'],
  message: 'boom',
  code: 'E1',
} satisfies NativeEvent;

// ---------------------------------------------------------------------------
// TodoItem wire-key set. Keep in lockstep with expectedTodoItemKeys in
// daemon/agentevent_test.go.
// ---------------------------------------------------------------------------

const TODO_ITEM_KEYS: string[] = ['text', 'completed', 'status'].sort();

const SAMPLE_TODO_ITEM = {
  text: 'do',
  completed: true,
  status: 'in_progress',
} satisfies TodoItem;

describe('native schema parity (cross-language golden for daemon/agentevent_test.go)', () => {
  it('EventType union matches the expected value set', () => {
    // If this fails, an EventType literal was renamed/removed/added in
    // nativeEvents.ts. Update EVENT_TYPE_PRESENCE here AND expectedEventTypes in
    // daemon/agentevent_test.go.
    expect(EVENT_TYPES).toEqual(EXPECTED_EVENT_TYPES);
    expect(EVENT_TYPES.length).toBe(18);
  });

  it('NativeEvent sample exposes exactly the expected wire keys', () => {
    // If this fails, a wire key was renamed/removed/added in nativeEvents.ts (or
    // the sample drifted from the golden). Update NATIVE_EVENT_KEYS + the sample
    // here AND expectedNativeEventKeys in daemon/agentevent_test.go.
    expect(Object.keys(SAMPLE_NATIVE_EVENT).sort()).toEqual(NATIVE_EVENT_KEYS);
  });

  it('TodoItem sample exposes exactly the expected wire keys', () => {
    expect(Object.keys(SAMPLE_TODO_ITEM).sort()).toEqual(TODO_ITEM_KEYS);
  });

  it('the four envelope keys are always present on a NativeEvent', () => {
    for (const k of ['seq', 'paneId', 'type', 'fromReplay']) {
      expect(NATIVE_EVENT_KEYS).toContain(k);
    }
  });
});
