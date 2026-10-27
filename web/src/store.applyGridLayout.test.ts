import { afterEach, beforeEach, expect, it, vi } from 'vitest';
import { api } from './api';
import type { PaneInfo } from './types';
import { useStore } from './store';

beforeEach(() => vi.useFakeTimers());
afterEach(() => { vi.restoreAllMocks(); vi.clearAllTimers(); vi.useRealTimers(); });

function seedOnePane() {
  const toast = vi.fn();
  useStore.setState({
    panes: { a: { id: 'a', title: 'shell', cwd: '/home', status: 'running' } as PaneInfo },
    layout: { type: 'pane', paneId: 'a' },
    focusedPaneId: null,
    // No active workspace: the post-spawn autosave flush is a no-op, so the
    // test needs no workspace API.
    activeWorkspace: '',
    toast,
  });
  return toast;
}

// A failed spawn used to only console.error: the grid picker stopped spinning
// with no new panes and no explanation.
it('toasts when the grid picker cannot open any new pane', async () => {
  const toast = seedOnePane();
  vi.spyOn(api, 'createPane').mockRejectedValue(new Error('500 Internal Server Error: connect agent: dial'));

  await useStore.getState().applyGridLayout(1, 2);

  expect(toast).toHaveBeenCalledWith(
    "Couldn't open new panes: 500 Internal Server Error: connect agent: dial",
    expect.objectContaining({ kind: 'error' }),
  );
});

it('reports a partial fill and keeps the panes that did open', async () => {
  const toast = seedOnePane();
  vi.spyOn(api, 'createPane')
    .mockResolvedValueOnce({ id: 'b', title: 'shell', cwd: '/home', status: 'running' } as PaneInfo)
    .mockRejectedValue(new Error('boom'));

  await useStore.getState().applyGridLayout(2, 2);

  expect(toast).toHaveBeenCalledWith(
    "Opened 1 of 3 panes — couldn't open the rest: boom",
    expect.objectContaining({ kind: 'error' }),
  );
  expect(useStore.getState().panes.b).toBeDefined();
});
