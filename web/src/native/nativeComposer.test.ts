// Unit tests for the pure composer state machines: prompt-history recall (↑/↓
// cursor), the client slash-menu trigger/filter/selection, and the keyboard
// answer mapping for AskUserQuestion (digits 1–9 / Enter / Esc). These stay
// framework-free so they're testable without the DOM.

import { describe, it, expect } from 'vitest';
import {
  derivePromptHistory,
  recallOlder,
  recallNewer,
  HISTORY_IDLE,
  type HistoryNav,
  isSlashTrigger,
  filterSlashCommands,
  stepSlashSelection,
  SLASH_COMMANDS,
  composeSlashCommands,
  normalizeSlashName,
  isCompactCommand,
  digitToOptionIndex,
  isAnswerComplete,
  applyQuestionKey,
  findAtMention,
  applyAtMention,
  type AnswerQuestion,
} from './nativeComposer';
import type { NativeEvent } from './nativeEvents';

function ev(partial: Partial<NativeEvent> & { type: NativeEvent['type']; seq: number }): NativeEvent {
  return { paneId: 'p', fromReplay: false, ...partial } as NativeEvent;
}

// ---------------------------------------------------------------------------
// derivePromptHistory
// ---------------------------------------------------------------------------

describe('derivePromptHistory', () => {
  it('extracts user prompts oldest→newest, skipping non-user + empty', () => {
    const events: NativeEvent[] = [
      ev({ type: 'user_text', seq: 1, text: 'first' }),
      ev({ type: 'text', seq: 2, text: 'assistant reply' }),
      ev({ type: 'user_text', seq: 3, text: 'second' }),
      ev({ type: 'user_text', seq: 4, text: '' }), // empty — dropped
      ev({ type: 'tool_call', seq: 5, name: 'Bash' }),
      ev({ type: 'user_text', seq: 6, text: 'third' }),
    ];
    expect(derivePromptHistory(events)).toEqual(['first', 'second', 'third']);
  });

  it('keeps back-to-back repeated prompts as separate entries', () => {
    const events: NativeEvent[] = [
      ev({ type: 'user_text', seq: 1, text: 'yes' }),
      ev({ type: 'user_text', seq: 2, text: 'yes' }),
    ];
    expect(derivePromptHistory(events)).toEqual(['yes', 'yes']);
  });

  it('drops forwarded slash commands but keeps path-like prompts', () => {
    const events: NativeEvent[] = [
      ev({ type: 'user_text', seq: 1, text: '/context' }), // forwarded command — dropped
      ev({ type: 'user_text', seq: 2, text: '/cost' }), // forwarded command — dropped
      ev({ type: 'user_text', seq: 3, text: '/clear' }), // clear-real (forwarded) — dropped
      ev({ type: 'user_text', seq: 4, text: '/etc/hosts' }), // legit path prompt — KEPT
      ev({ type: 'user_text', seq: 5, text: 'fix the bug' }),
    ];
    expect(derivePromptHistory(events)).toEqual(['/etc/hosts', 'fix the bug']);
  });

  it('drops DYNAMIC forwarded commands (skills/custom) when an exclude set is passed', () => {
    // A session advertises /review (skill) + /context (built-in). Both forward
    // verbatim and should be kept out of prompt recall, while a path-like prompt
    // and a command WITH args (/compact …) are still recalled.
    const events: NativeEvent[] = [
      ev({ type: 'user_text', seq: 1, text: '/review' }), // skill — dropped
      ev({ type: 'user_text', seq: 2, text: '/context' }), // built-in — dropped
      ev({ type: 'user_text', seq: 3, text: '/compact focus on the API' }), // args — KEPT
      ev({ type: 'user_text', seq: 4, text: '/etc/hosts' }), // path — KEPT
    ];
    const exclude = new Set(['/review', '/context']);
    expect(derivePromptHistory(events, exclude)).toEqual([
      '/compact focus on the API',
      '/etc/hosts',
    ]);
  });

  it('drops <task-notification> blobs (background-task notices, not real prompts)', () => {
    const events: NativeEvent[] = [
      ev({ type: 'user_text', seq: 1, text: 'real prompt' }),
      ev({
        type: 'user_text',
        seq: 2,
        text: '<task-notification>\n<task-id>b41l601iz</task-id>\n<status>completed</status>\n</task-notification>',
      }),
      ev({ type: 'user_text', seq: 3, text: '  <task-notification>leading ws still dropped</task-notification>' }),
      ev({ type: 'user_text', seq: 4, text: 'another prompt' }),
    ];
    expect(derivePromptHistory(events)).toEqual(['real prompt', 'another prompt']);
  });
});

