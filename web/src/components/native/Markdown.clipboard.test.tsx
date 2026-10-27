import { fireEvent, render, screen } from '@testing-library/react';
import { beforeEach, expect, it, vi } from 'vitest';
import { copyToClipboard } from '../../lib/clipboard';
import { Markdown } from './Markdown';
import { ToolCallView } from './NativeEventViews';
import type { NativeEvent } from '../../native/nativeEvents';

vi.mock('../../lib/clipboard', () => ({ copyToClipboard: vi.fn() }));
beforeEach(() => vi.mocked(copyToClipboard).mockReset());

it('copies a code block through the shared clipboard fallback', async () => {
  vi.mocked(copyToClipboard).mockResolvedValue(true);
  render(<Markdown source={'```js\nconst x = 1;\n```'} />);
  fireEvent.click(screen.getByRole('button', { name: 'Copy code' }));
  expect(await screen.findByRole('button', { name: 'Code copied' })).toBeInTheDocument();
  expect(copyToClipboard).toHaveBeenCalledWith('const x = 1;');
});

it('shows a retryable failure instead of claiming a blocked copy succeeded', async () => {
  vi.mocked(copyToClipboard).mockResolvedValueOnce(false).mockResolvedValueOnce(true);
  render(<Markdown source={'```\nkeep this text\n```'} />);
  fireEvent.click(screen.getByRole('button', { name: 'Copy code' }));
  const retry = await screen.findByRole('button', { name: 'Copy failed — retry copying code' });
  expect(retry).toHaveTextContent('Copy failed');
  expect(screen.getByText('keep this text')).toBeInTheDocument();
  fireEvent.click(retry);
  expect(await screen.findByRole('button', { name: 'Code copied' })).toBeInTheDocument();
});

it('keeps error output copy retryable when clipboard access fails', async () => {
  vi.mocked(copyToClipboard).mockResolvedValueOnce(false).mockResolvedValueOnce(true);
  const ev = { type: 'tool_call', paneId: 'p', seq: 1, fromReplay: false, callId: 'call', name: 'Bash', args: { command: 'test' } } as NativeEvent;
  const result = { type: 'tool_result', paneId: 'p', seq: 2, fromReplay: false, callId: 'call', content: 'error details', isError: true } as NativeEvent;
  render(<ToolCallView ev={ev} result={result} />);
  fireEvent.click(screen.getByRole('button', { name: 'Copy output' }));
  fireEvent.click(await screen.findByRole('button', { name: 'Copy failed — retry copying output' }));
  expect(await screen.findByRole('button', { name: 'Output copied' })).toBeInTheDocument();
  expect(copyToClipboard).toHaveBeenLastCalledWith('error details');
});
