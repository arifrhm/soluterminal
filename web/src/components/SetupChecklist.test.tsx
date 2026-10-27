import { fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { api } from '../api';
import { useEntitlements } from '../entitlements';
import { useCloud } from '../cloud';
import { SetupChecklist, launchModeDetail } from './SetupChecklist';

const SIGNED_OUT = 'Talk instead of typing, see your agents on your phone, get a push when one needs you. Everything on this PC works without one.';

describe('launchModeDetail', () => {
  it('describes a no-account local daemon as optional, not as a failure', () => {
    expect(launchModeDetail('cloud', { signedIn: false, relayed: false, phone: false })).toBe(SIGNED_OUT);
    expect(launchModeDetail('local', { signedIn: false, relayed: false, phone: false })).toBe(SIGNED_OUT);
  });

  it('mentions the phone only where phone access exists', () => {
    expect(launchModeDetail('cloud', { signedIn: true, relayed: false, phone: false })).toBe('Signed in');
    expect(launchModeDetail('cloud', { signedIn: true, relayed: false, phone: true })).toBe('Signed in. You can reach this computer from your phone.');
  });

  it('never calls a local-only daemon signed in', () => {
    expect(launchModeDetail('local', { signedIn: false, relayed: true, phone: false })).toBe(SIGNED_OUT);
  });

  it('covers relayed and detecting states', () => {
    expect(launchModeDetail('cloud', { signedIn: true, relayed: true, phone: false })).toBe('Signed in, connected to your computer through the cloud');
    expect(launchModeDetail('detecting', { signedIn: false, relayed: false, phone: false })).toBe('Checking…');
  });
});

type AgentHealth = { installed: boolean; authed: boolean };

function mockAgents(claude: AgentHealth, codex: AgentHealth | 'absent-from-daemon') {
  const health: Record<string, unknown> = {
    status: 'ok', version: '1', paneEnvironment: 'wsl',
    claudeChecked: true, claudeInstalled: claude.installed, claudeAuthed: claude.authed, claudeVersion: claude.installed ? '2.1.306 (Claude Code)' : '',
    museChecked: true, museInstalled: false, museAuthed: false, museVersion: '',
  };
  if (codex !== 'absent-from-daemon') {
    Object.assign(health, { codexChecked: true, codexInstalled: codex.installed, codexAuthed: codex.authed, codexVersion: codex.installed ? 'codex-cli 0.164.0' : '' });
  }
  vi.spyOn(api, 'health').mockResolvedValue(health as never);
  vi.spyOn(api, 'claudeRecheck').mockResolvedValue({ claudeChecked: true, claudeInstalled: claude.installed, claudeAuthed: claude.authed, claudeVersion: health.claudeVersion as string });
  vi.spyOn(api, 'museRecheck').mockResolvedValue({ museChecked: true, museInstalled: false, museAuthed: false, museVersion: '' });
  if (codex === 'absent-from-daemon') {
    vi.spyOn(api, 'codexRecheck').mockRejectedValue(Object.assign(new Error('404'), { status: 404 }));
  } else {
    vi.spyOn(api, 'codexRecheck').mockResolvedValue({ codexChecked: true, codexInstalled: codex.installed, codexAuthed: codex.authed, codexVersion: health.codexVersion as string });
  }
}

function row(key: string): HTMLElement {
  const el = document.querySelector<HTMLElement>(`[data-row="${key}"]`);
  if (!el) throw new Error(`row ${key} missing`);
  return el;
}

describe('SetupChecklist', () => {
  beforeEach(() => useEntitlements.setState({ loaded: true, team: false, operator: false }));
  afterEach(() => vi.restoreAllMocks());

  it('leaves Muse Code out for a public install', async () => {
    mockAgents({ installed: true, authed: true }, { installed: false, authed: false });
    render(<SetupChecklist open onClose={() => {}} />);
    await waitFor(() => expect(screen.getByText('Claude Code')).toBeInTheDocument());
    expect(screen.queryByText('Muse Code')).toBeNull();
    expect(api.museRecheck).not.toHaveBeenCalled();
  });

  it('keeps Muse Code for a team install', async () => {
    useEntitlements.setState({ loaded: true, team: true, operator: true });
    mockAgents({ installed: true, authed: true }, { installed: false, authed: false });
    render(<SetupChecklist open onClose={() => {}} />);
    expect(await screen.findByText('Muse Code')).toBeInTheDocument();
  });

  it('shows Codex with install and plan wording, and keeps Continue locked with no agent ready', async () => {
    mockAgents({ installed: false, authed: false }, { installed: false, authed: false });
    render(<SetupChecklist open onClose={() => {}} />);
    await waitFor(() => expect(within(row('codex')).getByText(/Not installed yet/)).toBeInTheDocument());
    expect(within(row('codex')).getByText(/ChatGPT Plus, Pro, Business, Edu or Enterprise plan, or an OpenAI API key/)).toBeInTheDocument();
    expect(within(row('claude')).getByText(/Claude Pro, Max, Team or Enterprise plan, or an Anthropic API key/)).toBeInTheDocument();
    expect(within(row('codex')).getByRole('button', { name: 'Review install' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Continue' })).toBeDisabled();
  });

  it('enables Continue as soon as Codex alone is ready', async () => {
    mockAgents({ installed: false, authed: false }, { installed: true, authed: true });
    const close = vi.fn();
    render(<SetupChecklist open onClose={close} />);
    await waitFor(() => expect(screen.getByRole('button', { name: 'Continue' })).toBeEnabled());
    expect(within(row('codex')).getByText('Ready to use (version 0.164.0).')).toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: 'Continue' }));
    expect(close).toHaveBeenCalledOnce();
  });

  it('offers Log in for an installed but signed-out Codex', async () => {
    mockAgents({ installed: true, authed: true }, { installed: true, authed: false });
    render(<SetupChecklist open onClose={() => {}} />);
    await waitFor(() => expect(within(row('codex')).getByRole('button', { name: 'Log in' })).toBeInTheDocument());
    expect(screen.getByRole('button', { name: 'Continue' })).toBeEnabled();
  });

  it('drops the Codex row on an older service that does not report it', async () => {
    mockAgents({ installed: true, authed: true }, 'absent-from-daemon');
    render(<SetupChecklist open onClose={() => {}} />);
    await waitFor(() => expect(screen.getByText('Claude Code')).toBeInTheDocument());
    expect(document.querySelector('[data-row="codex"]')).toBeNull();
  });

  it('shows a signed-out account as Optional, never as a green tick', async () => {
    useCloud.setState({ mode: 'cloud', withoutAccount: true, session: null, isLocal: true, accountOptional: true } as never);
    mockAgents({ installed: true, authed: true }, { installed: true, authed: true });
    render(<SetupChecklist open onClose={() => {}} />);
    await waitFor(() => expect(within(row('mode')).getByText('Optional')).toBeInTheDocument());
    expect(within(row('mode')).queryByText('✓')).toBeNull();
    expect(within(row('mode')).getByRole('button', { name: 'Create free account' })).toBeInTheDocument();
  });

  it('shows where agents run, with no setup action in a browser tab', async () => {
    mockAgents({ installed: true, authed: true }, { installed: true, authed: true });
    render(<SetupChecklist open onClose={() => {}} />);
    await waitFor(() => expect(within(row('backend')).getByText('Linux (WSL)')).toBeInTheDocument());
    expect(within(row('backend')).queryByRole('button')).toBeNull();
  });
});
