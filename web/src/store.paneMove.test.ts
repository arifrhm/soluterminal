import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { api, ApiError, currentRoutingScope, setApiPrincipalAccess, setBackend } from './api';
import { useStore } from './store';
import { setCloudDualBackend } from './dualBackend';
import { collectPaneIds } from './lib/layout';
import type { PaneInfo, Workspace } from './types';

const pane = (id: string) => ({ id, pid: id === 'p' ? 1 : 2, status: 'running', cwd: '/repo', cmd: ['bash'], title: id, cols: 80, rows: 24 } as PaneInfo);
const workspace = (name: string, ids: string[]): Workspace => ({
  name, paneIds: ids, panes: ids.map(id => ({ id, cwd: '/repo', cmd: ['bash'], title: id, cols: 80, rows: 24 })),
  layout: ids.length === 0 ? null : ids.length === 1 ? { type: 'pane', paneId: ids[0] }
    : { type: 'split', direction: 'horizontal', ratio: 0.4, a: { type: 'pane', paneId: ids[0] }, b: { type: 'pane', paneId: ids[1] } },
  activeAt: '2026-10-24T00:00:00Z',
});
let source: Workspace;
let destination: Workspace;
const originalToast = useStore.getState().toast;
const originalOpenDialog = useStore.getState().openDialog;

beforeEach(() => {
  vi.useFakeTimers();
  localStorage.clear();
  setCloudDualBackend(false); setBackend(null); setApiPrincipalAccess(true);
  source = workspace('Source', ['p', 'keep']); destination = workspace('Destination', ['other']);
  useStore.setState({
    toast: originalToast, openDialog: originalOpenDialog, toasts: [],
    activeWorkspace: 'Source', activeWorkspaceMoveVersion: 0, activeBackend: null,
    layout: source.layout, panes: { p: pane('p'), keep: pane('keep'), other: pane('other') },
    workspaces: [source, destination], managedOwnerBindings: new Map(), mainPresentation: { kind: 'workspace' },
    focusedPaneId: 'p', lastUserFocusId: 'p', zoomedPaneId: 'p', navigationGeneration: 0,
    files: {}, browsers: {}, parkedPaneIds: [], updateStatus: null,
  });
  vi.spyOn(api, 'saveWorkspace').mockImplementation(async w => w);
  vi.spyOn(api, 'listPanes').mockResolvedValue([pane('p'), pane('keep'), pane('other')]);
  vi.spyOn(api, 'listWorkspaceSummaries').mockResolvedValue([source, destination]);
  vi.spyOn(api, 'listWorkspaces').mockImplementation(async () => useStore.getState().workspaces);
});
afterEach(async () => {
  // Flush any scheduled source save before replacing mocks in the next case.
  await vi.advanceTimersByTimeAsync(500);
  vi.useRealTimers(); vi.restoreAllMocks();
});

function moved() {
  return {
    source: { ...workspace('Source', ['keep']), paneMoveVersion: 1 },
    destination: { ...workspace('Destination', ['other', 'p']), paneMoveVersion: 1 },
  };
}

