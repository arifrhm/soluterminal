import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { act, fireEvent, render, screen, waitFor } from '@testing-library/react';
import type { Session } from '@supabase/supabase-js';
import { useCloud } from '../cloud';
import { useStore } from '../store';
import { CloudGate, hostCommands } from './CloudGate';

function desktop(invoke?: ReturnType<typeof vi.fn>) {
  Object.defineProperty(window, '__SOLU_TAURI__', { configurable: true, value: true });
  Object.defineProperty(window, '__SOLU_BACKEND__', {
    configurable: true, value: { launchMode: 'cloud', hostPlatform: 'windows' },
  });
  if (invoke) Object.defineProperty(window, '__TAURI_INTERNALS__', {
    configurable: true, value: { invoke },
  });
}

function openHost() {
  fireEvent.click(screen.getByRole('button', { name: /run soluterminal on this computer/i }));
}

describe('CloudGate desktop host onboarding', () => {
  beforeEach(() => {
    useCloud.setState(useCloud.getInitialState(), true);
    useStore.setState({ dialog: null });
    useCloud.setState({
      mode: 'cloud', isLocal: false, authorised: true,
      session: { access_token: 'fixture-token', user: { email: 'demo@example.com' } } as Session,
      machines: [], machinesLoaded: true, selectedMachineId: null, error: null,
      bootstrap: vi.fn(async () => {}), signOut: vi.fn(async () => {}),
    });
    Object.defineProperty(navigator, 'maxTouchPoints', { configurable: true, value: 0 });
  });

  afterEach(() => {
    for (const key of ['__SOLU_TAURI__', '__SOLU_BACKEND__', '__TAURI_INTERNALS__']) {
      delete (window as unknown as Record<string, unknown>)[key];
    }
    useStore.setState({ dialog: null });
    useCloud.setState(useCloud.getInitialState(), true);
    vi.restoreAllMocks();
  });

  it('recognises an installed desktop app instead of requesting installation', () => {
    desktop(vi.fn());
    render(<CloudGate><div>app</div></CloudGate>);
    expect(screen.queryByText(/three steps to your first team/i)).toBeNull();
    expect(screen.getByText(/desktop app is already installed/i)).toBeInTheDocument();
  });

  it('does not restart automatically or dispatch to the unmounted pairing panel', async () => {
    const invoke = vi.fn().mockResolvedValue(undefined);
    desktop(invoke);
    const pair = vi.fn();
    window.addEventListener('solu:open-pair-panel', pair);
    try {
      render(<CloudGate><div>app</div></CloudGate>);
      openHost();
      expect(screen.getByText(/signing in alone does not link the machine/i)).toHaveTextContent('demo@example.com');
      expect(invoke).not.toHaveBeenCalled();
      fireEvent.click(screen.getByRole('button', { name: /run projects on this computer/i }));
      expect(screen.getByRole('dialog')).toBeInTheDocument();
      expect(invoke).not.toHaveBeenCalled();
      fireEvent.click(screen.getByRole('button', { name: 'Restart in local mode' }));
      await waitFor(() => expect(invoke).toHaveBeenCalledExactlyOnceWith('desktop_set_launch_mode', { mode: 'local' }));
      expect(pair).not.toHaveBeenCalled();
    } finally {
      window.removeEventListener('solu:open-pair-panel', pair);
    }
  });

  it('cancel leaves launch mode and login unchanged', async () => {
    const invoke = vi.fn();
    desktop(invoke);
    render(<CloudGate><div>app</div></CloudGate>);
    openHost();
    fireEvent.click(screen.getByRole('button', { name: /run projects on this computer/i }));
    await act(async () => {
      fireEvent.click(screen.getAllByRole('button', { name: 'Cancel' }).at(-1)!);
    });
    expect(invoke).not.toHaveBeenCalled();
    expect(useCloud.getState().session?.user.email).toBe('demo@example.com');
  });

  it('blocks duplicate requests while the restart confirmation is open', async () => {
    const invoke = vi.fn().mockResolvedValue(undefined);
    desktop(invoke);
    render(<CloudGate><div>app</div></CloudGate>);
    openHost();
    fireEvent.click(screen.getByRole('button', { name: /run projects on this computer/i }));
    expect(screen.getByRole('button', { name: 'Restarting…' })).toBeDisabled();
    fireEvent.click(screen.getByRole('button', { name: 'Restarting…' }));
    expect(screen.getAllByRole('dialog')).toHaveLength(1);
    fireEvent.click(screen.getByRole('button', { name: 'Restart in local mode' }));
    await waitFor(() => expect(invoke).toHaveBeenCalledTimes(1));
  });

  it('shows tray-menu recovery when an older shell denies the switch', async () => {
    const invoke = vi.fn().mockRejectedValue(new Error('not allowed'));
    desktop(invoke);
    render(<CloudGate><div>app</div></CloudGate>);
    openHost();
    fireEvent.click(screen.getByRole('button', { name: /run projects on this computer/i }));
    fireEvent.click(screen.getByRole('button', { name: 'Restart in local mode' }));
    await waitFor(() => expect(screen.getByRole('alert')).toHaveTextContent(/tray icon/i));
    expect(screen.getByRole('button', { name: /run projects on this computer/i })).toBeEnabled();
  });

  it('recognises a desktop marker without IPC and gives a usable tray fallback', () => {
    desktop();
    render(<CloudGate><div>app</div></CloudGate>);
    openHost();
    expect(screen.getByText(/tray icon/i)).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: /run projects on this computer/i })).toBeNull();
    expect(screen.queryByText(/three steps to your first team/i)).toBeNull();
  });

  it('retains installer guidance in an ordinary browser', () => {
    render(<CloudGate><div>app</div></CloudGate>);
    expect(screen.getByRole('heading', { name: /you're in\. three steps to your first team\./i })).toBeInTheDocument();
    expect(screen.getByText(/open it and sign in with the same email/i)).toBeInTheDocument();
  });
});

describe('hostCommands', () => {
  it('uses the default cloud without extra arguments', () => {
    expect(hostCommands('https://soluterminal-cloud.onrender.com')).toEqual({
      installCmd: 'curl -fsSL https://github.com/soluterminal/soluterminal-releases/releases/latest/download/install.sh | sh',
      fallbackCmd: 'curl -fsSL https://soluterminal-cloud.onrender.com/install.sh | sh',
      claimCmd: 'solud --claim --relay wss://soluterminal-cloud.onrender.com/relay',
    });
  });
  it("offers this cloud's copy of the same script for networks that block GitHub", () => {
    expect(hostCommands('https://app.soluterminal.com').fallbackCmd).toBe(
      'curl -fsSL https://app.soluterminal.com/install.sh | sh -s -- --cloud https://app.soluterminal.com',
    );
  });
  it('points pairing at any other cloud with --cloud', () => {
    expect(hostCommands('https://app.soluterminal.com').installCmd).toBe(
      'curl -fsSL https://github.com/soluterminal/soluterminal-releases/releases/latest/download/install.sh | sh -s -- --cloud https://app.soluterminal.com',
    );
  });
});
