import { describe, expect, it, vi } from 'vitest';
import { useStore } from './store';
import type { Layout, PaneInfo } from './types';
import { api } from './api';

const pane = {
  id: 'pane_a', cwd: '/r', cmd: ['bash'], title: 'agent', status: 'running',
  turn: { active: false, count: 3, last: { n: 3, startedMs: 1, endedMs: 2, files: 1, added: 1, removed: 0, repo: '/r' } },
} as PaneInfo;

function diffLeaf(): Extract<Layout, { type: 'diff' }> | undefined {
  const walk = (n: Layout | null): Extract<Layout, { type: 'diff' }> | undefined => {
    if (!n) return undefined;
    if (n.type === 'diff') return n;
    if (n.type === 'split') return walk(n.a) ?? walk(n.b);
    return undefined;
  };
  return walk(useStore.getState().layout);
}

describe('turn diff tile navigation', () => {
  it('opens on the latest turn, steps between turns, and reuses the tile', () => {
    vi.spyOn(api, 'saveWorkspace').mockResolvedValue(undefined as never);
    useStore.setState({ panes: { pane_a: pane }, layout: { type: 'pane', paneId: 'pane_a' }, focusedPaneId: 'pane_a' });
    useStore.getState().openTurnDiff('pane_a');
    const leaf = diffLeaf()!;
    expect(leaf.turn).toEqual({ paneId: 'pane_a', n: 3 });
    useStore.getState().setDiffTurn(leaf.diffId, { paneId: 'pane_a', n: 2 });
    expect(diffLeaf()!.turn?.n).toBe(2);
    useStore.getState().openTurnDiff('pane_a', 3);
    expect(diffLeaf()!.turn?.n).toBe(3);
    expect(diffLeaf()!.diffId).toBe(leaf.diffId);
    useStore.getState().setDiffTurn(leaf.diffId, null);
    expect(diffLeaf()!.turn).toBeUndefined();
  });
});