// ---------------------------------------------------------------------------
// Prompt-history cursor (recallOlder / recallNewer)
// ---------------------------------------------------------------------------

describe('prompt-history cursor', () => {
  const history = ['one', 'two', 'three']; // oldest→newest

  it('↑ from idle enters at the newest entry and saves the draft', () => {
    const res = recallOlder(HISTORY_IDLE, history, 'draft-in-progress');
    expect(res).not.toBeNull();
    expect(res!.text).toBe('three');
    expect(res!.nav).toEqual({ index: 2, saved: 'draft-in-progress' });
  });

  it('↑ steps to older entries and stops at the oldest', () => {
    let nav: HistoryNav = recallOlder(HISTORY_IDLE, history, 'd')!.nav; // index 2
    let res = recallOlder(nav, history, 'd');
    expect(res!.text).toBe('two');
    nav = res!.nav; // index 1
    res = recallOlder(nav, history, 'd');
    expect(res!.text).toBe('one');
    nav = res!.nav; // index 0 — oldest
    expect(recallOlder(nav, history, 'd')).toBeNull(); // can't go older
  });

  it('↓ steps toward newer and restores the saved draft past the newest', () => {
    // Walk up to the oldest, then back down.
    let res = recallOlder(HISTORY_IDLE, history, 'MYDRAFT')!; // index 2 (three)
    res = recallOlder(res.nav, history, 'MYDRAFT')!; // index 1 (two)
    res = recallOlder(res.nav, history, 'MYDRAFT')!; // index 0 (one)
    let down = recallNewer(res.nav, history)!;
    expect(down.text).toBe('two');
    down = recallNewer(down.nav, history)!;
    expect(down.text).toBe('three'); // newest
    // ↓ past the newest exits history and restores the original draft.
    const exit = recallNewer(down.nav, history)!;
    expect(exit.text).toBe('MYDRAFT');
    expect(exit.nav).toEqual(HISTORY_IDLE);
  });

  it('↓ while not browsing is a no-op', () => {
    expect(recallNewer(HISTORY_IDLE, history)).toBeNull();
  });

  it('↑ with empty history is a no-op', () => {
    expect(recallOlder(HISTORY_IDLE, [], 'draft')).toBeNull();
  });
});

// ---------------------------------------------------------------------------
// Slash menu: trigger / filter / selection cursor
// ---------------------------------------------------------------------------

describe('isSlashTrigger', () => {
  it('opens on a leading slash token with no space', () => {
    expect(isSlashTrigger('/')).toBe(true);
    expect(isSlashTrigger('/cl')).toBe(true);
    expect(isSlashTrigger('/clear')).toBe(true);
  });
  it('does NOT open mid-sentence, after a space, or on a normal message', () => {
    expect(isSlashTrigger('')).toBe(false);
    expect(isSlashTrigger('hello /clear')).toBe(false);
    expect(isSlashTrigger('/clear now')).toBe(false);
    expect(isSlashTrigger('what is 1/2')).toBe(false);
  });
});

describe('filterSlashCommands', () => {
  it('"/" returns the full list', () => {
    expect(filterSlashCommands('/').map((c) => c.name)).toEqual(
      SLASH_COMMANDS.map((c) => c.name),
    );
  });
  it('filters by the text after the slash, case-insensitively', () => {
    expect(filterSlashCommands('/cl').map((c) => c.name)).toEqual(['/clear', '/clearview']);
    expect(filterSlashCommands('/HELP').map((c) => c.name)).toEqual(['/help']);
  });
  it('returns [] when the draft is not a slash trigger', () => {
    expect(filterSlashCommands('hello')).toEqual([]);
    expect(filterSlashCommands('/clear extra')).toEqual([]);
  });
  it('returns [] for a non-matching query', () => {
    expect(filterSlashCommands('/zzz')).toEqual([]);
  });
});

describe('normalizeSlashName', () => {
  it('adds a leading slash to bare names', () => {
    expect(normalizeSlashName('context')).toBe('/context');
    expect(normalizeSlashName('review')).toBe('/review');
  });
  it('leaves already-slashed names untouched', () => {
    expect(normalizeSlashName('/clear')).toBe('/clear');
  });
  it('trims whitespace and treats empty as empty', () => {
    expect(normalizeSlashName('  compact  ')).toBe('/compact');
    expect(normalizeSlashName('   ')).toBe('');
  });
});

