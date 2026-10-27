// nativeComposer.ts — pure, unit-testable helpers for the native pane composer's
// keyboard-first surface: prompt history (↑/↓ recall), the client-side
// slash-command menu, and the keyboard cheatsheet. Kept out of the React
// component so the state machines can be tested in isolation (no DOM).

import type { NativeEvent } from './nativeEvents';

// ---------------------------------------------------------------------------
// Prompt history
// ---------------------------------------------------------------------------

// derivePromptHistory — the user prompts sent in THIS pane, oldest→newest.
// Derived from the folded event list: every user_text is a prompt the user sent
// (seed/replay + live echo alike). We dedup consecutive identical entries only
// when they're the SAME seq (the dedup that matters is already done in
// foldEvent); back-to-back repeats ("yes", "yes") are legitimately separate
// history entries and kept.
//
// `excludeForwardNames` (optional) is the set of slash-command names that forward
// to the agent and therefore should NOT be recalled as prompts. When omitted, the
// curated set is used; NativePane passes the SESSION's composed forward commands
// (built-in + custom + skills + MCP) so a forwarded /review or /context alike is
// kept out of ↑/↓ recall.
export function derivePromptHistory(events: NativeEvent[], excludeForwardNames?: ReadonlySet<string>): string[] {
  const exclude = excludeForwardNames ?? CURATED_FORWARD_NAMES;
  const out: string[] = [];
  for (const e of events) {
    if (e.type !== 'user_text') continue;
    const t = e.text ?? '';
    if (!t) continue;
    // Skip <task-notification> blobs — these are Claude Code's background-task
    // completion notices, injected as USER-role messages but NOT real prompts the
    // user typed. The visible transcript hides them (nativeTranscript.ts), so they
    // must also be kept out of ↑/↓ recall — otherwise pressing Up surfaces phantom
    // "<task-notification><task-id>…" entries that the user never sent.
    if (/^\s*<task-notification/.test(t)) continue;
    // Skip forwarded slash commands (/context, /cost, /config, /review, …) —
    // they're commands, not prompts to recall with ↑. Match the EXACT known
    // command names only, so a legit prompt that happens to be a path
    // ("/etc/hosts") is still recalled.
    if (exclude.has(t.trim())) continue;
    out.push(t);
  }
  return out;
}

// HistoryNav models the ↑/↓ recall state machine over a prompt list. `index` is
// null when NOT browsing history (the live draft is whatever the user typed);
// otherwise it points into `history` (0 = oldest, length-1 = newest). `saved`
// holds the in-progress draft captured on first entry so ↓ past the newest
// restores it.
export interface HistoryNav {
  index: number | null;
  saved: string;
}

export const HISTORY_IDLE: HistoryNav = { index: null, saved: '' };

// recallOlder — ↑. Returns the next state + the text to put in the composer, or
// null when there is nothing to do (empty history, or already at the oldest).
// On first entry it captures `currentDraft` into `saved` so ↓ can restore it.
export function recallOlder(
  nav: HistoryNav,
  history: string[],
  currentDraft: string,
): { nav: HistoryNav; text: string } | null {
  if (history.length === 0) return null;
  if (nav.index === null) {
    // Enter history at the newest entry, saving the live draft.
    const index = history.length - 1;
    return { nav: { index, saved: currentDraft }, text: history[index] };
  }
  if (nav.index <= 0) return null; // already at the oldest
  const index = nav.index - 1;
  return { nav: { ...nav, index }, text: history[index] };
}

// recallNewer — ↓. Steps toward newer entries; stepping past the newest exits
// history and restores the saved draft. Returns null when not browsing.
export function recallNewer(
  nav: HistoryNav,
  history: string[],
): { nav: HistoryNav; text: string } | null {
  if (nav.index === null) return null;
  if (nav.index >= history.length - 1) {
    // Past the newest → exit history, restore the saved in-progress draft.
    return { nav: HISTORY_IDLE, text: nav.saved };
  }
  const index = nav.index + 1;
  return { nav: { ...nav, index }, text: history[index] };
}

// ---------------------------------------------------------------------------
// Slash-command menu (client-side)
// ---------------------------------------------------------------------------

