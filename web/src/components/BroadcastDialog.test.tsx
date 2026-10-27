import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import { afterEach, beforeEach, expect, it, vi } from 'vitest';
import { api } from '../api';
import { useStore } from '../store';
import type { PaneInfo } from '../types';
import { BroadcastDialog } from './BroadcastDialog';

const sender = vi.fn(() => true);
const toast = vi.fn();
beforeEach(() => {
  sender.mockClear(); toast.mockClear();
  useStore.setState({
    panes: { codex: { id:'codex', agent:'codex', cmd:['codex'], status:'running', title:'Codex' } as PaneInfo },
    senders: { codex: sender }, workspaces: [], toast, refreshPanes: vi.fn().mockResolvedValue(undefined),
  });
});
afterEach(() => vi.restoreAllMocks());

function broadcast(text: string) {
  const close = vi.fn();
  render(<BroadcastDialog onClose={close} />);
  fireEvent.click(screen.getByRole('button', { name:'All' }));
  fireEvent.change(screen.getByLabelText('Prompt'), { target: { value:text } });
  return close;
}

it.each(['line one\nline two', 'Long broadcast '.repeat(120)])('verifies a Codex broadcast through the daemon', async (text) => {
  const submit = vi.spyOn(api, 'submitPaneMessage').mockResolvedValue({ bytes:text.length, outcome:'submitted' });
  const close = broadcast(text);
  fireEvent.click(screen.getByRole('button', { name:'Send to 1' }));
  await waitFor(() => expect(close).toHaveBeenCalledOnce());
  expect(submit).toHaveBeenCalledExactlyOnceWith('codex', text);
  expect(sender).not.toHaveBeenCalled();
  expect(toast).toHaveBeenCalledWith('Broadcast: 1 submitted', expect.anything());
});

it('keeps insert-only broadcasts on the raw path', async () => {
  const submit = vi.spyOn(api, 'submitPaneMessage');
  const close = broadcast('Review before sending');
  fireEvent.click(screen.getByLabelText('Submit with Enter'));
  fireEvent.click(screen.getByRole('button', { name:'Send to 1' }));
  await waitFor(() => expect(close).toHaveBeenCalledOnce());
  expect(sender).toHaveBeenCalledExactlyOnceWith('Review before sending');
  expect(submit).not.toHaveBeenCalled();
});

it.each(['left_in_composer', 'sent_unconfirmed', 'failed'] as const)('reports %s without falling back to raw input', async (outcome) => {
  const submit = vi.spyOn(api, 'submitPaneMessage');
  if (outcome === 'failed') submit.mockRejectedValue(new Error('unavailable'));
  else submit.mockResolvedValue({ bytes:10, outcome });
  const close = broadcast('Please investigate');
  fireEvent.click(screen.getByRole('button', { name:'Send to 1' }));
  await waitFor(() => expect(close).toHaveBeenCalledOnce());
  expect(sender).not.toHaveBeenCalled();
  expect(toast).toHaveBeenCalledWith(expect.stringContaining(outcome === 'left_in_composer' ? 'left unsent' : outcome === 'sent_unconfirmed' ? 'without confirmation' : 'could not receive'), expect.objectContaining({ kind:'warn' }));
});