describe('isCompactCommand', () => {
  it('matches bare /compact and /compact with trailing instructions', () => {
    expect(isCompactCommand('/compact')).toBe(true);
    expect(isCompactCommand('/compact focus on the API')).toBe(true);
    expect(isCompactCommand('  /compact   ')).toBe(true); // trimmed
    expect(isCompactCommand('/compact  double-spaced args')).toBe(true);
  });
  it('does NOT match other commands or lookalike words', () => {
    expect(isCompactCommand('/context')).toBe(false);
    expect(isCompactCommand('/clear')).toBe(false);
    expect(isCompactCommand('/compaction')).toBe(false); // different word
    expect(isCompactCommand('')).toBe(false);
    expect(isCompactCommand('compact this please')).toBe(false); // no leading slash
  });
});

describe('composeSlashCommands', () => {
  it('does not leak Claude-only curated commands into Codex', () => {
    expect(composeSlashCommands([], 'codex').map((c) => c.name)).toEqual(['/clearview', '/help']);
    expect(composeSlashCommands(['status'], 'codex').map((c) => c.name)).toEqual([
      '/clearview',
      '/help',
      '/status',
    ]);
  });
  it('returns the curated list unchanged when no session commands are reported (pre-init fallback)', () => {
    expect(composeSlashCommands([])).toEqual(SLASH_COMMANDS);
    expect(composeSlashCommands([])).not.toBe(SLASH_COMMANDS); // a copy, not the same ref
  });

  it('forwards every advertised command verbatim (built-in + custom + skills)', () => {
    const composed = composeSlashCommands(['context', 'review', 'security-review', 'cost']);
    const byName = new Map(composed.map((c) => [c.name, c]));
    expect(byName.get('/context')?.action).toBe('forward');
    expect(byName.get('/review')?.action).toBe('forward'); // skill
    expect(byName.get('/security-review')?.action).toBe('forward'); // skill
    expect(byName.get('/cost')?.action).toBe('forward');
  });

  it('tolerates advertised names that already carry a leading slash', () => {
    const composed = composeSlashCommands(['/context', 'review']);
    const names = composed.map((c) => c.name).sort();
    expect(names).toContain('/context');
    expect(names).toContain('/review');
  });

  it('always includes /clear (confirm-guarded), /clearview, and /help even if the session omits them', () => {
    const composed = composeSlashCommands(['context']);
    const byName = new Map(composed.map((c) => [c.name, c]));
    expect(byName.get('/clear')?.action).toBe('clear-real'); // confirm-first reset
    expect(byName.get('/clearview')?.action).toBe('clear'); // client-only view hide
    expect(byName.get('/help')?.action).toBe('help'); // client cheatsheet
  });

  it('reclassifies a session-advertised /clear to the confirm guard (not plain forward)', () => {
    const composed = composeSlashCommands(['clear', 'context']);
    const clear = composed.find((c) => c.name === '/clear');
    expect(clear?.action).toBe('clear-real');
    // exactly one /clear entry (no duplicate forward + clear-real)
    expect(composed.filter((c) => c.name === '/clear')).toHaveLength(1);
  });

  it('does NOT duplicate /clearview or /help if the session also advertises them', () => {
    const composed = composeSlashCommands(['clearview', 'help', 'context']);
    expect(composed.filter((c) => c.name === '/clearview')).toHaveLength(1);
    expect(composed.filter((c) => c.name === '/help')).toHaveLength(1);
    expect(composed.find((c) => c.name === '/help')?.action).toBe('help');
  });

  it('sorts the merged list alphabetically by name', () => {
    const names = composeSlashCommands(['review', 'context', 'agents', 'memory']).map((c) => c.name);
    const sorted = [...names].sort();
    expect(names).toEqual(sorted);
  });

  it('drops blank/whitespace entries from the advertised set', () => {
    const composed = composeSlashCommands(['context', '   ', '']);
    expect(composed.map((c) => c.name)).toContain('/context');
    expect(composed.some((c) => c.name === '/' || c.name === '')).toBe(false);
  });

  it('retains the known description for built-in commands, blank for the rest', () => {
    const composed = composeSlashCommands(['context', 'compact', 'review', 'supabase:supabase']);
    const byName = new Map(composed.map((c) => [c.name, c]));
    expect(byName.get('/context')?.description).toBe('Show context-window usage');
    expect(byName.get('/compact')?.description).toBe(
      'Summarize the conversation to free up context',
    );
    expect(byName.get('/review')?.description).toBe(''); // skill — no known description
    expect(byName.get('/supabase:supabase')?.description).toBe(''); // namespaced plugin
  });

  it('round-trips namespaced plugin commands (colon kept, leading slash added)', () => {
    const composed = composeSlashCommands([
      'android-publish:android-ota',
      'supabase:supabase',
      'supabase:supabase-postgres-best-practices',
    ]);
    const names = composed.map((c) => c.name);
    expect(names).toContain('/android-publish:android-ota');
    expect(names).toContain('/supabase:supabase');
    // filterSlashCommands still finds namespaced commands by the text after the slash.
    expect(filterSlashCommands('/android', composed).map((c) => c.name)).toContain(
      '/android-publish:android-ota',
    );
    expect(filterSlashCommands('/supabase', composed).map((c) => c.name)).toEqual([
      '/supabase:supabase',
      '/supabase:supabase-postgres-best-practices',
    ]);
  });
});

