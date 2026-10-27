// modeSwitch.ts — pure, unit-testable helpers for the graceful Terminal↔Native
// renderer switch. Kept out of the React components so the "is the pane working,
// so we must confirm before interrupting?" predicate and the dismissed-notice
// keying can be tested without a DOM.

import type { PaneRuntimeState } from '../types';

// The pane runtime states that mean a turn is IN FLIGHT (or an approval is
// pending) — i.e. flipping the renderer now would abort live work. Switching
// while the pane is in any of these states must go through the inline confirm;
// any other state ('idle', 'awaiting-input', 'exited') switches one-click.
//
// 'awaiting-input' is deliberately NOT here: the agent is parked waiting on the
// USER, so a switch interrupts nothing the model is doing. 'choice-prompt' IS
// here because an approval is pending and the turn is mid-tool — switching drops
// that pending tool call.
const WORKING_STATES: ReadonlySet<PaneRuntimeState> = new Set<PaneRuntimeState>([
  'busy',
  'recent',
  'choice-prompt',
]);

// shouldConfirmModeSwitch — true when clicking the renderer toggle should pop the
// "Claude is working — interrupt?" confirm instead of switching immediately.
//
// `agentMidTurn` is the daemon's prompt-aware signal (a visible Claude prompt box
// reads "not working"). Prefer it when present: the coarse `state` keeps reading
// 'busy'/'recent' for ~3s after a terminal turn ends (final redraws / cursor
// blink), which made an idle pane wrongly offer the "switch when done" confirm —
// the bug this fixes. A live choice menu ('choice-prompt') ALWAYS confirms
// regardless (switching drops the pending tool). Falls back to the recency states
// only when agentMidTurn is absent (an older daemon). Defensive against an
// undefined state (treat as idle ⇒ no confirm).
export function shouldConfirmModeSwitch(
  state: PaneRuntimeState | undefined,
  agentMidTurn?: boolean,
): boolean {
  if (state === 'choice-prompt') return true;
  if (typeof agentMidTurn === 'boolean') return agentMidTurn;
  return state != null && WORKING_STATES.has(state);
}

// Human label for a renderer kind, for the confirm + interrupted-marker copy.
// `target` is the kind being switched TO ('' = terminal, 'native' = native).
export function modeLabel(kind: '' | 'native'): 'Terminal' | 'Focus' {
  return kind === 'native' ? 'Focus' : 'Terminal';
}

// ---------------------------------------------------------------------------
// One-time "Try Focus" discovery hint bookkeeping. The hint (rendered by
// PaneHeader under the mode toggle of a Terminal-mode Claude pane) exists to
// introduce Focus to users who have never touched it — so ANY Focus usage
// anywhere marks it seen forever: NativePane calls markFocusUsed() on mount
// (a Focus pane existing IS usage), and the header dismisses on ✕ / toggle
// touch. Lives here (not PaneHeader) because NativePane must reach it without
// a component-to-component import.
// ---------------------------------------------------------------------------

const FOCUS_HINT_KEY = 'solu.focusHintDismissed';

export function focusHintDismissed(): boolean {
  try { return localStorage.getItem(FOCUS_HINT_KEY) === '1'; } catch { return true; }
}

export function markFocusUsed(): void {
  try { localStorage.setItem(FOCUS_HINT_KEY, '1'); } catch { /* ignore */ }
}

// Label for the kind switched AWAY from, as carried in switchNotice.fromKind
// ('terminal' | 'native'). Same words, different source spelling.
export function fromKindLabel(fromKind: 'native' | 'terminal'): 'Terminal' | 'Focus' {
  return fromKind === 'native' ? 'Focus' : 'Terminal';
}

// The daemon's discontinuity notice (PaneInfo.switchNotice). Reason
// discriminates the banner copy: '' / absent = a mode switch interrupted the
// turn; 'resume-repair' = a restart cut off the previous response (the resume
// synthetically answered a dangling tool_use); 'fresh-start' = a requested
// --resume was dropped (no transcript on disk / session live elsewhere) and
// the pane started a NEW conversation — informational, nothing to continue.
export interface SwitchNotice {
  interrupted: boolean;
  fromKind: 'native' | 'terminal';
  at: number;
  reason?: 'resume-repair' | 'fresh-start';
}

