import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest';
import { render, screen, fireEvent, waitFor } from '@testing-library/react';
import type { Health } from '../types';

// --- Mocks ---------------------------------------------------------------
// Controllable daemon-health value per test.
let healthValue: Health | null = null;
const refreshDaemonHealth = vi.fn();
vi.mock('../lib/useDaemonHealth', () => ({
  useDaemonHealth: () => ({ health: healthValue, lastOkMs: 0, polling: false }),
  refreshDaemonHealth: () => refreshDaemonHealth(),
}));

let desktop = true;
vi.mock('../cloudPair', () => ({
  isDesktopShell: () => desktop,
}));

const claudeRecheck = vi.fn().mockResolvedValue({
  claudeChecked: true, claudeInstalled: false, claudeVersion: '', claudeAuthed: false,
});
const claudeInstallCommand = vi.fn().mockResolvedValue({
  supported: true,
  cmd: ['bash', '-lc', 'curl -fsSL https://claude.ai/install.sh | bash'],
  display: 'curl -fsSL https://claude.ai/install.sh | bash',
  note: 'Installs the Claude Code CLI (no admin needed).',
});
vi.mock('../api', () => ({
  api: {
    claudeRecheck: () => claudeRecheck(),
    claudeInstallCommand: () => claudeInstallCommand(),
  },
}));

// Stub the store the install hook reaches via getState().
const storeMock = vi.hoisted(() => {
  const toast = vi.fn();
  const createPane = vi.fn().mockResolvedValue(undefined);
  type StoreState = {
    activeBackend: 'wsl' | 'win' | null;
    panes: Record<string, unknown>;
    toast: typeof toast;
    createPane: typeof createPane;
  };
  let state: StoreState = {
    activeBackend: null,
    panes: {},
    toast,
    createPane,
  };
  const useStore = vi.fn((selector: (s: StoreState) => unknown) => selector(state)) as
    ReturnType<typeof vi.fn> & { getState: () => StoreState };
  useStore.getState = () => state;
  const reset = () => {
    state = {
      activeBackend: null,
      panes: {},
      toast,
      createPane,
    };
  };
  const setActiveBackend = (activeBackend: 'wsl' | 'win' | null) => {
    state = { ...state, activeBackend };
  };
  return { toast, createPane, useStore, reset, setActiveBackend };
});
vi.mock('../store', () => ({
  useStore: storeMock.useStore,
}));

import { ClaudeInstallBanner } from './ClaudeInstallBanner';
import { useEntitlements } from '../entitlements';
import { setExtensionsForTests } from '../extensions';

function health(over: Partial<Health>): Health {
  return { status: 'ok', version: '0.0.0', ...over } as Health;
}

