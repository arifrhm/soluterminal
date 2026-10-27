import { act, fireEvent, render, screen } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { api } from './api';
import { useStore } from './store';
import type { Health } from './types';
import { agentReadiness, startAgent, useAgentInstallPrompt } from './agentStart';
import { AgentInstallPrompt } from './components/AgentInstallPrompt';
import { setupStartedWithin, resetSetupActivityForTests } from './lib/setupActivity';

const healthRef = vi.hoisted(() => ({ current: null as Health | null }));
vi.mock('./lib/useDaemonHealth', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./lib/useDaemonHealth')>();
  return {
    ...actual,
    getDaemonHealthState: () => ({ ...actual.getDaemonHealthState(), health: healthRef.current }),
    refreshDaemonHealth: vi.fn(),
  };
});

function health(over: Partial<Health> = {}): Health {
  return {
    claudeChecked: true,
    claudeInstalled: true,
    claudeAuthed: true,
    codexChecked: true,
    codexInstalled: true,
    codexAuthed: true,
    ...over,
  } as Health;
}

const createPane = vi.fn();
const toast = vi.fn();

beforeEach(() => {
  createPane.mockReset().mockResolvedValue({ id: 'p1' });
  toast.mockReset();
  resetSetupActivityForTests();
  useStore.setState({ createPane, toast, panes: {} } as never);
  useAgentInstallPrompt.setState({ agent: null });
});
afterEach(() => {
  healthRef.current = null;
  vi.restoreAllMocks();
});

describe('agentReadiness', () => {
  it('reads the A5 health fields, and is unknown until the daemon has probed', () => {
    expect(agentReadiness('codex', null)).toBe('unknown');
    expect(agentReadiness('codex', health({ codexChecked: false, codexInstalled: false }))).toBe('unknown');
    expect(agentReadiness('codex', health({ codexChecked: undefined }))).toBe('unknown');
    expect(agentReadiness('codex', health({ codexInstalled: false }))).toBe('missing');
    expect(agentReadiness('claude', health({ claudeAuthed: false }))).toBe('signed-out');
    expect(agentReadiness('claude', health())).toBe('ready');
  });

  it("ignores the other side's answer in Dual mode", () => {
    expect(agentReadiness('codex', health({ codexInstalled: false, backendID: 'win' }), 'wsl')).toBe('unknown');
    expect(agentReadiness('codex', health({ codexInstalled: false, backendID: 'wsl' }), 'wsl')).toBe('missing');
  });
});

describe('startAgent', () => {
  it('opens the install card instead of a broken pane when the agent is missing', async () => {
    healthRef.current = health({ codexInstalled: false });
    vi.spyOn(api, 'codexRecheck').mockResolvedValue({
      codexChecked: true, codexInstalled: false, codexVersion: '', codexAuthed: false,
    } as never);
    await expect(startAgent('codex')).resolves.toBe('install');
    expect(createPane).not.toHaveBeenCalled();
    expect(useAgentInstallPrompt.getState().agent).toBe('codex');
  });

  it('starts it when a fresh check finds it was installed meanwhile', async () => {
    healthRef.current = health({ claudeInstalled: false });
    vi.spyOn(api, 'claudeRecheck').mockResolvedValue({
      claudeChecked: true, claudeInstalled: true, claudeVersion: '2.0', claudeAuthed: true,
    } as never);
    await expect(startAgent('claude')).resolves.toBe('started');
    expect(createPane).toHaveBeenCalledWith({ cwd: '', cmd: ['claude'], title: 'claude' });
  });

  it('starts as before while health is still unknown', async () => {
    await expect(startAgent('codex')).resolves.toBe('started');
    expect(createPane).toHaveBeenCalledWith({ cwd: '', cmd: ['codex'], agent: 'codex', title: 'codex' });
    expect(toast).not.toHaveBeenCalled();
  });

  it('starts a signed-out agent, says it will ask to sign in, and holds the consent question', async () => {
    healthRef.current = health({ codexAuthed: false });
    await startAgent('codex');
    expect(createPane).toHaveBeenCalledTimes(1);
    expect(toast.mock.calls[0][0]).toBe('Codex will ask you to sign in first. Follow the steps in the new pane.');
    expect(setupStartedWithin(60_000)).toBe(true);
  });
});

describe('AgentInstallPrompt', () => {
  it('says the agent is missing and runs the same reviewed installer card', async () => {
    const spec = { supported: true, cmd: ['sh', '-c', 'install codex'], display: 'curl … | sh', note: 'Installs Codex.' };
    vi.spyOn(api, 'codexInstallCommand').mockResolvedValue(spec as never);
    render(<AgentInstallPrompt />);
    expect(screen.queryByRole('dialog')).toBeNull();
    act(() => useAgentInstallPrompt.getState().show('codex'));
    expect(screen.getByRole('dialog', { name: 'Codex isn’t installed yet.' })).toBeInTheDocument();

    await act(async () => { fireEvent.click(screen.getByRole('button', { name: 'Install' })); });
    expect(screen.getByText('Run Codex installer?')).toBeInTheDocument();
    expect(screen.getByText('curl … | sh')).toBeInTheDocument();
    expect(createPane).not.toHaveBeenCalled();

    await act(async () => { fireEvent.click(screen.getByRole('button', { name: 'Run installer in new pane' })); });
    expect(createPane).toHaveBeenCalledWith({ cwd: '', cmd: spec.cmd, title: 'Install Codex' });
    expect(useAgentInstallPrompt.getState().agent).toBeNull();
    expect(setupStartedWithin(60_000)).toBe(true);
  });

  it('closes on Cancel without installing', () => {
    render(<AgentInstallPrompt />);
    act(() => useAgentInstallPrompt.getState().show('claude'));
    expect(screen.getByRole('heading', { name: 'Claude Code isn’t installed yet.' })).toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: 'Cancel' }));
    expect(useAgentInstallPrompt.getState().agent).toBeNull();
    expect(createPane).not.toHaveBeenCalled();
  });
});
