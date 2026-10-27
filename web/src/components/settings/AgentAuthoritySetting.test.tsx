import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { api } from '../../api';
import { AGENT_AUTHORITY_LABEL, AgentAuthoritySetting } from './AgentAuthoritySetting';

afterEach(() => vi.restoreAllMocks());

describe('AgentAuthoritySetting', () => {
  it('stays hidden on a daemon without the endpoint', async () => {
    const get = vi.spyOn(api, 'getAgentAuthority').mockResolvedValue(null);
    render(<AgentAuthoritySetting />);
    await waitFor(() => expect(get).toHaveBeenCalled());
    expect(screen.queryByText(AGENT_AUTHORITY_LABEL)).toBeNull();
  });

  it('is off by default and turns on for a limited time', async () => {
    vi.spyOn(api, 'getAgentAuthority').mockResolvedValue({ panes: false, active: false });
    const until = new Date(Date.now() + 8 * 3_600_000).toISOString();
    const set = vi.spyOn(api, 'setAgentAuthority').mockResolvedValue({ panes: true, until, active: true });
    render(<AgentAuthoritySetting />);
    const select = await screen.findByRole('combobox');
    expect(select).toHaveValue('off');
    fireEvent.change(select, { target: { value: '8' } });
    await waitFor(() => expect(set).toHaveBeenCalledWith({ panes: true, hours: 8 }));
    await waitFor(() => expect(select).toHaveValue('8'));
    expect(screen.getByText(/Ends /)).toBeInTheDocument();
  });

  it('turns off', async () => {
    vi.spyOn(api, 'getAgentAuthority').mockResolvedValue({ panes: true, active: true });
    const set = vi.spyOn(api, 'setAgentAuthority').mockResolvedValue({ panes: false, active: false });
    render(<AgentAuthoritySetting />);
    const select = await screen.findByRole('combobox');
    expect(select).toHaveValue('always');
    fireEvent.change(select, { target: { value: 'off' } });
    await waitFor(() => expect(set).toHaveBeenCalledWith({ panes: false }));
  });

  it('shows the refusal when an agent tries to turn it on', async () => {
    vi.spyOn(api, 'getAgentAuthority').mockResolvedValue({ panes: false, active: false });
    vi.spyOn(api, 'setAgentAuthority').mockRejectedValue(new Error('To turn on agent permissions, use the SoluTerminal app.'));
    render(<AgentAuthoritySetting />);
    fireEvent.change(await screen.findByRole('combobox'), { target: { value: '1' } });
    expect(await screen.findByText(/Couldn't save agent permissions/)).toBeInTheDocument();
  });
});
