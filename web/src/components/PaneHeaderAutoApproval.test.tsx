import { cleanup, render, screen } from '@testing-library/react';
import { afterEach, expect, it } from 'vitest';
import type { PaneInfo } from '../types';
import { useStore } from '../store';
import { PaneHeaderBadges } from './PaneHeaderBadges';

const pane: PaneInfo = {
  id: 'approval-pane', cwd: '/project', cmd: ['codex'], title: 'Build',
  status: 'running', createdAt: '', cols: 60, rows: 58, pid: 0,
  lastActivityMs: 0, hasClaude: false, hasCodex: true, autoMode: 'always',
  state: 'choice-prompt', attention: 'needs-user',
  attentionReason: "won't auto-approve — focused option is not an accept-once approval",
};

afterEach(() => {
  cleanup();
  useStore.setState({ ariaAutoPending: [] });
});

it('shows a blocked approval beside the enabled mode and explains why', () => {
  const { rerender } = render(<PaneHeaderBadges pane={pane} paneIndex={1} />);
  expect(screen.getByText('auto blocked')).toHaveAttribute('title', `Auto-approval needs your review: ${pane.attentionReason}`);
  expect(screen.getByRole('button', { name: /Auto-approve:/ })).toBeInTheDocument();
  expect(screen.queryByText('auto-approving…')).not.toBeInTheDocument();
  rerender(<PaneHeaderBadges pane={{ ...pane, state: 'busy', attention: 'none' }} paneIndex={1} />);
  expect(screen.queryByText('auto blocked')).not.toBeInTheDocument();
});

it('does not show an approval blocker when auto approval is off', () => {
  render(<PaneHeaderBadges pane={{ ...pane, autoMode: '' }} paneIndex={1} />);
  expect(screen.queryByText('auto blocked')).not.toBeInTheDocument();
});

it('a blocked decision replaces an obsolete countdown', () => {
  useStore.setState({ ariaAutoPending: [{
    confirmId: 'old-confirmation', paneId: pane.id, paneTitle: pane.title,
    input: '\\r', orderId: 'test', startedAt: Date.now(),
    deadlineAt: Date.now() + 5000, countdownMs: 5000, status: 'pending',
  }] });
  render(<PaneHeaderBadges pane={pane} paneIndex={1} />);
  expect(screen.getByText('auto blocked')).toBeInTheDocument();
  expect(screen.queryByText(/approving in/)).not.toBeInTheDocument();
  expect(screen.queryByRole('button', { name: /^Cancel/ })).not.toBeInTheDocument();
});
