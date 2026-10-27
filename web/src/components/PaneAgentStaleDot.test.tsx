import { fireEvent, render, screen } from '@testing-library/react';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import type { PaneInfo } from '../types';
import { confirmDialog } from '../lib/dialog';
import { useStore } from '../store';

vi.mock('../lib/dialog', () => ({ confirmDialog: vi.fn() }));

import { PaneAgentStaleDot } from './PaneAgentStaleDot';

const pane = (over: Partial<PaneInfo> = {}): PaneInfo => ({ id: 'pane-a', ...over } as PaneInfo);

describe('PaneAgentStaleDot', () => {
  beforeEach(() => {
    vi.restoreAllMocks();
    vi.mocked(confirmDialog).mockReset();
  });

  it('renders nothing when the pane agent is current', () => {
    const { container } = render(<PaneAgentStaleDot pane={pane({ agentVersion: '0.0.1142' })} />);
    expect(container).toBeEmptyDOMElement();
  });

  it('renders a quiet dot naming the running build when stale', () => {
    render(<PaneAgentStaleDot pane={pane({ agentStale: true, agentVersion: '0.0.1142' })} />);
    const dot = screen.getByRole('button', { name: /restart this pane/i });
    expect(dot).toHaveClass('pane-agent-stale-dot');
    expect(dot.title).toBe('Running v0.0.1142 · restart pane to update');
    expect(dot).toBeEmptyDOMElement();
  });

  it('falls back to a generic label without a reported version', () => {
    render(<PaneAgentStaleDot pane={pane({ agentStale: true })} />);
    expect(screen.getByRole('button').title).toBe('Running an older build · restart pane to update');
  });

  it('confirms before restarting just this pane', async () => {
    const restart = vi.spyOn(useStore.getState(), 'restartPane').mockResolvedValue(null);
    vi.mocked(confirmDialog).mockResolvedValue(true);
    render(<PaneAgentStaleDot pane={pane({ agentStale: true, agentVersion: '0.0.1142' })} />);
    fireEvent.click(screen.getByRole('button'));
    await vi.waitFor(() => expect(restart).toHaveBeenCalledWith('pane-a'));
    expect(confirmDialog).toHaveBeenCalledTimes(1);
  });

  it('does nothing when the confirm is declined', async () => {
    const restart = vi.spyOn(useStore.getState(), 'restartPane').mockResolvedValue(null);
    vi.mocked(confirmDialog).mockResolvedValue(false);
    render(<PaneAgentStaleDot pane={pane({ agentStale: true })} />);
    fireEvent.click(screen.getByRole('button'));
    await vi.waitFor(() => expect(confirmDialog).toHaveBeenCalledTimes(1));
    expect(restart).not.toHaveBeenCalled();
  });
});
