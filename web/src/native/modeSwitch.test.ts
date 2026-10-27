// Unit tests for the graceful mode-switch predicates: when to confirm before
// interrupting an in-flight turn, and the dismissed-notice keying that keeps the
// "interrupted — Continue" marker from reappearing for the SAME interruption.

import { describe, it, expect } from 'vitest';
import {
  shouldConfirmModeSwitch,
  modeLabel,
  fromKindLabel,
  switchNoticeKey,
  shouldShowSwitchNotice,
  switchRequestForChoice,
  pendingSwitchLabel,
  pendingSwitchKind,
  toggleVisual,
  type PendingSwitch,
} from './modeSwitch';
import type { PaneRuntimeState } from '../types';

describe('shouldConfirmModeSwitch', () => {
  it('confirms while a turn is in flight or an approval is pending (legacy state fallback)', () => {
    // No agentMidTurn arg → fall back to the coarse recency states.
    expect(shouldConfirmModeSwitch('busy')).toBe(true);
    expect(shouldConfirmModeSwitch('recent')).toBe(true);
    expect(shouldConfirmModeSwitch('choice-prompt')).toBe(true);
  });

  it('switches one-click when idle / awaiting the user / exited (legacy state fallback)', () => {
    expect(shouldConfirmModeSwitch('idle')).toBe(false);
    expect(shouldConfirmModeSwitch('awaiting-input')).toBe(false);
    expect(shouldConfirmModeSwitch('exited')).toBe(false);
  });

  it('treats an undefined state as idle (no confirm)', () => {
    expect(shouldConfirmModeSwitch(undefined)).toBe(false);
    // Defensive against an unexpected string slipping through.
    expect(shouldConfirmModeSwitch('???' as PaneRuntimeState)).toBe(false);
  });

  it('prefers the daemon agentMidTurn signal over the coarse state', () => {
    // The bug: a just-settled terminal pane reads state='busy'/'recent' for ~3s
    // (post-turn redraws) while the prompt box is already shown. With the
    // prompt-aware signal saying "not mid-turn", it must switch one-click — no
    // "switch when done" confirm.
    expect(shouldConfirmModeSwitch('busy', false)).toBe(false);
    expect(shouldConfirmModeSwitch('recent', false)).toBe(false);
    // And it confirms when the agent really IS mid-turn, even if state lagged.
    expect(shouldConfirmModeSwitch('idle', true)).toBe(true);
    expect(shouldConfirmModeSwitch('busy', true)).toBe(true);
  });

  it('always confirms on a live choice menu regardless of agentMidTurn', () => {
    // A pending menu/approval is the user's turn but switching drops the tool, so
    // it must confirm even though the daemon reports agentMidTurn=false for it.
    expect(shouldConfirmModeSwitch('choice-prompt', false)).toBe(true);
    expect(shouldConfirmModeSwitch('choice-prompt', true)).toBe(true);
  });
});

describe('modeLabel / fromKindLabel', () => {
  it('labels the target kind for the confirm copy', () => {
    expect(modeLabel('native')).toBe('Focus');
    expect(modeLabel('')).toBe('Terminal');
  });
  it('labels the kind switched away from (switchNotice.fromKind spelling)', () => {
    expect(fromKindLabel('native')).toBe('Focus');
    expect(fromKindLabel('terminal')).toBe('Terminal');
  });
});