// A client-side slash command. `action` is an id NativePane maps to a handler
// (kept out of this pure module so the data stays serializable/testable). The
// list is a small extensible array — add an entry + handle its id to add more.
export interface SlashCommand {
  name: string; // e.g. "/clear" (includes the leading slash)
  description: string;
  // 'forward'    → sent verbatim to the agent; the headless CLI runs it locally and
  //   returns its output as a slash_result card (/context, /cost, /config, /compact).
  // 'clear-real' → forwards "/clear": REALLY wipes the agent's memory (confirm first).
  // 'clear'      → client-only view hide (does NOT reset the agent's memory).
  // 'help'       → keyboard cheatsheet overlay.
  // 'model'      → client-side model picker (headless claude has no /model; the
  //   pick calls POST /api/panes/{id}/model, which respawns with --model).
  action: 'clear' | 'help' | 'forward' | 'clear-real' | 'model';
}

export const SLASH_COMMANDS: SlashCommand[] = [
  { name: '/context', description: 'Show context-window usage', action: 'forward' },
  { name: '/compact', description: 'Summarize the conversation to free up context', action: 'forward' },
  { name: '/clear', description: "Reset Claude's memory — start a fresh conversation", action: 'clear-real' },
  { name: '/cost', description: 'Show your usage — session, today, this week', action: 'forward' },
  { name: '/config', description: 'Show current settings (type /config key=value to change)', action: 'forward' },
  { name: '/clearview', description: 'Hide these messages (view only — Claude keeps its memory)', action: 'clear' },
  { name: '/help', description: 'Show keyboard shortcuts', action: 'help' },
  { name: '/model', description: 'Change the Claude model for this session', action: 'model' },
];

// The curated forward-command names (those sent verbatim to the agent): the
// default exclusion set for prompt history when no dynamic session list is
// available yet. Computed once at module load, AFTER SLASH_COMMANDS is defined.
const CURATED_FORWARD_NAMES: ReadonlySet<string> = new Set(
  SLASH_COMMANDS.filter((c) => c.action === 'forward' || c.action === 'clear-real').map((c) => c.name),
);

// The purely-client actions — always present in the menu regardless of what the
// session advertises. /clearview hides rows locally (no agent round-trip); /help
// opens the keyboard cheatsheet. /clear is RECLASSIFIED to clear-real (the
// confirm-first destructive reset) so its guard survives dynamic merging even
// when the session lists /clear as a plain forwardable command.
const LOCAL_CLEAR_REAL: SlashCommand = {
  name: '/clear',
  description: "Reset Claude's memory — start a fresh conversation",
  action: 'clear-real',
};
const LOCAL_CLEAR_VIEW: SlashCommand = {
  name: '/clearview',
  description: 'Hide these messages (view only — Claude keeps its memory)',
  action: 'clear',
};
const LOCAL_HELP: SlashCommand = { name: '/help', description: 'Show keyboard shortcuts', action: 'help' };
// /model is TUI-only in claude, so the session never advertises it — always
// overlay our client picker (which drives the daemon's respawn-with---model).
const LOCAL_MODEL: SlashCommand = {
  name: '/model',
  description: 'Change the Claude model for this session',
  action: 'model',
};

// normalizeSlashName — coerce a session-reported command name to the "/name" form
// the menu + send path expect. claude's stream-json init `slash_commands` array
// is a list of bare names ("context", "review", "supabase:supabase") OR
// slash-prefixed ("/context"); tolerate both so a CLI version change in either
// direction can't break the menu. Plugin namespaced names ("android-publish:android-ota")
// keep their colon — that IS the invocation form claude expects.
export function normalizeSlashName(raw: string): string {
  const s = raw.trim();
  if (!s) return '';
  return s.startsWith('/') ? s : '/' + s;
}

// isCompactCommand — true when a user turn is a /compact invocation, WITH OR
// WITHOUT trailing summarization instructions ("/compact" or "/compact focus on
// the API"). Used to arm the "Compacting conversation…" metabar label across
// EVERY send path (menu run, queued-steer flush, and a Tab/click-completed
// fill-then-Enter submit), so the label can't go missing just because the user
// added instructions — or because they completed /compact via Tab (which routes
// through submit() rather than runSlashCommand). Note this is a token-prefix
// match, so "/compaction" (a different word) is NOT matched.
export function isCompactCommand(text: string): boolean {
  const t = text.trim();
  return t === '/compact' || t.startsWith('/compact ');
}

