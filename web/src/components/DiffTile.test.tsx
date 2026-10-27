import { act, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { DiffTile } from './DiffTile';
import { api } from '../api';
import { useStore } from '../store';
import type { Layout, PaneInfo, TurnRecord } from '../types';

type Leaf = Extract<Layout, { type: 'diff' }>;
const record = (n: number): TurnRecord => ({ n, startedMs: 100, endedMs: 200, files: 1, added: 1, removed: 0 });
const response = (n: number) => ({ repo: '/r', branch: `Turn ${n}`, staged: false, files: [{ path: `selected-${n}.txt`, status: 'M', added: 1, removed: 0, secret: false }], diff: `+selected-${n}`, truncated: false, hiddenSecret: 0, turn: n, startedMs: 100, endedMs: 200 });
function deferred<T>() { let resolve!: (value: T) => void; let reject!: (error: Error) => void; const promise = new Promise<T>((a, b) => { resolve = a; reject = b; }); return { promise, resolve, reject }; }
function Fixture() { const leaf = useStore((s) => s.layout) as Leaf; return <DiffTile leaf={leaf} />; }
function select(n: number | null) { act(() => useStore.getState().setDiffTurn('diff-test', n === null ? null : { paneId: 'p', n })); }
beforeEach(() => {
  vi.spyOn(api, 'saveWorkspace').mockResolvedValue(undefined as never);
  vi.spyOn(api, 'paneTurns').mockResolvedValue({ turns: [record(8), record(3), record(11)] });
  vi.spyOn(api, 'paneTurnDiff').mockImplementation(async (_, n) => response(n));
  vi.spyOn(api, 'gitDiff').mockResolvedValue({ ...response(0), branch: 'main', staged: false });
  useStore.setState({ activeWorkspace: '', focusedPaneId: 'diff-test', panes: { p: { id: 'p', title: 'agent', cmd: ['codex'], cwd: '/r', status: 'running' } as PaneInfo }, layout: { type: 'diff', diffId: 'diff-test', path: '/r', staged: false, turn: { paneId: 'p', n: 8 } } });
});
afterEach(() => vi.restoreAllMocks());
describe('retained turn diff requests', () => {
  it('navigates actual retained chronology [8,3,11] and its edges', async () => {
    render(<Fixture />);
    await screen.findByText('selected-8.txt');
    expect(screen.getByText('Turn 8 · 1 of 3 retained')).toBeTruthy();
    expect((screen.getByText('Next ›') as HTMLButtonElement).disabled).toBe(true);
    fireEvent.click(screen.getByText('‹ Prev'));
    await screen.findByText('selected-3.txt');
    fireEvent.click(screen.getByText('‹ Prev'));
    await screen.findByText('selected-11.txt');
    expect((screen.getByText('‹ Prev') as HTMLButtonElement).disabled).toBe(true);
    fireEvent.click(screen.getByText('Next ›'));
    await screen.findByText('selected-3.txt');
  });
  it.each(['success', 'error'] as const)('ignores old %s and finally while newer selection is loading', async (outcome) => {
    const old = deferred<ReturnType<typeof response>>(), current = deferred<ReturnType<typeof response>>();
    vi.mocked(api.paneTurnDiff).mockImplementation((_, n) => n === 8 ? old.promise : current.promise);
    render(<Fixture />); select(3);
    await act(async () => outcome === 'success' ? old.resolve(response(8)) : old.reject(new Error('obsolete error')));
    expect(screen.queryByText('selected-8.txt')).toBeNull();
    expect(screen.queryByText(/obsolete error/)).toBeNull();
    expect(screen.getByText('loading diff…')).toBeTruthy();
    await act(async () => current.resolve(response(3)));
    expect(screen.getByText('selected-3.txt')).toBeTruthy();
  });
  it('guards Retry and working/staged return from late turn responses', async () => {
    const old = deferred<ReturnType<typeof response>>();
    vi.mocked(api.paneTurnDiff).mockReturnValueOnce(old.promise).mockResolvedValue(response(8));
    render(<Fixture />);
    fireEvent.click(screen.getByTitle('Reload diff'));
    await screen.findByText('selected-8.txt');
    fireEvent.click(screen.getByText('All uncommitted'));
    await screen.findByText('selected-0.txt');
    fireEvent.click(screen.getByText('Staged'));
    await waitFor(() => expect(api.gitDiff).toHaveBeenLastCalledWith('/r', true));
    await act(async () => old.reject(new Error('stale turn failure')));
    expect(screen.queryByText(/stale turn failure/)).toBeNull();
    expect(screen.getByText('selected-0.txt')).toBeTruthy();
  });
  it('keeps a missing retained selection explicit and offers Retry and all uncommitted', async () => {
    vi.mocked(api.paneTurnDiff).mockRejectedValue(new Error('404 no such turn'));
    render(<Fixture />); select(77);
    await screen.findByText(/404 no such turn/);
    expect(screen.getByText('Turn 77 · not retained')).toBeTruthy();
    expect(screen.getByText('Retry')).toBeTruthy();
    expect(screen.getByText('All uncommitted')).toBeTruthy();
  });
  it('does not publish an older history response after a new selection', async () => {
    const old = deferred<{ turns: TurnRecord[] }>();
    vi.mocked(api.paneTurns).mockReturnValueOnce(old.promise).mockResolvedValue({ turns: [record(3), record(11)] });
    render(<Fixture />); select(3);
    await screen.findByText('Turn 3 · 1 of 2 retained');
    await act(async () => old.resolve({ turns: [record(8)] }));
    expect(screen.getByText('Turn 3 · 1 of 2 retained')).toBeTruthy();
  });
  it('refreshes an older selected snapshot when only historyRevision changes', async () => {
    vi.mocked(api.paneTurnDiff).mockRejectedValueOnce(new Error('Diff capture pending')).mockResolvedValue(response(3));
    select(3);
    useStore.setState((s) => ({ panes: { ...s.panes, p: { ...s.panes.p, turn: { active: false, count: 8, historyRevision: 1, last: record(8) } } } }));
    render(<Fixture />);
    await screen.findByText(/Diff capture pending/);
    act(() => useStore.setState((s) => ({ panes: { ...s.panes, p: { ...s.panes.p, turn: { ...s.panes.p.turn!, historyRevision: 2 } } } })));
    await screen.findByText('selected-3.txt');
    expect(api.paneTurnDiff).toHaveBeenLastCalledWith('p', 3);
  });

});
