import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import { afterEach, expect, it, vi } from 'vitest';
import { api } from '../api';
import { useStore } from '../store';
import type { PaneInfo } from '../types';
import { UnsentMessageChip } from './UnsentMessageChip';

const pane = { id: 'p1', unsentMessage: { id: 'receipt', sender: '#5 Claude' } } as PaneInfo;
afterEach(() => vi.restoreAllMocks());

it.each(['send', 'clear'] as const)('binds %s to the displayed pane and receipt', async (action) => {
  const act = vi.spyOn(api, 'actOnUnsentMessage').mockResolvedValue({ outcome: action === 'clear' ? 'cleared' : 'submitted' });
  // refreshPane restarts a native agent; the chip must only reload pane data.
  const refresh = vi.fn().mockResolvedValue(undefined);
  const restart = vi.fn();
  useStore.setState({ refreshPanes: refresh, refreshPane: restart });
  render(<UnsentMessageChip pane={pane} />);
  expect(screen.getByRole('status').textContent).toContain('Unsent message from #5 Claude');
  fireEvent.click(screen.getByRole('button', { name: action === 'send' ? 'Send' : 'Clear' }));
  await waitFor(() => expect(refresh).toHaveBeenCalled());
  expect(restart).not.toHaveBeenCalled();
  expect(act).toHaveBeenCalledExactlyOnceWith('p1', 'receipt', action);
});

it('surfaces a refused clear and never falls back to raw keys', async () => {
  vi.spyOn(api, 'actOnUnsentMessage').mockRejectedValue(new Error('input changed; text preserved'));
  const toast = vi.fn();
  useStore.setState({ toast });
  render(<UnsentMessageChip pane={pane} />);
  fireEvent.click(screen.getByRole('button', { name: 'Clear' }));
  await waitFor(() => expect(toast).toHaveBeenCalledWith(expect.stringContaining('text preserved'), { kind: 'warn' }));
});

it('renders nothing without a receipt', () => {
  const { container } = render(<UnsentMessageChip pane={{ id: 'p1' } as PaneInfo} />);
  expect(container.textContent).toBe('');
});