// KNOWN_DESCRIPTIONS — descriptions for the built-in commands we already had
// copy for, keyed by the normalized "/name". claude's init only reports NAMES
// (no descriptions), so without this map every dynamic row would render blank.
// We fill in the handful we know; the rest show their name only (still usable —
// the menu's job is discovery + invocation). Verified against the live
// session_init set: claude advertises only headless-applicable commands here
// (skills + custom commands + headless built-ins like clear/compact/context/
// config/init/usage); it does NOT list TUI-only commands (/model, /login, …),
// so no denylist is needed — every advertised command works when forwarded.
const KNOWN_DESCRIPTIONS: ReadonlyMap<string, string> = new Map(
  SLASH_COMMANDS.filter((c) => c.action === 'forward').map((c) => [c.name, c.description]),
);

// composeSlashCommands — build the Focus slash menu from the session's ADVERTISED
// command set. `dynamicNames` is the `slash_commands` array the daemon captures
// from the stream-json init event (already plumbed to sessionInit.slashCommands):
// the union of built-in commands, custom commands, skills, and MCP commands
// available in THIS session — i.e. exactly what Term mode's interactive `/` menu
// offers. Each dynamic entry forwards VERBATIM to the headless agent, which runs
// it locally and returns the output as a slash_result card (/context, /cost) or
// executes it as a normal turn (skills like /review, /compact with args). Result
// rendering is already generic on the daemon side, so forwarding is all that's
// needed for parity.
//
// When `dynamicNames` is empty (no session_init yet, or the session reported
// none), the curated SLASH_COMMANDS list is returned unchanged so the menu still
// shows the core forward commands pre-init — no blank-menu regression.
export function composeSlashCommands(dynamicNames: string[], agent: 'claude' | 'codex' | 'muse' = 'claude'): SlashCommand[] {
  if (agent === 'codex' || agent === 'muse') {
    const commands = new Map<string, SlashCommand>();
    for (const raw of dynamicNames ?? []) {
      const name = normalizeSlashName(raw);
      if (!name || name === LOCAL_CLEAR_VIEW.name || name === LOCAL_HELP.name) continue;
      commands.set(name, { name, description: KNOWN_DESCRIPTIONS.get(name) ?? '', action: 'forward' });
    }
    commands.set(LOCAL_CLEAR_VIEW.name, { ...LOCAL_CLEAR_VIEW, description: 'Hide these messages (view only — Codex keeps its memory)' });
    commands.set(LOCAL_HELP.name, LOCAL_HELP);
    return Array.from(commands.values()).sort((a, b) => a.name.localeCompare(b.name));
  }
  if (!dynamicNames || dynamicNames.length === 0) return SLASH_COMMANDS.slice();

  const byName = new Map<string, SlashCommand>();
  for (const raw of dynamicNames) {
    const name = normalizeSlashName(raw);
    if (!name) continue;
    // Reserved for the local-only / guarded overlays below — never a plain forward.
    if (name === LOCAL_CLEAR_REAL.name || name === LOCAL_CLEAR_VIEW.name || name === LOCAL_HELP.name || name === LOCAL_MODEL.name) continue;
    byName.set(name, { name, description: KNOWN_DESCRIPTIONS.get(name) ?? '', action: 'forward' });
  }
  // Overlay the client-only + guarded actions. /clear is ALWAYS offered (even if
  // the session omits it) with its confirm-first guard; /clearview + /help are
  // pure client features no agent advertises.
  byName.set(LOCAL_CLEAR_REAL.name, LOCAL_CLEAR_REAL);
  byName.set(LOCAL_CLEAR_VIEW.name, LOCAL_CLEAR_VIEW);
  byName.set(LOCAL_HELP.name, LOCAL_HELP);
  byName.set(LOCAL_MODEL.name, LOCAL_MODEL);
  return Array.from(byName.values()).sort((a, b) => a.name.localeCompare(b.name));
}

// isSlashTrigger — the composer opens the slash menu only when the WHOLE draft
// is a slash-prefixed token with no spaces (so typing "/" first opens it, and a
// "/foo bar" mid-sentence or a normal message never does). An empty string is
// not a trigger.
export function isSlashTrigger(draft: string): boolean {
  return /^\/\S*$/.test(draft);
}