describe('ClaudeInstallBanner', () => {
  beforeEach(() => {
    window.history.replaceState(null, '', '/');
    healthValue = null;
    desktop = true;
    storeMock.reset();
    vi.clearAllMocks();
    claudeRecheck.mockResolvedValue({
      claudeChecked: true, claudeInstalled: false, claudeVersion: '', claudeAuthed: false,
    });
    claudeInstallCommand.mockResolvedValue({
      supported: true,
      cmd: ['bash', '-lc', 'curl -fsSL https://claude.ai/install.sh | bash'],
      display: 'curl -fsSL https://claude.ai/install.sh | bash',
      note: 'Installs the Claude Code CLI (no admin needed).',
    });
  });
  afterEach(() => vi.restoreAllMocks());

  it('shows when the daemon reports claude checked + NOT installed', () => {
    healthValue = health({ claudeChecked: true, claudeInstalled: false });
    render(<ClaudeInstallBanner />);
    expect(screen.getByText(/Claude Code isn’t installed yet\./i)).toBeInTheDocument();
    expect(screen.getByRole('button', { name: /^Install$/i })).toBeInTheDocument();
    // Plain words only: no command names, jargon or backticks.
    expect(document.body.textContent).not.toMatch(/CLI|this side|`/);
  });

  it('stays hidden while the setup checklist shows the same step', () => {
    healthValue = health({ claudeChecked: true, claudeInstalled: false });
    render(<ClaudeInstallBanner hidden />);
    expect(screen.queryByText(/Claude Code isn’t installed/i)).toBeNull();
  });

  it('is hidden while the boot probe is still in flight (claudeChecked false)', () => {
    healthValue = health({ claudeChecked: false, claudeInstalled: false });
    render(<ClaudeInstallBanner />);
    expect(screen.queryByText(/Claude Code isn’t installed/i)).toBeNull();
  });

  it('is hidden once claude is installed AND signed in', () => {
    healthValue = health({ claudeChecked: true, claudeInstalled: true, claudeAuthed: true });
    render(<ClaudeInstallBanner />);
    expect(screen.queryByText(/Claude Code isn’t installed/i)).toBeNull();
    expect(screen.queryByText(/isn’t signed in/i)).toBeNull();
  });

  it('shows the sign-in nudge when installed but NOT signed in', () => {
    healthValue = health({ backendID: 'win', claudeChecked: true, claudeInstalled: true, claudeAuthed: false });
    render(<ClaudeInstallBanner />);
    expect(screen.getByText(/Claude Code isn’t signed in/i)).toBeInTheDocument();
    expect(screen.getByRole('button', { name: /^Log in$/i })).toBeInTheDocument();
    // Not the install state.
    expect(screen.queryByText(/isn’t installed/i)).toBeNull();
  });

  it('names the active backend in dual mode', () => {
    window.history.replaceState(null, '', '/?solu_router=1&b=win');
    storeMock.setActiveBackend('win');
    healthValue = health({ backendID: 'win', claudeChecked: true, claudeInstalled: true, claudeAuthed: false });

    render(<ClaudeInstallBanner />);

    expect(screen.getByText(/Claude Code isn’t signed in yet in Windows PowerShell\./i)).toBeInTheDocument();
    expect(screen.getByText(/Linux \(WSL\) and Windows PowerShell each need their own sign-in/i)).toBeInTheDocument();
  });

  it('does not show stale WSL health while PowerShell is active', () => {
    window.history.replaceState(null, '', '/?solu_router=1&b=win');
    storeMock.setActiveBackend('win');
    healthValue = health({
      backendID: 'wsl',
      claudeChecked: true,
      claudeInstalled: false,
      claudeAuthed: false,
    });

    render(<ClaudeInstallBanner />);

    expect(screen.queryByText(/Claude Code isn’t installed/i)).toBeNull();
    expect(screen.queryByText(/PowerShell/i)).toBeNull();
  });

  it('dismisses only the active backend need for the session', () => {
    window.history.replaceState(null, '', '/?solu_router=1&b=wsl');
    storeMock.setActiveBackend('wsl');
    healthValue = health({
      backendID: 'wsl',
      claudeChecked: true,
      claudeInstalled: false,
      claudeAuthed: false,
    });
    const { rerender } = render(<ClaudeInstallBanner />);
    fireEvent.click(screen.getByRole('button', { name: /Dismiss/i }));
    expect(screen.queryByText(/Claude Code isn’t installed/i)).toBeNull();

    window.history.replaceState(null, '', '/?solu_router=1&b=win');
    storeMock.setActiveBackend('win');
    healthValue = health({
      backendID: 'win',
      claudeChecked: true,
      claudeInstalled: false,
      claudeAuthed: false,
    });
    rerender(<ClaudeInstallBanner />);

    expect(screen.getByText(/Claude Code isn’t installed yet in Windows PowerShell/i)).toBeInTheDocument();
  });

  it('Log in spawns a sign-in pane and dismisses', async () => {
    healthValue = health({ claudeChecked: true, claudeInstalled: true, claudeAuthed: false });
    render(<ClaudeInstallBanner />);
    fireEvent.click(screen.getByRole('button', { name: /^Log in$/i }));
    await waitFor(() =>
      expect(storeMock.createPane).toHaveBeenCalledWith(
        expect.objectContaining({ title: 'Sign in to Claude', cmd: ['claude'] }),
      ),
    );
  });

  it('is hidden outside the desktop shell (phone/relay web)', () => {
    desktop = false;
    healthValue = health({ claudeChecked: true, claudeInstalled: false });
    render(<ClaudeInstallBanner />);
    expect(screen.queryByText(/Claude Code isn’t installed/i)).toBeNull();
  });

  it('is hidden on an older daemon that predates claude detection (fields absent)', () => {
    healthValue = health({}); // claudeChecked === undefined
    render(<ClaudeInstallBanner />);
    expect(screen.queryByText(/Claude Code isn’t installed/i)).toBeNull();
  });

  it('Install discloses the exact command in a confirm card before running it', async () => {
    healthValue = health({ claudeChecked: true, claudeInstalled: false });
    render(<ClaudeInstallBanner />);

    fireEvent.click(screen.getByRole('button', { name: /^Install$/i }));

    // No pane spawned yet — the confirm card appears first with the command.
    await waitFor(() => expect(screen.getByText(/Run Claude Code installer\?/i)).toBeInTheDocument());
    expect(screen.getByText(/curl -fsSL https:\/\/claude\.ai\/install\.sh \| bash/)).toBeInTheDocument();
    expect(storeMock.createPane).not.toHaveBeenCalled();

    // Confirming spawns the installer pane.
    fireEvent.click(screen.getByRole('button', { name: /Run installer in new pane/i }));
    await waitFor(() =>
      expect(storeMock.createPane).toHaveBeenCalledWith(
        expect.objectContaining({ title: 'Install Claude Code', cmd: expect.arrayContaining(['bash']) }),
      ),
    );
  });

  it('only points team installs to Credentials when hiding for good', () => {
    healthValue = health({ claudeChecked: true, claudeInstalled: false });
    useEntitlements.setState({ loaded: true, team: false, operator: false });
    const { unmount } = render(<ClaudeInstallBanner />);
    const hide = screen.getByRole('button', { name: 'Don’t show again' });
    expect(hide.getAttribute('title')).not.toMatch(/Credentials/);
    unmount();
    useEntitlements.setState({ loaded: true, team: true, operator: false });
    // A build without the Credentials section has nowhere to point.
    setExtensionsForTests([]);
    const second = render(<ClaudeInstallBanner />);
    expect(screen.getByRole('button', { name: 'Don’t show again' }).getAttribute('title')).not.toMatch(/Credentials/);
    second.unmount();
    setExtensionsForTests([{ sidebarSections: { credentials: () => null } }]);
    render(<ClaudeInstallBanner />);
    expect(screen.getByRole('button', { name: 'Don’t show again' }).getAttribute('title')).toMatch(/Credentials/);
    setExtensionsForTests(null);
    useEntitlements.setState({ loaded: false, team: false, operator: false });
  });

  it('× dismisses the banner for the session', () => {
    healthValue = health({ claudeChecked: true, claudeInstalled: false });
    render(<ClaudeInstallBanner />);
    expect(screen.getByText(/Claude Code isn’t installed/i)).toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: /Dismiss/i }));
    expect(screen.queryByText(/Claude Code isn’t installed/i)).toBeNull();
  });

  it('forces a fresh probe + health refresh on mount (desktop)', async () => {
    healthValue = health({ claudeChecked: true, claudeInstalled: false });
    render(<ClaudeInstallBanner />);
    await waitFor(() => expect(refreshDaemonHealth).toHaveBeenCalled());
    expect(claudeRecheck).toHaveBeenCalled();
  });
});
