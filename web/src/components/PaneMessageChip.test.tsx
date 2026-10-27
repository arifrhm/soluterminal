import { fireEvent, render, screen } from '@testing-library/react';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import type { PaneInfo } from '../types';
import { useStore } from '../store';
import { MESSAGE_CHIP_MS, PaneMessageChip } from './PaneMessageChip';

const revealPane = vi.fn(async () => {});
const NOW = 1_800_000_000_000;
const pane = (at?: number) => ({ id: 'b', cwd: '/', cmd: ['codex'], title: 'B', status: 'running', lastAgentMessage: at ? { fromPaneId: 'a', fromLabel: '#3 Codex', at, outcome: 'submitted' } : undefined }) as PaneInfo;

describe('PaneMessageChip', () => {
  beforeEach(() => {
    revealPane.mockClear();
    useStore.setState({ panes: { a: { id: 'a' } as PaneInfo }, revealPane } as never);
  });

  it('shows a recent message from another agent and goes to the sender', () => {
    render(<PaneMessageChip pane={pane(NOW - 60_000)} now={() => NOW} />);
    const chip = screen.getByRole('button');
    expect(chip.textContent).toBe('✉ #3 Codex');
    fireEvent.click(chip);
    expect(revealPane).toHaveBeenCalledWith('a');
  });

  it('is gone after a few minutes, and absent without a message', () => {
    const { container: a } = render(<PaneMessageChip pane={pane(NOW - MESSAGE_CHIP_MS - 1)} now={() => NOW} />);
    const { container: b } = render(<PaneMessageChip pane={pane()} now={() => NOW} />);
    expect(a.innerHTML + b.innerHTML).toBe('');
  });
});