describe('stepSlashSelection', () => {
  it('moves down + wraps', () => {
    expect(stepSlashSelection(0, 3, 1)).toBe(1);
    expect(stepSlashSelection(2, 3, 1)).toBe(0); // wrap to top
  });
  it('moves up + wraps', () => {
    expect(stepSlashSelection(1, 3, -1)).toBe(0);
    expect(stepSlashSelection(0, 3, -1)).toBe(2); // wrap to bottom
  });
  it('clamps a stale out-of-range index back in', () => {
    expect(stepSlashSelection(9, 3, 1)).toBe(1); // 9%3=0 → +1 = 1
  });
  it('returns 0 for an empty list', () => {
    expect(stepSlashSelection(0, 0, 1)).toBe(0);
  });
});

// ---------------------------------------------------------------------------
// AskUserQuestion keyboard answer mapping
// ---------------------------------------------------------------------------

describe('digitToOptionIndex', () => {
  it('maps 1–9 to 0–8 and rejects everything else', () => {
    expect(digitToOptionIndex('1')).toBe(0);
    expect(digitToOptionIndex('9')).toBe(8);
    expect(digitToOptionIndex('0')).toBeNull();
    expect(digitToOptionIndex('a')).toBeNull();
    expect(digitToOptionIndex('Enter')).toBeNull();
  });
});

const singleQ: AnswerQuestion[] = [
  { multiSelect: false, options: [{ label: 'Apple' }, { label: 'Banana' }, { label: 'Cherry' }] },
];
const multiQ: AnswerQuestion[] = [
  { multiSelect: true, options: [{ label: 'Red' }, { label: 'Green' }, { label: 'Blue' }] },
];
const twoQ: AnswerQuestion[] = [
  { multiSelect: false, options: [{ label: 'A1' }, { label: 'A2' }] },
  { multiSelect: false, options: [{ label: 'B1' }, { label: 'B2' }] },
];

describe('applyQuestionKey', () => {
  it('single-select single-question: a digit answers immediately', () => {
    const r = applyQuestionKey('2', singleQ, [[]], 0);
    expect(r.kind).toBe('answer');
    if (r.kind === 'answer') expect(r.sel).toEqual([['Banana']]);
  });

  it('single-select: an out-of-range digit is ignored', () => {
    expect(applyQuestionKey('5', singleQ, [[]], 0).kind).toBe('none'); // only 3 options
  });

  it('single-select: Enter does nothing (a digit is the answer)', () => {
    expect(applyQuestionKey('Enter', singleQ, [[]], 0).kind).toBe('none');
  });

  it('multi-select: digits toggle (update), Enter confirms when ≥1 picked', () => {
    let sel: string[][] = [[]];
    let r = applyQuestionKey('1', multiQ, sel, 0); // toggle Red on
    expect(r.kind).toBe('update');
    if (r.kind === 'update') sel = r.sel;
    expect(sel).toEqual([['Red']]);

    r = applyQuestionKey('3', multiQ, sel, 0); // toggle Blue on
    if (r.kind === 'update') sel = r.sel;
    expect(sel).toEqual([['Red', 'Blue']]);

    r = applyQuestionKey('1', multiQ, sel, 0); // toggle Red off
    if (r.kind === 'update') sel = r.sel;
    expect(sel).toEqual([['Blue']]);

    const confirm = applyQuestionKey('Enter', multiQ, sel, 0);
    expect(confirm.kind).toBe('answer');
    if (confirm.kind === 'answer') expect(confirm.sel).toEqual([['Blue']]);
  });

  it('multi-select: Enter with no pick is a no-op (incomplete)', () => {
    expect(applyQuestionKey('Enter', multiQ, [[]], 0).kind).toBe('none');
  });

  it('multi-question: each question accumulates via update; Enter confirms when all answered', () => {
    let sel: string[][] = [[], []];
    let r = applyQuestionKey('1', twoQ, sel, 0); // answer Q0 (radio in a multi set)
    expect(r.kind).toBe('update');
    if (r.kind === 'update') sel = r.sel;
    expect(sel).toEqual([['A1'], []]);
    // Q0 done; Enter still incomplete (Q1 unanswered).
    expect(applyQuestionKey('Enter', twoQ, sel, 1).kind).toBe('none');
    r = applyQuestionKey('2', twoQ, sel, 1); // answer Q1
    if (r.kind === 'update') sel = r.sel;
    expect(sel).toEqual([['A1'], ['B2']]);
    const confirm = applyQuestionKey('Enter', twoQ, sel, 1);
    expect(confirm.kind).toBe('answer');
    if (confirm.kind === 'answer') expect(confirm.sel).toEqual([['A1'], ['B2']]);
  });

  it('Escape always skips', () => {
    expect(applyQuestionKey('Escape', singleQ, [[]], 0).kind).toBe('skip');
    expect(applyQuestionKey('Escape', multiQ, [['Red']], 0).kind).toBe('skip');
  });

  it('an unrelated key is a no-op', () => {
    expect(applyQuestionKey('x', singleQ, [[]], 0).kind).toBe('none');
  });
});

