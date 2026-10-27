import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import { describe, expect, it, vi } from 'vitest';
import type { PaneInfo } from '../../types';
import { AgentPanesSection, agentStartedPanes, likelyFinished } from './AgentPanesSection';

vi.mock('../../lib/dialog', () => ({ confirmDialog: vi.fn(async () => true) }));
vi.mock('../usePaneContextMenu', () => ({ confirmClosePane: vi.fn(async () => true) }));

const NOW = 1_800_000_000_000;

function pane(id: string, extra: Partial<PaneInfo>): PaneInfo {
  return {
    id, cwd: '/tmp', cmd: ['codex'], title: id, status: 'running', createdAt: '', cols: 80, rows: 24, pid: 1,
    lastActivityMs: NOW, state: 'idle', hasClaude: false, ...extra,
  } as PaneInfo;
}

const lead = pane('lead', { title: '#5 Claude', origin: { source: 'user', at: 1 } });
const working = pane('w1', { title: 'Engine deletion', state: 'busy', origin: { source: 'agent', byPaneId: 'lead', byPaneTitle: '#5 Claude', leadPaneId: 'lead', purpose: 'Delete the engine', at: 3 } });
const quiet = pane('w2', { title: 'Docs sweep', lastActivityMs: NOW - 20 * 60_000, origin: { source: 'agent', byPaneId: 'lead', byPaneTitle: '#5 Claude', leadPaneId: 'lead', at: 2 } });
const orphan = pane('w3', { title: 'Orphan', origin: { source: 'agent', byPaneId: 'gone', byPaneTitle: '#9 Codex', leadPaneId: 'gone', at: 1 } });
const waiting = pane('w4', { title: 'Needs approval', state: 'choice-prompt', attention: 'needs-user', lastActivityMs: 0, origin: { source: 'agent', byPaneId: 'gone', leadPaneId: 'gone', at: 4 } });
const legacy = pane('legacy', { title: 'Old pane' });
const panes: Record<string, PaneInfo> = Object.fromEntries([lead, working, quiet, orphan, waiting, legacy].map((p) => [p.id, p]));

describe('AgentPanesSection', () => {
  it('lists only panes started by agents, schedules or other programs, newest first', () => {
    expect(agentStartedPanes(panes).map((p) => p.id)).toEqual(['w4', 'w1', 'w2', 'w3']);
  });

  it('marks a worker finished only when it is not working or waiting on the person', () => {
    expect(likelyFinished(working, panes, NOW)).toBe(false);
    expect(likelyFinished(waiting, panes, NOW)).toBe(false);
    expect(likelyFinished(quiet, panes, NOW)).toBe(true); // quiet for 20 minutes
    expect(likelyFinished(orphan, panes, NOW)).toBe(true); // its lead is gone
    expect(likelyFinished(pane('fresh', { origin: { source: 'agent', leadPaneId: 'lead', at: 1 } }), panes, NOW)).toBe(false);
  });

  it('shows who started each pane and why, and jumps to it on click', () => {
    const onReveal = vi.fn();
    render(<AgentPanesSection panes={panes} collapsed={false} onToggle={() => {}} onRevealPane={onReveal} onKillPane={() => {}} now={NOW} />);
    expect(screen.getByText('Delete the engine')).toBeTruthy();
    // Workers are grouped under the lead they work for.
    expect(screen.getByText(/Lead #5 Claude · 2 workers/)).toBeTruthy();
    expect(screen.getByText(/Lead #9 Codex \(closed\) · 2 workers/)).toBeTruthy();
    expect(screen.getByText('working')).toBeTruthy();
    expect(screen.getByText('needs you')).toBeTruthy();
    expect(screen.queryByText('Old pane')).toBeNull();
    fireEvent.click(screen.getByTitle('Go to Engine deletion'));
    expect(onReveal).toHaveBeenCalledWith('w1');
  });

  it('closes only the finished workers after confirmation', async () => {
    const onKill = vi.fn();
    render(<AgentPanesSection panes={panes} collapsed={false} onToggle={() => {}} onRevealPane={() => {}} onKillPane={onKill} now={NOW} />);
    fireEvent.click(screen.getByLabelText('Started by agents actions'));
    fireEvent.click(screen.getByText('Close finished workers (2)…'));
    await waitFor(() => expect(onKill).toHaveBeenCalledTimes(2));
    expect(onKill.mock.calls.map((c) => c[0]).sort()).toEqual(['w2', 'w3']);
  });

  it('asks before closing a single pane from its row', async () => {
    const onKill = vi.fn();
    render(<AgentPanesSection panes={panes} collapsed={false} onToggle={() => {}} onRevealPane={() => {}} onKillPane={onKill} now={NOW} />);
    fireEvent.click(screen.getByLabelText('Close Engine deletion'));
    const { confirmClosePane } = await import('../usePaneContextMenu');
    expect(confirmClosePane).toHaveBeenCalled();
    await waitFor(() => expect(onKill).toHaveBeenCalledWith('w1'));
  });

  it('renders nothing when no pane was started by an agent', () => {
    const { container } = render(<AgentPanesSection panes={{ lead }} collapsed={false} onToggle={() => {}} onRevealPane={() => {}} onKillPane={() => {}} now={NOW} />);
    expect(container.innerHTML).toBe('');
  });
});
