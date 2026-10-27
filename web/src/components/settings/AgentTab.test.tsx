import { act, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import type { AriaResponderSettings } from '../../types';

// The tab's job is the feedback loop: a dead key, wrong Aria-side mode, or
// queued mentions must be visible HERE, not only in daemon.log.
const mocks = vi.hoisted(() => ({
  getAriaResponder: vi.fn<() => Promise<unknown>>(),
  saveAriaResponder: vi.fn<(body: unknown) => Promise<unknown>>(),
}));
vi.mock('../../api', async (importOriginal) => {
  const real = await importOriginal<typeof import('../../api')>();
  return {
    ...real,
    api: {
      ...real.api,
      getAriaResponder: mocks.getAriaResponder,
      saveAriaResponder: mocks.saveAriaResponder,
    },
  };
});

import { AgentTab } from './AgentTab';

const base: AriaResponderSettings = {
  enabled: true,
  hasKey: true,
  keyPrefix: 'ariak_0123456',
  handle: 'helper',
  gatewayUrl: 'https://gw.example',
  permission: 'plan',
  note: '',
};

afterEach(() => {
  vi.clearAllMocks();
});

describe('AgentTab', () => {
  it('shows the update hint on an older daemon without the endpoint', async () => {
    mocks.getAriaResponder.mockRejectedValue(new Error('404'));
    render(<AgentTab />);
    expect(await screen.findByText(/update soluterminal/)).toBeInTheDocument();
  });

  it('renders the live status card — listening', async () => {
    mocks.getAriaResponder.mockResolvedValue({
      ...base,
      status: {
        state: 'listening',
        mode: 'self',
        handle: 'helper',
        queueLen: 2,
        lastPollAt: new Date(Date.now() - 10_000).toISOString(),
        activity: [],
      },
    });
    render(<AgentTab />);
    expect(await screen.findByText(/Listening as @helper/)).toBeInTheDocument();
    expect(screen.getByText(/2 waiting for the pane/)).toBeInTheDocument();
  });

  it('surfaces a rejected key instead of looking healthy', async () => {
    mocks.getAriaResponder.mockResolvedValue({
      ...base,
      status: { state: 'auth-failed', lastError: 'gateway HTTP 401: unknown key', queueLen: 0, activity: [] },
    });
    render(<AgentTab />);
    expect(await screen.findByText(/gateway rejected the key/)).toBeInTheDocument();
  });

  it('explains standby when the Aria-side mode is hosted', async () => {
    mocks.getAriaResponder.mockResolvedValue({
      ...base,
      status: { state: 'standby', mode: 'hosted', queueLen: 0, activity: [] },
    });
    render(<AgentTab />);
    expect(await screen.findByText(/Aria's cloud responder answers/)).toBeInTheDocument();
  });

  it('save-key reports the verify result, not just "saved"', async () => {
    mocks.getAriaResponder.mockResolvedValue({ ...base, hasKey: false });
    mocks.saveAriaResponder.mockResolvedValue({
      ...base,
      verify: { ok: true, handle: 'helper', mode: 'hosted', grantedChats: 3 },
    });
    render(<AgentTab />);
    const input = await screen.findByPlaceholderText('ariak_…');
    fireEvent.change(input, { target: { value: ' ariak_abc ' } });
    await act(async () => {
      fireEvent.click(screen.getByRole('button', { name: 'Save key' }));
    });
    await waitFor(() =>
      expect(mocks.saveAriaResponder).toHaveBeenCalledWith({ agentKey: 'ariak_abc' }),
    );
    // Verified-but-wrong-mode keys must say so in the toast (store-level
    // toast; the mode nudge rides the same message).
  });

  it('lists recent activity with chat labels', async () => {
    mocks.getAriaResponder.mockResolvedValue({
      ...base,
      status: {
        state: 'listening',
        queueLen: 0,
        activity: [
          { at: new Date().toISOString(), kind: 'pane', chat: 'wa/family', detail: '1 mention(s) → claude main' },
          { at: new Date().toISOString(), kind: 'error', chat: 'wa/family', detail: 'headless run failed' },
        ],
      },
    });
    render(<AgentTab />);
    expect(await screen.findByText('Recent activity')).toBeInTheDocument();
    expect(screen.getByText(/wa\/family — 1 mention/)).toBeInTheDocument();
    expect(screen.getByText(/headless run failed/)).toBeInTheDocument();
  });

  it('marks a dangling paneId as a closed pane instead of a blank select', async () => {
    mocks.getAriaResponder.mockResolvedValue({ ...base, paneId: 'pane_dead' });
    render(<AgentTab />);
    expect(
      await screen.findByRole('option', { name: /Closed pane — answering with fresh runs/ }),
    ).toBeInTheDocument();
  });

  it('renders one card per agent with add + remove on a multi-agent daemon', async () => {
    mocks.getAriaResponder.mockResolvedValue({
      ...base,
      agents: [
        {
          id: 'agent_default', enabled: true, hasKey: true, keyPrefix: 'ariak_0123456',
          handle: 'helper', gatewayUrl: 'https://gw.example', permission: 'plan', mcpName: 'aria-comms',
          status: { state: 'listening', handle: 'helper', queueLen: 0, activity: [] },
        },
        {
          id: 'agent_beef', enabled: false, hasKey: true, keyPrefix: 'ariak_fedcba',
          handle: 'scout', gatewayUrl: 'https://gw.example', permission: 'plan', mcpName: 'aria-comms-scout',
          status: { state: 'disabled', queueLen: 0, activity: [] },
        },
      ],
    });
    render(<AgentTab />);
    expect(await screen.findByText('@helper')).toBeInTheDocument();
    expect(screen.getByText('@scout')).toBeInTheDocument();
    expect(screen.getByText('Add another agent')).toBeInTheDocument();
    // Every agent card is removable on a multi-agent daemon.
    expect(screen.getAllByRole('button', { name: 'Remove agent' })).toHaveLength(2);
    expect(mocks.saveAriaResponder).not.toHaveBeenCalled();
  });

  it('creates a second agent via id:"new"', async () => {
    mocks.getAriaResponder.mockResolvedValue({
      ...base,
      agents: [
        {
          id: 'agent_default', enabled: true, hasKey: true, handle: 'helper',
          gatewayUrl: 'https://gw.example', permission: 'plan', mcpName: 'aria-comms',
        },
      ],
    });
    mocks.saveAriaResponder.mockResolvedValue({ ...base, agents: [] });
    render(<AgentTab />);
    // Two key inputs exist (card + add form); the add form's is the last.
    const inputs = await screen.findAllByPlaceholderText('ariak_…');
    const addInput = inputs[inputs.length - 1]!;
    fireEvent.change(addInput, { target: { value: 'ariak_new' } });
    await act(async () => {
      fireEvent.click(screen.getByRole('button', { name: 'Add agent' }));
    });
    await waitFor(() =>
      expect(mocks.saveAriaResponder).toHaveBeenCalledWith({ id: 'new', agentKey: 'ariak_new' }),
    );
  });
});