// filterSlashCommands — case-insensitive prefix-ish filter of the command list
// by the draft (which starts with "/"). "/" alone returns all; "/cl" returns
// /clear. Matches commands whose name contains the query after the slash.
export function filterSlashCommands(
  draft: string,
  commands: SlashCommand[] = SLASH_COMMANDS,
): SlashCommand[] {
  if (!isSlashTrigger(draft)) return [];
  const q = draft.slice(1).toLowerCase(); // text after the "/"
  if (!q) return commands.slice();
  return commands.filter((c) => c.name.slice(1).toLowerCase().includes(q));
}

// stepSlashSelection — the ↑/↓ wrap-around cursor over the filtered command
// list. Pure: given the current highlighted index, the list length, and a
// direction (+1 = ↓/next, -1 = ↑/prev), return the next index. Clamps an
// out-of-range index back in (the list reflows as the filter narrows), and
// returns 0 for an empty list (caller guards on length before acting).
export function stepSlashSelection(index: number, length: number, dir: 1 | -1): number {
  if (length <= 0) return 0;
  // Normalize a stale index (the filter may have shrunk the list under us).
  const cur = ((index % length) + length) % length;
  return ((cur + dir) % length + length) % length;
}

// ---------------------------------------------------------------------------
// @-file mention menu (client-side)
//
// Typing "@" (at the start of the draft or after whitespace) opens a fuzzy
// file picker over the pane's cwd (api.searchFiles). Unlike the slash menu it
// works MID-message — that's how claude prompts reference files ("fix the bug
// in @src/api.ts please"). Picking a hit splices "@<rel> " into the draft;
// sending is unchanged (the headless claude resolves @path itself).
// ---------------------------------------------------------------------------

// An active @-mention token under the caret: `query` is the text between the
// "@" and the caret; `start` is the index of the "@" in the draft.
export interface AtMention {
  query: string;
  start: number;
}

// findAtMention — the @-token containing the caret, or null. A token counts
// only when its "@" sits at start-of-draft or right after whitespace, so
// emails ("a@b.com") and mid-word @s never trigger. Works mid-message: only
// the token UNDER THE CARET matters, so earlier completed mentions in the
// draft don't re-open the menu. A second "@" inside the token disqualifies it.
export function findAtMention(draft: string, caret: number): AtMention | null {
  const c = Math.max(0, Math.min(caret, draft.length));
  // Scan back from the caret to the token start (whitespace boundary).
  let i = c;
  while (i > 0 && !/\s/.test(draft[i - 1])) i--;
  if (draft[i] !== '@') return null;
  const query = draft.slice(i + 1, c);
  if (query.includes('@')) return null;
  return { query, start: i };
}

// applyAtMention — splice the picked file in as "@<rel> ", replacing the
// active token from its "@" through the caret. The trailing space ends the
// token, so findAtMention returns null afterwards and the menu closes itself —
// the same self-closing trick completeSlashCommand uses. Returns the new draft
// and caret position (right after the inserted space).
export function applyAtMention(
  draft: string,
  start: number,
  caret: number,
  rel: string,
): { draft: string; caret: number } {
  const c = Math.max(start, Math.min(caret, draft.length));
  const insert = '@' + rel + ' ';
  return {
    draft: draft.slice(0, start) + insert + draft.slice(c),
    caret: start + insert.length,
  };
}

// ---------------------------------------------------------------------------
// Keyboard answer mapping for AskUserQuestion (digits 1–9, Enter, Esc).
// ---------------------------------------------------------------------------

// A normalized question the keyboard handler reasons about. Mirrors the
// {label, multiSelect, options} shape NativeEventViews parses from the wire,
// but kept structurally minimal + framework-free so the selection state machine
// is unit-testable without the renderer.
export interface AnswerOption {
  label: string;
}
export interface AnswerQuestion {
  multiSelect?: boolean;
  options: AnswerOption[];
}

// digitToOptionIndex — map a KeyboardEvent.key for digits '1'..'9' to a 0-based
// option index, or null for any other key. '1' → 0, … '9' → 8.
export function digitToOptionIndex(key: string): number | null {
  if (key.length !== 1 || key < '1' || key > '9') return null;
  return key.charCodeAt(0) - '1'.charCodeAt(0);
}

