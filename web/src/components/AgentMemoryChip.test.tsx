import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import type { PaneInfo } from '../types';
import { useStore } from '../store';
import { AgentMemoryChip } from './AgentMemoryChip';
import { DialogHost } from './DialogHost';

const codexPane = {
  id: 'codex-focus',
  kind: 'native',
  agent: 'codex',
  codexThreadId: 'thread-1',
  agentRssMB: 384,
} as PaneInfo;

describe('AgentMemoryChip', () => {
  afterEach(() => useStore.setState({ dialog: null }));

  it('shows Codex Focus RAM and confirms a thread-preserving reclaim', async () => {
    const onReclaim = vi.fn();
    render(
      <>
        <AgentMemoryChip pane={codexPane} onReclaim={onReclaim} />
        <DialogHost />
      </>,
    );

    fireEvent.click(screen.getByRole('button', { name: /Agent memory 384 MB/ }));
    expect(await screen.findByText(/resumes the same thread/)).toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: 'Restart & reclaim' }));
    await waitFor(() => expect(onReclaim).toHaveBeenCalledTimes(1));
  });

  it('stays hidden until the daemon has sampled memory', () => {
    const { container } = render(
      <AgentMemoryChip pane={{ ...codexPane, agentRssMB: 0 }} onReclaim={vi.fn()} />,
    );
    expect(container).toBeEmptyDOMElement();
  });
});
