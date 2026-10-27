import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { api } from '../../api';
import type { AgentIntegrationSettings } from '../../api';
import { AGENT_INTEGRATION_LABEL, AgentIntegrationSetting } from './AgentIntegrationSetting';

const base: AgentIntegrationSettings = {
  state: 'on',
  source: 'existing',
  effective: 'on',
  needsAnswer: false,
  noticePending: false,
  operatorDefault: false,
  changes: [
    { what: 'Claude Code: hooks', path: '/home/u/.claude/settings.json' },
    { what: 'Codex: SoluTerminal\'s tools', path: '/home/u/.codex/config.toml' },
  ],
};

afterEach(() => vi.restoreAllMocks());

describe('AgentIntegrationSetting', () => {
  it('stays hidden on a daemon without the endpoint', async () => {
    const get = vi.spyOn(api, 'getAgentIntegration').mockResolvedValue(null);
    render(<AgentIntegrationSetting />);
    await waitFor(() => expect(get).toHaveBeenCalled());
    expect(screen.queryByText(AGENT_INTEGRATION_LABEL)).toBeNull();
  });

  it('shows the switch and exactly what it changes', async () => {
    vi.spyOn(api, 'getAgentIntegration').mockResolvedValue(base);
    render(<AgentIntegrationSetting />);
    expect(await screen.findByLabelText(AGENT_INTEGRATION_LABEL)).toBeChecked();
    expect(screen.getByText('/home/u/.claude/settings.json')).toBeInTheDocument();
    expect(screen.getByText('/home/u/.codex/config.toml')).toBeInTheDocument();
  });

  it('turns it off and reports the removal', async () => {
    vi.spyOn(api, 'getAgentIntegration').mockResolvedValue(base);
    const set = vi.spyOn(api, 'setAgentIntegration').mockResolvedValue({
      ...base,
      state: 'off',
      source: 'user',
      effective: 'off',
      cleanup: [{ what: 'Claude Code hooks', result: 'removed' }],
    });
    render(<AgentIntegrationSetting />);
    const box = await screen.findByLabelText(AGENT_INTEGRATION_LABEL);
    fireEvent.click(box);
    await waitFor(() => expect(set).toHaveBeenCalledWith('off'));
    await waitFor(() => expect(box).not.toBeChecked());
    expect(screen.getByText(/Removed\. A copy of each changed file/)).toBeInTheDocument();
  });

  it('shows entries that could not be removed', async () => {
    vi.spyOn(api, 'getAgentIntegration').mockResolvedValue(base);
    vi.spyOn(api, 'setAgentIntegration').mockResolvedValue({
      ...base,
      state: 'off',
      effective: 'off',
      cleanup: [{ what: 'Codex MCP server', result: '', error: 'permission denied' }],
    });
    render(<AgentIntegrationSetting />);
    fireEvent.click(await screen.findByLabelText(AGENT_INTEGRATION_LABEL));
    expect(await screen.findByText(/Codex MCP server \(permission denied\)/)).toBeInTheDocument();
  });

  it('rolls back when saving fails', async () => {
    vi.spyOn(api, 'getAgentIntegration').mockResolvedValue({ ...base, state: 'off', effective: 'off' });
    vi.spyOn(api, 'setAgentIntegration').mockRejectedValue(new Error('offline'));
    render(<AgentIntegrationSetting />);
    const box = await screen.findByLabelText(AGENT_INTEGRATION_LABEL);
    fireEvent.click(box);
    expect(await screen.findByText(/Couldn't save the setting/)).toBeInTheDocument();
    expect(box).not.toBeChecked();
  });
});
