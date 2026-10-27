import { describe, expect, it } from 'vitest';
import { render, screen } from '@testing-library/react';
import type { PaneInfo } from '../types';
import { PaneTurnChip } from './PaneTurnChip';

const now = Date.now();
const pane = {
  id: 'pane_1', cwd: '/r', cmd: ['claude'], title: 'agent', status: 'running', hasClaude: true,
  turn: { active: false, count: 1, last: { n: 1, startedMs: now - 60_000, endedMs: now - 5_000, files: 2, added: 5, removed: 1 } },
} as PaneInfo;

describe('PaneTurnChip', () => {
  it('carries the unread signal as a "new" tag for an unseen finished turn', () => {
    render(<PaneTurnChip pane={pane} unseen />);
    const chip = screen.getByRole('button', { name: /New: last turn changed 2 files/ });
    expect(chip.className).toContain('unseen');
    expect(chip.textContent).toContain('new');
  });

  it('is plain once the turn has been seen, and hidden while a turn runs', () => {
    const { rerender, container } = render(<PaneTurnChip pane={pane} unseen={false} />);
    expect(container.textContent).not.toContain('new');
    rerender(<PaneTurnChip pane={{ ...pane, turn: { ...pane.turn!, active: true, startedMs: now } }} unseen={false} />);
    expect(container.textContent).toBe('');
  });

  it('formats singular and plural file counts correctly in accessible aria-label', () => {
    const singleFilePane = {
      ...pane,
      turn: { ...pane.turn!, last: { ...pane.turn!.last!, files: 1 } },
    } as PaneInfo;
    const { rerender } = render(<PaneTurnChip pane={singleFilePane} unseen={false} />);
    expect(screen.getByRole('button', { name: 'last turn changed 1 file. Review changes' })).toBeDefined();

    const multiFilePane = {
      ...pane,
      turn: { ...pane.turn!, last: { ...pane.turn!.last!, files: 3 } },
    } as PaneInfo;
    rerender(<PaneTurnChip pane={multiFilePane} unseen={false} />);
    expect(screen.getByRole('button', { name: 'last turn changed 3 files. Review changes' })).toBeDefined();
  });
});
