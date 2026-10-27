import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import type { PaneInfo } from '../types';
import { useStore } from '../store';
import { PaneIdentityChip } from './PaneIdentityChip';
import { handleProblem, normalizeHandle, workingOnProvenance } from './PaneIdentityDialog';
import { originLine } from './sidebar/AgentPanesSection';

function pane(extra: Partial<PaneInfo>): PaneInfo {
  return { id: 'p1', cwd: '/tmp', cmd: ['codex'], title: 'Login fix', status: 'running', createdAt: '', cols: 80, rows: 24, pid: 1, lastActivityMs: 0, state: 'idle', hasClaude: false, ...extra } as PaneInfo;
}

const setPaneIdentity = vi.fn(async (_id: string, _change: { handle?: string; workingOn?: string }) => {});
const renamePane = vi.fn(async () => null);

describe('pane handle and working-on line', () => {
  beforeEach(() => {
    setPaneIdentity.mockReset();
    setPaneIdentity.mockImplementation(async () => {});
    renamePane.mockClear();
    useStore.setState({ setPaneIdentity, renamePane } as never);
  });

  it('checks handles the way the daemon does', () => {
    expect(normalizeHandle(' @API-Fix ')).toBe('api-fix');
    expect(handleProblem('')).toBeNull();
    expect(handleProblem('api-fix')).toBeNull();
    for (const bad of ['3', 'api fix', 'api_fix', '-x', 'a'.repeat(25)]) expect(handleProblem(bad)).not.toBeNull();
  });

  it('shows nothing until a handle or working-on line is set', () => {
    const { container } = render(<PaneIdentityChip pane={pane({})} />);
    expect(container.innerHTML).toBe('');
  });

  it('shows the handle and working-on line, with who set it', () => {
    const at = Date.now() - 5 * 60_000;
    const p = pane({ identity: { handle: 'api-fix', workingOn: 'Fixing the login redirect', workingOnAt: at, workingOnBy: '#2 Claude' } });
    render(<PaneIdentityChip pane={p} />);
    expect(screen.getByText('@api-fix')).toBeTruthy();
    expect(screen.getByText('Fixing the login redirect')).toBeTruthy();
    expect(workingOnProvenance(p)).toMatch(/^set by #2 Claude, 5m ago$/);
    expect(workingOnProvenance(pane({ identity: { workingOn: 'x', workingOnBy: 'user' } }))).toBe('set by you');
  });

  it('saves only what changed and keeps the dialog open on a taken handle', async () => {
    setPaneIdentity.mockRejectedValueOnce(new Error('that handle is taken by #3 Codex; choose another'));
    render(<PaneIdentityChip pane={pane({ identity: { handle: 'old', workingOn: 'Same line' } })} />);
    fireEvent.click(screen.getByText('@old'));
    fireEvent.change(screen.getByLabelText('Handle'), { target: { value: '@Lead' } });
    fireEvent.click(screen.getByText('Save'));
    expect(await screen.findByRole('alert')).toHaveProperty('textContent', 'that handle is taken by #3 Codex; choose another');
    expect(setPaneIdentity).toHaveBeenCalledWith('p1', { handle: 'lead' });
    expect(renamePane).not.toHaveBeenCalled();

    fireEvent.change(screen.getByLabelText('Handle'), { target: { value: 'lead-2' } });
    fireEvent.change(screen.getByLabelText('Name'), { target: { value: 'Login lead' } });
    fireEvent.click(screen.getByText('Save'));
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull());
    expect(setPaneIdentity).toHaveBeenLastCalledWith('p1', { handle: 'lead-2' });
    expect(renamePane).toHaveBeenCalledWith('p1', 'Login lead');
  });

  it('refuses an invalid handle before asking the daemon', () => {
    render(<PaneIdentityChip pane={pane({ identity: { handle: 'old' } })} />);
    fireEvent.click(screen.getByText('@old'));
    fireEvent.change(screen.getByLabelText('Handle'), { target: { value: 'api fix' } });
    expect((screen.getByText('Save') as HTMLButtonElement).disabled).toBe(true);
    expect(screen.getByText(/lowercase letters, digits or dashes/)).toBeTruthy();
  });

  it('lists a worker by what it is working on now, else what it was started for', () => {
    const origin = { source: 'agent' as const, byPaneId: 'lead', leadPaneId: 'lead', purpose: 'Delete the engine', at: 1 };
    expect(originLine(pane({ origin }), {}, [])).toBe('Delete the engine');
    expect(originLine(pane({ origin, identity: { workingOn: 'Running the full suite' } }), {}, [])).toBe('Running the full suite');
  });
});