describe('switchNoticeKey + shouldShowSwitchNotice', () => {
  const notice = { interrupted: true as const, fromKind: 'terminal' as const, at: 1717000000000 };

  it('keys a notice by its `at` timestamp', () => {
    expect(switchNoticeKey(notice)).toBe(1717000000000);
    expect(switchNoticeKey(null)).toBeNull();
    expect(switchNoticeKey(undefined)).toBeNull();
  });

  it('shows a live notice that has not been dismissed', () => {
    expect(shouldShowSwitchNotice(notice, new Set())).toBe(true);
  });

  it('hides a notice once its key is dismissed', () => {
    expect(shouldShowSwitchNotice(notice, new Set([notice.at]))).toBe(false);
  });

  it('shows a FRESH interruption (new `at`) even if an older one was dismissed', () => {
    const older = new Set([1717000000000]);
    const fresh = { ...notice, at: 1717000009999 };
    expect(shouldShowSwitchNotice(fresh, older)).toBe(true);
  });

  it('shows nothing when there is no notice', () => {
    expect(shouldShowSwitchNotice(null, new Set())).toBe(false);
    expect(shouldShowSwitchNotice(undefined, new Set())).toBe(false);
  });
});

describe('switchRequestForChoice', () => {
  it('maps the deferred (lossless) choice to when:deferred', () => {
    expect(switchRequestForChoice('deferred', 'native')).toEqual({ when: 'deferred' });
    expect(switchRequestForChoice('deferred', '')).toEqual({ when: 'deferred' });
  });
  it('maps the interrupt-now choice to when:now', () => {
    expect(switchRequestForChoice('now', 'native')).toEqual({ when: 'now' });
    expect(switchRequestForChoice('now', '')).toEqual({ when: 'now' });
  });
});

describe('pendingSwitchLabel', () => {
  it('labels the armed target', () => {
    expect(pendingSwitchLabel({ target: 'native', at: 1 })).toBe('Focus');
    expect(pendingSwitchLabel({ target: 'terminal', at: 1 })).toBe('Terminal');
  });
  it('returns empty for no pending switch', () => {
    expect(pendingSwitchLabel(null)).toBe('');
    expect(pendingSwitchLabel(undefined)).toBe('');
  });
});

describe('pendingSwitchKind', () => {
  it('normalizes the wire target to the API/store kind spelling', () => {
    expect(pendingSwitchKind({ target: 'native', at: 1 })).toBe('native');
    // 'terminal' (wire) → '' (kind), so a cancel/override calls switchPaneMode
    // with the same value the toggle uses.
    expect(pendingSwitchKind({ target: 'terminal', at: 1 })).toBe('');
  });
  it('returns null when nothing is armed', () => {
    expect(pendingSwitchKind(null)).toBeNull();
    expect(pendingSwitchKind(undefined)).toBeNull();
  });
});

describe('toggleVisual', () => {
  const pending: PendingSwitch = { target: 'native', at: 1 };
  it('is idle with no switch and no pending', () => {
    expect(toggleVisual(false, null)).toBe('idle');
    expect(toggleVisual(false, undefined)).toBe('idle');
  });
  it('is pending when armed + waiting', () => {
    expect(toggleVisual(false, pending)).toBe('pending');
  });
  it('is flipping while a switch is actually in flight', () => {
    expect(toggleVisual(true, null)).toBe('flipping');
  });
  it('flipping WINS over pending when both coexist', () => {
    expect(toggleVisual(true, pending)).toBe('flipping');
  });
});

describe('switchNotice reasons (resume-repair / fresh-start)', () => {
  it('a fresh-start (informational, interrupted:false) notice still keys and shows', () => {
    const freshStart = {
      interrupted: false,
      fromKind: 'native' as const,
      at: 1717000000123,
      reason: 'fresh-start' as const,
    };
    expect(switchNoticeKey(freshStart)).toBe(1717000000123);
    expect(shouldShowSwitchNotice(freshStart, new Set())).toBe(true);
    expect(shouldShowSwitchNotice(freshStart, new Set([freshStart.at]))).toBe(false);
  });

  it('a resume-repair notice behaves like an interrupted notice', () => {
    const repaired = {
      interrupted: true,
      fromKind: 'native' as const,
      at: 1717000000456,
      reason: 'resume-repair' as const,
    };
    expect(shouldShowSwitchNotice(repaired, new Set())).toBe(true);
  });
});
