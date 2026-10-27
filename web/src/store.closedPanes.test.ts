import { describe, expect, it, vi } from 'vitest';
import { api } from './api';
import { useStore } from './store';
import type { PaneInfo } from './types';

describe('reopen closed pane goes through the daemon', () => {
  it('asks the daemon to rebuild the pane and inserts it next to its old neighbour', async () => {
    const left = { id: 'pane_left', cwd: '/r', cmd: ['bash'], title: 'left', status: 'running', cols: 90, rows: 30 } as PaneInfo;
    useStore.setState({ panes: { pane_left: left }, layout: { type: 'pane', paneId: 'pane_left' }, focusedPaneId: 'pane_left' });
    vi.spyOn(api, 'closedPanes')
      .mockResolvedValueOnce({ panes: [{ id: 'pane_gone', title: 'agent', cwd: '/r', kind: 'claude', closedAtMs: 1 }] })
      .mockResolvedValue({ panes: [] }); // the daemon consumed it
    const reopened = { id: 'pane_new', cwd: '/r', cmd: ['claude'], title: 'agent', status: 'running' } as PaneInfo;
    const reopen = vi.spyOn(api, 'reopenClosedPane').mockResolvedValue(reopened);
    vi.spyOn(api, 'saveWorkspace').mockResolvedValue(undefined as never);

    await useStore.getState().reopenClosedPane();

    // The client sends only the id and live dims — never a command recipe.
    expect(reopen).toHaveBeenCalledWith('pane_gone', 90, 30);
    const st = useStore.getState();
    expect(st.panes.pane_new).toBeDefined();
    expect(st.focusedPaneId).toBe('pane_new');
    expect(st.recentlyClosedPanes).toEqual([]);
  });
});