describe('pane workspace reassignment', () => {
  it('warns about a stale view and reloads only after the user chooses to replace its layout', async () => {
    vi.mocked(api.saveWorkspace).mockRejectedValue(new ApiError(409, 'workspace changed after a pane move'));
    useStore.getState().setLayout(source.layout);
    await vi.advanceTimersByTimeAsync(500);
    const warning = useStore.getState().toasts.find(toast => toast.message.includes('not being saved'));
    expect(warning?.action?.label).toBe('Reload layout');
    expect(warning?.ttlMs).toBe(0);
    expect(useStore.getState().layout).toEqual(source.layout);
    expect(useStore.getState().activeWorkspaceMoveVersion).toBe(0);
    const result = moved();
    vi.mocked(api.listWorkspaces).mockResolvedValue([result.source, result.destination]);
    const confirm = vi.spyOn(useStore.getState(), 'openDialog').mockImplementation(request => request.resolve(true));
    warning?.action?.run();
    await vi.advanceTimersByTimeAsync(1);
    expect(confirm).toHaveBeenCalledWith(expect.objectContaining({ title: 'Reload workspace layout?' }));
    expect(useStore.getState().activeWorkspaceMoveVersion).toBe(1);
    expect(collectPaneIds(useStore.getState().layout)).toEqual(['keep']);
    vi.mocked(api.saveWorkspace).mockImplementation(async value => value);
    useStore.getState().setLayout(result.source.layout);
    await vi.advanceTimersByTimeAsync(500);
    expect(api.saveWorkspace).toHaveBeenLastCalledWith(expect.objectContaining({ name: 'Source', paneMoveVersion: 1 }), currentRoutingScope());
  });

  it('keeps local layout edits if the user declines the conflict reload', async () => {
    vi.mocked(api.saveWorkspace).mockRejectedValue(new ApiError(409, 'workspace changed after a pane move'));
    useStore.getState().setLayout(source.layout);
    await vi.advanceTimersByTimeAsync(500);
    const warning = useStore.getState().toasts.find(toast => toast.message.includes('not being saved'));
    expect(warning?.action).toBeDefined();
    vi.spyOn(useStore.getState(), 'openDialog').mockImplementation(request => request.resolve(false));
    warning?.action?.run();
    await vi.advanceTimersByTimeAsync(1);
    expect(useStore.getState().layout).toEqual(source.layout);
    expect(useStore.getState().activeWorkspaceMoveVersion).toBe(0);
    expect(api.listWorkspaces).not.toHaveBeenCalled();
  });

  it('persists first, moves the same pane, and opens/focuses the destination', async () => {
    const move = vi.spyOn(api, 'movePaneToWorkspace').mockResolvedValue(moved());
    const create = vi.spyOn(api, 'createPane');
    const original = useStore.getState().panes.p;
    await useStore.getState().movePaneToWorkspace('p', 'Destination');
    expect(api.saveWorkspace).toHaveBeenCalledWith(expect.objectContaining({ name: 'Source', paneMoveVersion: 0 }), currentRoutingScope());
    expect(move).toHaveBeenCalledWith('Source', 'p', 'Destination', 0, currentRoutingScope());
    const state = useStore.getState();
    expect(state.activeWorkspace).toBe('Destination');
    expect(state.focusedPaneId).toBe('p');
    expect(state.activeWorkspaceMoveVersion).toBe(1);
    expect(collectPaneIds(state.layout)).toEqual(['other', 'p']);
    expect(state.workspaces.find(w => w.name === 'Source')?.paneIds).toEqual(['keep']);
    expect(state.panes.p.pid).toBe(original.pid);
    expect(create).not.toHaveBeenCalled();
  });

  it('moves the last pane without reviving it through the outgoing autosave', async () => {
    source = workspace('Source', ['p']);
    useStore.setState({ layout: source.layout, workspaces: [source, destination] });
    vi.spyOn(api, 'movePaneToWorkspace').mockResolvedValue({ ...moved(), source: { ...workspace('Source', []), paneMoveVersion: 1 } });
    await useStore.getState().movePaneToWorkspace('p', 'Destination');
    expect(useStore.getState().activeWorkspace).toBe('Destination');
    expect(useStore.getState().workspaces.find(w => w.name === 'Source')?.layout).toBeNull();
    await vi.advanceTimersByTimeAsync(500);
    const save = vi.mocked(api.saveWorkspace);
    const moveOrder = vi.mocked(api.movePaneToWorkspace).mock.invocationCallOrder[0];
    save.mock.calls.forEach(([w], index) => {
      if (w.name === 'Source') expect(save.mock.invocationCallOrder[index]).toBeLessThan(moveOrder);
    });
  });

  it('leaves the current layout and focus unchanged on a failed move', async () => {
    vi.spyOn(api, 'movePaneToWorkspace').mockRejectedValue(new Error('storage unavailable'));
    const toast = vi.spyOn(useStore.getState(), 'toast');
    await useStore.getState().movePaneToWorkspace('p', 'Destination');
    expect(useStore.getState().layout).toEqual(source.layout);
    expect(useStore.getState().focusedPaneId).toBe('p');
    expect(useStore.getState().activeWorkspace).toBe('Source');
    expect(toast).toHaveBeenCalledWith(expect.stringContaining("Couldn't move pane"), expect.objectContaining({ kind: 'error' }));
  });

  it('does not move when saving the source fails', async () => {
    vi.mocked(api.saveWorkspace).mockRejectedValue(new Error('storage unavailable'));
    const move = vi.spyOn(api, 'movePaneToWorkspace');
    await useStore.getState().movePaneToWorkspace('p', 'Destination');
    expect(move).not.toHaveBeenCalled();
    expect(useStore.getState().layout).toEqual(source.layout);
  });

  it('does not change another view after navigation during the request', async () => {
    vi.spyOn(api, 'movePaneToWorkspace').mockImplementation(async () => {
      useStore.setState({ activeWorkspace: 'Elsewhere', navigationGeneration: 1, layout: { type: 'pane', paneId: 'other' } });
      return moved();
    });
    await useStore.getState().movePaneToWorkspace('p', 'Destination');
    expect(useStore.getState().activeWorkspace).toBe('Elsewhere');
    expect(collectPaneIds(useStore.getState().layout)).toEqual(['other']);
  });

  it.each(['owner', 'different backend', 'missing destination'])('rejects %s before writing', async (kind) => {
    if (kind === 'owner') useStore.setState({ managedOwnerBindings: new Map([['p', 'task']]) });
    if (kind === 'different backend') useStore.setState({ activeBackend: 'wsl', workspaces: [source, { ...destination, backend: 'win' }] });
    const move = vi.spyOn(api, 'movePaneToWorkspace');
    await useStore.getState().movePaneToWorkspace('p', kind === 'missing destination' ? 'Missing' : 'Destination');
    expect(move).not.toHaveBeenCalled();
    expect(api.saveWorkspace).not.toHaveBeenCalled();
  });
});
