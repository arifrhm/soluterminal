import { act, cleanup, render } from '@testing-library/react';
import { afterEach, beforeEach, expect, it, vi } from 'vitest';
import { currentRoutingScope } from '../api';
import { acquirePaneHost } from '../lib/paneHosts';
import { paneIdsInOrder } from '../lib/layout';
import {
  clearTerminalSessions,
  ensureTerminalSession,
  sessionKey,
} from '../lib/terminalViewSessions';
import { useStore } from '../store';
import type { Layout, PaneInfo } from '../types';
import { WarmPaneLayer } from './WarmPaneLayer';

vi.mock('./DelegatedWorkChip', () => ({ DelegatedWorkChip: () => null }));

// Exercise the persistent renderer and real badge without starting xterm/PTYs.
vi.mock('./Pane', async () => {
  const { PaneHeaderBadges } = await import('./PaneHeaderBadges');
  return {
    Pane: ({ pane, paneIndex }: { pane: PaneInfo; paneIndex: number }) => (
      <PaneHeaderBadges pane={pane} paneIndex={paneIndex} />
    ),
  };
});

const pane = (id: string): PaneInfo => ({
  id,
  cwd: '/project',
  cmd: ['/bin/bash'],
  title: id,
  status: 'running',
  state: 'idle',
  createdAt: '2026-10-15',
  cols: 80,
  rows: 24,
  pid: 1,
  lastActivityMs: 0,
  hasClaude: false,
});

const left = pane('left');
const right = pane('right');
const layout: Layout = {
  type: 'split',
  direction: 'horizontal',
  ratio: 0.5,
  a: { type: 'pane', paneId: left.id },
  b: { type: 'pane', paneId: right.id },
};

function badge(id: string) {
  const key = sessionKey(currentRoutingScope(), id);
  return acquirePaneHost(key).querySelector('.pane-index-badge');
}

function expectShortcutNumbers() {
  for (const [index, id] of paneIdsInOrder(
    useStore.getState().layout,
  ).entries()) {
    expect(badge(id)?.textContent).toBe(`#${index + 1}`);
    expect(badge(id)?.getAttribute('title')).toBe(
      `Alt+${index + 1} focuses this pane`,
    );
  }
}

beforeEach(() => {
  clearTerminalSessions();
  vi.stubGlobal('innerWidth', 1200);
  useStore.setState({
    mainPresentation: { kind: 'workspace' },
    ownerReturnContext: null,
    panes: { left, right },
    layout,
    focusedPaneId: left.id,
    zoomedPaneId: null,
    parkedPaneIds: [],
  });
});

afterEach(() => {
  cleanup();
  clearTerminalSessions();
  vi.unstubAllGlobals();
});

it('labels the first and second panes #1 and #2, matching their shortcuts', () => {
  render(<WarmPaneLayer />);
  expectShortcutNumbers();
});

it('updates numbers when panes move and ignores nonterminal tiles', () => {
  render(<WarmPaneLayer />);
  act(() =>
    useStore.setState({
      layout: {
        ...layout,
        a: { type: 'browser', browserId: 'browser' },
        b: { ...layout, a: layout.b, b: layout.a },
      },
    }),
  );
  expect(badge(right.id)?.textContent).toBe('#1');
  expect(badge(left.id)?.textContent).toBe('#2');
  expectShortcutNumbers();
});

it('keeps workspace numbers while zoomed and hides numbers for retained outsiders', () => {
  ensureTerminalSession(currentRoutingScope(), pane('outside'), true);
  useStore.setState({ zoomedPaneId: right.id });
  render(<WarmPaneLayer />);
  expect(badge(right.id)?.textContent).toBe('#2');
  expect(badge('outside')).toBeNull();
});