describe('isAnswerComplete', () => {
  it('true only when every question has at least one pick', () => {
    expect(isAnswerComplete(twoQ, [['A1'], ['B1']])).toBe(true);
    expect(isAnswerComplete(twoQ, [['A1'], []])).toBe(false);
    expect(isAnswerComplete([], [])).toBe(false);
  });
});

describe('findAtMention / applyAtMention (@-file picker)', () => {
  it('triggers at start of draft', () => {
    expect(findAtMention('@src', 4)).toEqual({ query: 'src', start: 0 });
  });

  it('triggers mid-message after whitespace', () => {
    const draft = 'fix the bug in @src/ap please';
    // caret right after "ap" (index 22)
    expect(findAtMention(draft, 22)).toEqual({ query: 'src/ap', start: 15 });
  });

  it('bare "@" triggers with an empty query (browse immediately)', () => {
    expect(findAtMention('hello @', 7)).toEqual({ query: '', start: 6 });
  });

  it('never triggers inside an email-like token', () => {
    expect(findAtMention('mail a@b.com now', 12)).toBeNull();
  });

  it('never triggers when the caret is outside the token', () => {
    const draft = '@src/api.ts and more';
    expect(findAtMention(draft, draft.length)).toBeNull(); // caret in "more"
  });

  it('a completed mention (trailing space) stops triggering', () => {
    const draft = '@src/api.ts ';
    expect(findAtMention(draft, draft.length)).toBeNull();
  });

  it('a second @ inside the token disqualifies it', () => {
    expect(findAtMention('@a@b', 4)).toBeNull();
  });

  it('applyAtMention splices "@<rel> " and moves the caret after the space', () => {
    const draft = 'fix @ap then send';
    const m = findAtMention(draft, 7); // token "@ap", caret after "ap"
    expect(m).toEqual({ query: 'ap', start: 4 });
    const res = applyAtMention(draft, m!.start, 7, 'src/api.ts');
    expect(res.draft).toBe('fix @src/api.ts  then send');
    expect(res.caret).toBe('fix @src/api.ts '.length);
    // The invariant that self-closes the menu: no active token at the new caret.
    expect(findAtMention(res.draft, res.caret)).toBeNull();
  });

  it('applyAtMention at end of draft', () => {
    const res = applyAtMention('@rea', 0, 4, 'README.md');
    expect(res.draft).toBe('@README.md ');
    expect(res.caret).toBe(11);
    expect(findAtMention(res.draft, res.caret)).toBeNull();
  });
});

describe('/model slash command', () => {
  it('is a client-side action in the curated list', () => {
    const model = SLASH_COMMANDS.find((c) => c.name === '/model');
    expect(model?.action).toBe('model');
  });
  it('is ALWAYS overlaid onto a session-advertised menu (claude never advertises /model)', () => {
    const merged = composeSlashCommands(['context', 'compact', 'review']);
    const model = merged.find((c) => c.name === '/model');
    expect(model?.action).toBe('model');
  });
  it('a session that (hypothetically) advertises "model" cannot demote it to a forward', () => {
    const merged = composeSlashCommands(['model', 'context']);
    const model = merged.find((c) => c.name === '/model');
    expect(model?.action).toBe('model');
  });
});