// isAnswerComplete — every question has at least one pick. Drives whether Enter
// can confirm a multi-question / multi-select answer.
export function isAnswerComplete(questions: AnswerQuestion[], sel: string[][]): boolean {
  if (questions.length === 0) return false;
  return sel.every((s) => s.length > 0);
}

// QuestionKeyAction is the outcome of a keystroke on an active AskUserQuestion.
//  - 'answer': commit `sel` now (single-select single-question immediate answer,
//    or a confirm on a complete multi answer). Caller builds the reason string.
//  - 'update': `sel` changed (a multi-select toggle); re-render, don't send yet.
//  - 'skip': decline the question (Esc).
//  - 'none': the keystroke was not handled here (let it fall through).
export type QuestionKeyAction =
  | { kind: 'answer'; sel: string[][] }
  | { kind: 'update'; sel: string[][] }
  | { kind: 'skip' }
  | { kind: 'none' };

// applyQuestionKey — the pure selection state machine for the keyboard-driven
// AskUserQuestion card. `qi` is the question index a digit applies to (the
// keyboard answers ONE question at a time; the UI advances qi as each is
// answered — for the common single-question case qi is always 0).
//
//  - A digit 1–9 selecting a valid option:
//      • single-select single-question  → immediate 'answer' (one pick).
//      • multi-select / multi-question   → toggle that option → 'update'.
//  - Enter:
//      • single-question single-select   → 'none' (a digit already answers; an
//        Enter with no pick has nothing to send — fall through to the composer).
//      • otherwise, when complete         → 'answer' with the accumulated sel.
//        when incomplete                  → 'none' (nothing to confirm yet).
//  - Escape → 'skip'.
//  - anything else → 'none'.
export function applyQuestionKey(
  key: string,
  questions: AnswerQuestion[],
  sel: string[][],
  qi: number,
): QuestionKeyAction {
  if (questions.length === 0) return { kind: 'none' };
  const single = questions.length === 1 && !questions[0].multiSelect;

  if (key === 'Escape') return { kind: 'skip' };

  if (key === 'Enter') {
    if (single) return { kind: 'none' };
    if (isAnswerComplete(questions, sel)) return { kind: 'answer', sel };
    return { kind: 'none' };
  }

  const oi = digitToOptionIndex(key);
  if (oi != null) {
    const q = questions[qi];
    if (!q || oi >= q.options.length) return { kind: 'none' }; // out-of-range digit
    const label = q.options[oi].label;
    if (single) {
      // Single-select single-question: answer immediately with this one pick.
      const next = questions.map((_, i) => (i === qi ? [label] : []));
      return { kind: 'answer', sel: next };
    }
    // Multi-select / multi-question: toggle the option in this question's row.
    const next = sel.map((a) => a.slice());
    while (next.length < questions.length) next.push([]);
    const cur = next[qi] ?? (next[qi] = []);
    if (q.multiSelect) {
      const idx = cur.indexOf(label);
      if (idx >= 0) cur.splice(idx, 1);
      else cur.push(label);
    } else {
      // A non-multiSelect question inside a multi-question set: radio behavior.
      next[qi] = cur[0] === label ? [] : [label];
    }
    return { kind: 'update', sel: next };
  }

  return { kind: 'none' };
}

// ---------------------------------------------------------------------------
// Keyboard cheatsheet (surfaced by /help)
// ---------------------------------------------------------------------------

export interface Shortcut {
  keys: string;
  desc: string;
}

export const KEYBOARD_SHORTCUTS: Shortcut[] = [
  { keys: '↵', desc: 'Send message' },
  { keys: '⇧↵', desc: 'New line' },
  { keys: 'Esc', desc: 'Stop the turn · deny a pending approval · skip a question' },
  { keys: '↵', desc: 'Approve a non-destructive tool (when one is pending)' },
  { keys: '⇧A', desc: 'Approve all pending tool requests' },
  { keys: '1–9', desc: 'Pick a question option (Enter confirms multi-select)' },
  { keys: '↑ / ↓', desc: 'Recall earlier / later prompts (empty composer)' },
  { keys: '/', desc: 'Open the slash-command menu (empty composer)' },
];
