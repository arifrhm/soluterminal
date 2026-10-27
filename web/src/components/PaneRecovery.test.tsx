import { render, screen, fireEvent } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { useStore } from '../store';
import { failedPaneFromDef, optimisticPaneFromDef } from '../lib/workspace';
import type { Layout, PaneInfo } from '../types';
import { PaneRecovery, recoveryKind, suggestedFolder } from './PaneRecovery';

const lost = (error: string, cwd = '/tmp/deleted') =>
  failedPaneFromDef({ id: 'lost', cwd, cmd: ['bash'], title: 'Keep me', agent: 'codex', codexThreadId: 'original' }, new Error(error));
const live = (id: string, cwd: string, title = id): PaneInfo => ({ ...optimisticPaneFromDef({ id, cwd, cmd: ['bash'], title }), status: 'running' });
const split = (...ids: string[]): Layout => ids.slice(1).reduce<Layout>(
  (a, id) => ({ type: 'split', direction: 'horizontal', ratio: 0.5, a, b: { type: 'pane', paneId: id } }),
  { type: 'pane', paneId: ids[0] },
);

let prior: ReturnType<typeof useStore.getState>;
beforeEach(() => { prior = useStore.getState(); });
afterEach(() => {
  useStore.setState({ resumePane: prior.resumePane, closePaneInLayout: prior.closePaneInLayout, revealPane: prior.revealPane, panes: prior.panes, layout: prior.layout });
  vi.restoreAllMocks();
});

describe('recoveryKind', () => {
  it('classifies each restore failure the daemon reports', () => {
    expect(recoveryKind('working directory does not exist: /tmp/x')).toEqual({ kind: 'folder' });
    expect(recoveryKind('working directory is not a directory: /tmp/x')).toEqual({ kind: 'folder' });
    expect(recoveryKind('conversation is already running in pane pane_abc')).toEqual({ kind: 'duplicate', ownerId: 'pane_abc' });
    expect(recoveryKind('required Claude transcript is no longer resumable')).toEqual({ kind: 'gone' });
    expect(recoveryKind('something else')).toEqual({ kind: 'other' });
  });
});

describe('suggestedFolder', () => {
  it('prefers a folder named in the lost path over the most used one', () => {
    const panes = { a: live('a', '/home/me/notes'), b: live('b', '/home/me/notes'), c: live('c', '/home/me/wsl-projects/shinoque') };
    const layout = split('a', 'b', 'c');
    expect(suggestedFolder({ panes, layout }, 'lost', '/tmp/shinoque-app-platform-20261009/kit')).toBe('/home/me/wsl-projects/shinoque');
    expect(suggestedFolder({ panes, layout }, 'lost', '/tmp/unrelated-copy')).toBe('/home/me/notes');
    expect(suggestedFolder({ panes: {}, layout: null }, 'lost', '/tmp/x')).toBe('~');
  });
});

