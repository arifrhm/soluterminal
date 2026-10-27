import { afterEach, expect, it } from 'vitest';
import { currentRoutingScope } from '../api';
import type { PaneInfo } from '../types';
import { clearTerminalSessions, ensureTerminalSession, holdTerminalPlacement, reconcileTerminalSessions, terminalSession } from './terminalViewSessions';

afterEach(clearTerminalSessions);

it('keeps the same terminal core through a pane move blank phase, then releases ordinary caching', () => {
  const scope = currentRoutingScope();
  const pane = { id: 'moving', pid: 1, status: 'running', cmd: ['bash'] } as PaneInfo;
  const core = ensureTerminalSession(scope, pane);
  const release = holdTerminalPlacement(scope, pane.id);
  reconcileTerminalSessions(scope, new Set(), new Set());
  expect(terminalSession(core.key)).toBe(core);
  expect(core.invalid).toBe(false);
  release();
  // Visible again in its destination; the core and scrollback survive.
  reconcileTerminalSessions(scope, new Set([pane.id]), new Set());
  expect(terminalSession(core.key)).toBe(core);
  // Later ordinary navigation can evict it as usual (warm cache disabled).
  reconcileTerminalSessions(scope, new Set(), new Set());
  expect(terminalSession(core.key)).toBeUndefined();
});

it('retains overlapping placement leases until the final release', () => {
  const scope = currentRoutingScope();
  const core = ensureTerminalSession(scope, { id: 'moving', pid: 1 } as PaneInfo);
  const first = holdTerminalPlacement(scope, 'moving');
  const second = holdTerminalPlacement(scope, 'moving');
  first(); first();
  reconcileTerminalSessions(scope, new Set(), new Set());
  expect(terminalSession(core.key)).toBe(core);
  second();
  reconcileTerminalSessions(scope, new Set(), new Set());
  expect(terminalSession(core.key)).toBeUndefined();
});