// Stable identity for a switchNotice, used to key the per-pane "dismissed" set
// so a dismissed marker doesn't reappear for the SAME notice (but a fresh
// one — a new `at` — shows again). Null/absent notice → null key. Any live
// notice (interrupted or informational fresh-start) has a key.
export function switchNoticeKey(notice: SwitchNotice | null | undefined): number | null {
  return notice ? notice.at : null;
}

// Whether the notice banner should render: a live notice whose key the user
// hasn't dismissed in THIS session. Pure so the gate is testable.
export function shouldShowSwitchNotice(
  notice: SwitchNotice | null | undefined,
  dismissed: ReadonlySet<number>,
): boolean {
  const key = switchNoticeKey(notice);
  return key != null && !dismissed.has(key);
}

// ---------------------------------------------------------------------------
// Deferred mode switch — the three-way confirm ("switch when the turn finishes"
// vs "switch now" vs cancel) and the armed-but-waiting pending indicator.
// ---------------------------------------------------------------------------

// The shape the daemon's pane DTO carries while a deferred switch is armed and
// waiting for the current turn to finish (PaneInfo.pendingSwitch). `target` is
// the renderer being switched TO ('native' | 'terminal' — note 'terminal', not
// the '' kind spelling the API/store use). Cleared (→ null) by the daemon the
// moment the switch fires or is cancelled.
export interface PendingSwitch {
  target: 'native' | 'terminal';
  at: number;
}

// The user's choice in the three-way confirm. 'deferred' = lossless, switch when
// the turn finishes (the primary/default); 'now' = interrupt immediately.
export type SwitchChoice = 'deferred' | 'now';

// The POST body opts for api.switchPaneMode, derived from a confirm choice.
// Mirrors the WIRE CONTRACT: { kind, when?, cancel? }. `kind` is the API/store
// spelling ('' = terminal). `when` is the choice verbatim.
export interface SwitchRequest {
  when: SwitchChoice;
}

// switchRequestForChoice — map a confirm choice to the switchPaneMode opts. Pure
// so the component just forwards the result. `target` is included for symmetry /
// future use; the opts themselves only carry `when`.
export function switchRequestForChoice(choice: SwitchChoice, _target: '' | 'native'): SwitchRequest {
  return { when: choice };
}

// pendingSwitchLabel — the human target label for an armed pending switch
// ('Focus' | 'Terminal'), for the indicator + its tooltip/aria copy. Null
// pending → '' (caller renders nothing).
export function pendingSwitchLabel(pending: PendingSwitch | null | undefined): '' | 'Focus' | 'Terminal' {
  if (!pending) return '';
  return pending.target === 'native' ? 'Focus' : 'Terminal';
}

// pendingSwitchKind — normalize a pending switch's target ('native' | 'terminal')
// to the API/store kind spelling ('native' | ''), so a cancel/override can call
// switchPaneMode(id, kind, …) with the same value the toggle uses. Null pending
// → null (no target armed).
export function pendingSwitchKind(pending: PendingSwitch | null | undefined): '' | 'native' | null {
  if (!pending) return null;
  return pending.target === 'native' ? 'native' : '';
}

// toggleVisual — the visual state of the renderer toggle, precedence-resolved.
// 'flipping' (the round-8 in-progress state: paneModeSwitching, neutral '…',
// DISABLED) WINS over 'pending' (armed + waiting: pulse, ENABLED, clickable) so a
// switch that actually starts firing never looks merely armed. 'idle' otherwise.
export function toggleVisual(
  switching: boolean,
  pending: PendingSwitch | null | undefined,
): 'flipping' | 'pending' | 'idle' {
  if (switching) return 'flipping';
  if (pending) return 'pending';
  return 'idle';
}