describe('pane recovery tile', () => {
  it('suggests the folder the workspace works in when the old folder is gone', () => {
    const pane = lost('working directory does not exist: /tmp/deleted');
    const retry = vi.fn().mockResolvedValue(pane);
    useStore.setState({
      resumePane: retry,
      panes: { lost: pane, a: live('a', '/work/repo'), b: live('b', '/work/repo'), c: live('c', '/home/me') },
      layout: split('lost', 'a', 'b', 'c'),
    });
    render(<PaneRecovery pane={pane} />);
    expect(screen.getByText("This pane's folder is gone")).toBeInTheDocument();
    expect(screen.getByText('/tmp/deleted')).toBeInTheDocument();
    expect(screen.getByLabelText('Reopen in folder')).toHaveValue('/work/repo');
    fireEvent.click(screen.getByRole('button', { name: 'Reopen here' }));
    expect(retry).toHaveBeenCalledWith('lost', pane.cols, pane.rows, '/work/repo');
  });

  it('lets the user pick a different folder, falling back to home when nothing else is open', () => {
    const pane = lost('working directory does not exist: /tmp/deleted');
    const retry = vi.fn().mockResolvedValue(pane);
    useStore.setState({ resumePane: retry, panes: { lost: pane }, layout: split('lost') });
    render(<PaneRecovery pane={pane} />);
    expect(screen.getByLabelText('Reopen in folder')).toHaveValue('~');
    fireEvent.change(screen.getByLabelText('Reopen in folder'), { target: { value: '/work/existing' } });
    fireEvent.click(screen.getByRole('button', { name: 'Reopen here' }));
    expect(retry).toHaveBeenCalledWith('lost', pane.cols, pane.rows, '/work/existing');
  });

  it('sends a duplicate to the pane that already has the conversation', () => {
    const pane = lost('conversation is already running in pane owner', '/work/repo');
    const reveal = vi.fn().mockResolvedValue(undefined);
    const close = vi.fn();
    useStore.setState({ revealPane: reveal, closePaneInLayout: close, panes: { lost: pane, owner: live('owner', '/work/repo', 'prtriage') }, layout: split('lost', 'owner') });
    render(<PaneRecovery pane={pane} />);
    expect(screen.getByText('prtriage')).toBeInTheDocument();
    expect(screen.queryByLabelText('Working directory')).toBeNull();
    fireEvent.click(screen.getByRole('button', { name: 'Go to that pane' }));
    expect(reveal).toHaveBeenCalledWith('owner');
    fireEvent.click(screen.getByRole('button', { name: 'Close this copy' }));
    expect(close).toHaveBeenCalledWith('lost');
  });

  it('offers to reopen a duplicate once the pane that had it has closed', () => {
    const pane = lost('conversation is already running in pane owner', '/work/repo');
    const retry = vi.fn().mockResolvedValue(pane);
    useStore.setState({ resumePane: retry, panes: { lost: pane, owner: { ...live('owner', '/work/repo', 'prtriage'), status: 'exited' } }, layout: split('lost', 'owner') });
    render(<PaneRecovery pane={pane} />);
    expect(screen.queryByRole('button', { name: 'Go to that pane' })).toBeNull();
    fireEvent.click(screen.getByRole('button', { name: 'Reopen here' }));
    expect(retry).toHaveBeenCalledWith('lost', pane.cols, pane.rows, '/work/repo');
  });

  it('treats an owner this window cannot see as still open', () => {
    const pane = lost('conversation is already running in pane elsewhere', '/work/repo');
    useStore.setState({ panes: { lost: pane }, layout: split('lost') });
    render(<PaneRecovery pane={pane} />);
    expect(screen.getByText("Another pane has it open, so this copy can't open too.")).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Reopen here' })).toBeNull();
    expect(screen.getByRole('button', { name: 'Close this copy' })).toHaveClass('primary');
    expect(screen.getByRole('button', { name: 'Try again' })).toBeInTheDocument();
  });

  it('offers closing first when the conversation history is gone', () => {
    const pane = lost('required Claude transcript is no longer resumable', '/work/repo');
    const close = vi.fn();
    useStore.setState({ closePaneInLayout: close, panes: { lost: pane }, layout: split('lost') });
    render(<PaneRecovery pane={pane} />);
    expect(screen.getByRole('alert')).toHaveTextContent('no longer resumable');
    fireEvent.click(screen.getByRole('button', { name: 'Close pane' }));
    expect(close).toHaveBeenCalledWith('lost');
  });

  it('keeps the original folder for other failures', () => {
    const pane = lost('spawn failed', '/work/repo');
    const retry = vi.fn().mockResolvedValue(pane);
    useStore.setState({ resumePane: retry, panes: { lost: pane }, layout: split('lost') });
    render(<PaneRecovery pane={pane} />);
    expect(screen.getByLabelText('Working directory')).toHaveValue('/work/repo');
    fireEvent.click(screen.getByRole('button', { name: 'Retry restore' }));
    expect(retry).toHaveBeenCalledWith('lost', pane.cols, pane.rows, '/work/repo');
  });

  it('switches to the suggested folder when a retry reveals the folder is gone', () => {
    const first = lost('The previous shell ended during shutdown.', '/tmp/deleted');
    useStore.setState({ panes: { lost: first, a: live('a', '/work/repo') }, layout: split('lost', 'a') });
    const { rerender } = render(<PaneRecovery pane={first} />);
    expect(screen.getByLabelText('Working directory')).toHaveValue('/tmp/deleted');
    rerender(<PaneRecovery pane={{ ...first, recoveryError: 'working directory does not exist: /tmp/deleted' }} />);
    expect(screen.getByLabelText('Reopen in folder')).toHaveValue('/work/repo');
  });

  it('closes every pane needing recovery in one click', () => {
    const pane = lost('working directory does not exist: /tmp/deleted');
    const other = { ...lost('required Claude transcript is no longer resumable'), id: 'gone' };
    const close = vi.fn();
    useStore.setState({ closePaneInLayout: close, panes: { lost: pane, gone: other, a: live('a', '/work/repo') }, layout: split('lost', 'gone', 'a') });
    render(<PaneRecovery pane={pane} />);
    fireEvent.click(screen.getByRole('button', { name: 'Close all 2 panes that need recovery' }));
    expect(close).not.toHaveBeenCalled();
    fireEvent.click(screen.getByRole('button', { name: 'Close 2 panes' }));
    expect(close.mock.calls.map((c) => c[0])).toEqual(['lost', 'gone']);
  });
});
