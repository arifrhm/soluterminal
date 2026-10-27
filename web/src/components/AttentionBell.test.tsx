import { fireEvent, render, screen } from '@testing-library/react';
import { beforeEach, describe, expect, it } from 'vitest';
import { AttentionBell } from './AttentionBell';
import { useStore } from '../store';
import type { PaneInfo } from '../types';

// The bell reads the real store through useAttentionBadge/useAttentionItems —
// seed panes directly and assert the badge count + the opened list rows
// (including the LLM-graded summary line) render from the same data.

function needsUserPane(id: string, over: Partial<PaneInfo> = {}): PaneInfo {
  return {
    id,
    title: `pane ${id}`,
    cmd: ['claude'],
    cwd: '/tmp',
    status: 'running',
    state: 'choice-prompt',
    attention: 'needs-user',
    lastActivityMs: Date.now(),
    ...over,
  } as PaneInfo;
}

describe('AttentionBell', () => {
  beforeEach(() => {
    useStore.setState({
      panes: {},
      workspaces: [],
      paneTails: {},
      ariaDismissedFingerprints: {},
    });
  });

  it('shows no badge when nothing needs the user', () => {
    render(<AttentionBell />);
    const btn = screen.getByRole('button', { name: /no panes are waiting/i });
    expect(btn.querySelector('.attention-bell-count')).toBeNull();
  });

  it('counts needs-user panes and lists them with the graded summary on open', () => {
    useStore.setState({
      panes: {
        p1: needsUserPane('p1', {
          attentionSummary: 'wants to run a prod DB migration',
          attentionRisk: 'high',
        }),
        p2: needsUserPane('p2', { state: 'awaiting-input' }),
        // auto-pending panes are auto-approval's problem, never the bell's.
        p3: needsUserPane('p3', { attention: 'auto-pending' }),
      },
    });
    render(<AttentionBell />);
    const btn = screen.getByRole('button', { name: /2 panes are waiting/i });
    expect(btn.querySelector('.attention-bell-count')?.textContent).toBe('2');

    fireEvent.click(btn);
    expect(screen.getByText('Waiting on you')).toBeTruthy();
    expect(screen.getByText('pane p1')).toBeTruthy();
    expect(screen.getByText('pane p2')).toBeTruthy();
    expect(screen.queryByText('pane p3')).toBeNull();
    expect(screen.getByText('wants to run a prod DB migration')).toBeTruthy();
    expect(screen.getByText('high')).toBeTruthy();
  });

  it("lists a message stuck in an idle pane's input box until it is sent or cleared", () => {
    useStore.setState({
      panes: {
        w1: needsUserPane('w1', { state: 'idle', attention: 'none', unsentMessage: { id: 'm', sender: '#1 Claude' } }),
        // A pane already listed for a prompt is not listed twice.
        w2: needsUserPane('w2', { unsentMessage: { id: 'n', sender: '#1 Claude' } }),
      },
    });
    render(<AttentionBell />);
    const btn = screen.getByRole('button', { name: /2 panes are waiting/i });
    fireEvent.click(btn);
    expect(screen.getByText('Message from #1 Claude not sent — Send or Clear it on the pane')).toBeTruthy();
    expect(screen.getAllByText(/pane w/).length).toBe(2);
  });

  it('still lists a stuck message when its pane\'s prompt row is dismissed', () => {
    useStore.setState({
      panes: { w3: needsUserPane('w3', { state: 'awaiting-input', unsentMessage: { id: 'm', sender: '#2 Codex' } }) },
      paneTails: { w3: { text: 'plain prose, no prompt here', ts: Date.now() } as never },
    });
    render(<AttentionBell />);
    fireEvent.click(screen.getByRole('button', { name: /1 pane is waiting/i }));
    expect(screen.getByText('Message from #2 Codex not sent — Send or Clear it on the pane')).toBeTruthy();
  });
});
