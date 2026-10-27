import { cleanup, fireEvent, render, screen } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { useStore } from '../store';
import type { UpdateStatus } from '../types';
import { VersionPopover } from './VersionPopover';
import { api } from '../api';

const anchors: HTMLElement[] = [];
const realApplyUpdate = useStore.getState().applyUpdate;
const realApplyAllUpdates = useStore.getState().applyAllUpdates;

function renderPopover(status: UpdateStatus | null = null) {
  const anchor = document.createElement('button');
  document.body.appendChild(anchor);
  anchors.push(anchor);
  const onClose = vi.fn();
  render(
    <VersionPopover
      anchor={anchor}
      status={status}
      currentFallback="0.0.1"
      onClose={onClose}
    />,
  );
  return { onClose };
}

afterEach(() => {
  cleanup();
  anchors.splice(0).forEach((anchor) => anchor.remove());
  useStore.setState({
    deferredUpdate: null,
    updateApplyRequestInFlight: false,
    updateStatus: null,
    applyUpdate: realApplyUpdate,
    applyAllUpdates: realApplyAllUpdates,
  });
  vi.restoreAllMocks();
  delete (window as unknown as { __SOLU_TAURI__?: unknown }).__SOLU_TAURI__;
  delete (window as unknown as { __TAURI_INTERNALS__?: unknown }).__TAURI_INTERNALS__;
});

describe('VersionPopover', () => {
  it('notes panes on older agent builds only when there are any', () => {
    const realPanes = useStore.getState().panes;
    try {
      useStore.setState({ panes: {} });
      renderPopover();
      expect(screen.queryByText(/on older builds/)).toBeNull();
      cleanup();
      useStore.setState({ panes: {
        a: { id: 'a', agentStale: true },
        b: { id: 'b', agentStale: true },
        c: { id: 'c' },
      } as never });
      renderPopover();
      expect(screen.getByText('2 panes on older builds · update when restarted')).toBeInTheDocument();
    } finally {
      useStore.setState({ panes: realPanes });
    }
  });

  it('closes on outside click but not inside click', () => {
    const { onClose } = renderPopover();
    fireEvent.click(screen.getByRole('dialog', { name: 'Update status' }));
    expect(onClose).not.toHaveBeenCalled();
    fireEvent.click(document.body);
    expect(onClose).toHaveBeenCalledOnce();
  });

  it('closes on Escape and stops propagation', () => {
    const { onClose } = renderPopover();
    const downstream = vi.fn();
    document.addEventListener('keydown', downstream);
    fireEvent.keyDown(document.body, { key: 'Escape' });
    document.removeEventListener('keydown', downstream);
    expect(onClose).toHaveBeenCalledOnce();
    expect(downstream).not.toHaveBeenCalled();
  });

  it('does not exempt app dialog backdrops', () => {
    const { onClose } = renderPopover();
    const backdrop = document.createElement('div');
    backdrop.className = 'app-dialog-backdrop';
    const child = document.createElement('button');
    backdrop.appendChild(child);
    document.body.appendChild(backdrop);
    fireEvent.click(child);
    expect(onClose).toHaveBeenCalledOnce();
    backdrop.remove();
  });

  it.each([
    ['focus', 'Update now — interrupt Focus', 'Focus pane focus-7 finishes its turn'],
    ['unknown', 'Update now — interrupt active work', 'Applies when active work finishes'],
  ] as const)('uses cause-honest queued copy and force only on the %s override', (cause, label, detail) => {
    const applyUpdate = vi.fn().mockResolvedValue(undefined);
    useStore.setState({
      deferredUpdate: {
        cause,
        since: Date.now(),
        auto: false,
        paneId: cause === 'focus' ? 'focus-7' : undefined,
      },
      applyUpdate: applyUpdate as never,
    });
    renderPopover({ current: '1.0.0', latest: '2.0.0', available: true });

    expect(screen.getByText(label)).toBeInTheDocument();
    expect(screen.getByText(new RegExp(detail))).toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: new RegExp(label) }));
    expect(applyUpdate).toHaveBeenCalledWith({ force: true });
  });

  it('uses cause-honest queued copy and force only on the workflow override', () => {
    const applyUpdate = vi.fn().mockResolvedValue(undefined);
    useStore.setState({
      deferredUpdate: {
        cause: 'workflow', since: Date.now(), auto: false, workflowInflight: 2,
      },
      applyUpdate: applyUpdate as never,
    });
    renderPopover({
      current: '1.0.0', latest: '2.0.0', available: true,
      workflowInflight: 2, workflowDeferState: 'backoff', workflowDeferAttempts: 1,
      workflowNextRetryMs: Date.now() + 120_000,
    });

    expect(screen.getByText(/Update deferred · automatic retry in 2m/)).toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: /Update now — force restart/ }));
    expect(applyUpdate).not.toHaveBeenCalled();
    expect(screen.getByText('Their work will be lost.')).toBeInTheDocument();
  });

  // Behavioral replacement: the old workflow one-click-force row is covered
  // separately because forcing active invocations now requires confirmation.
  it('shows invocation count and deadline while the workflow drain is active', () => {
    useStore.setState({
      deferredUpdate: {
        cause: 'workflow',
        since: Date.now(),
        auto: false,
        workflowInflight: 2,
        workflowDeferDeadlineMs: Date.now() + 6 * 60_000,
      },
    });
    renderPopover({
      current: '1.0.0',
      latest: '2.0.0',
      available: true,
      workflowInflight: 2,
      workflowDeferState: 'draining',
      workflowDeferDeadlineMs: Date.now() + 6 * 60_000,
    });

    expect(screen.getByText(/2 workflow invocations draining/)).toBeInTheDocument();
  });

  it('derives workflow queued detail and force confirmation from status alone', () => {
    const now = Date.now();
    const applyUpdate = vi.fn().mockResolvedValue(undefined);
    useStore.setState({ deferredUpdate: null, applyUpdate: applyUpdate as never });
    renderPopover({
      current: '0.0.1048', latest: '0.0.1049', available: true,
      applying: true,
      workflowDeferred: true, workflowDeferState: 'draining',
      workflowInflight: 2, workflowDeferDeadlineMs: now + 6 * 60_000,
    });

    expect(screen.getByText(/2 workflow invocations draining · deadline in 6m/)).toBeInTheDocument();
    const force = screen.getByRole('button', { name: /Update now — force restart/ });
    fireEvent.click(force);
    expect(applyUpdate).not.toHaveBeenCalled();
    expect(screen.getByText(/This will interrupt 2 running workflow invocations/)).toBeInTheDocument();
    expect(screen.getByText('Their work will be lost.')).toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: 'Confirm force update' }));
    expect(applyUpdate).toHaveBeenCalledWith({ force: true });
  });

  it('surfaces a typed workflow retry before status reports backoff', () => {
    useStore.setState({
      deferredUpdate: {
        cause: 'workflow', since: Date.now(), auto: false,
        workflowInflight: 2, workflowNextRetryMs: Date.now() + 120_000,
      },
    });
    renderPopover({
      current: '1.0.0', latest: '2.0.0', available: true,
      workflowDeferred: true, workflowDeferState: 'draining', workflowInflight: 2,
    });

    expect(screen.getByText(/Update deferred · automatic retry in 2m/)).toBeInTheDocument();
  });

  it('lets authoritative workflow drain state override a stale local Focus hold', () => {
    useStore.setState({
      deferredUpdate: {
        cause: 'focus', since: Date.now(), auto: false, paneId: 'stale-focus',
      },
    });
    renderPopover({
      current: '0.0.1048', latest: '0.0.1049', available: true,
      workflowDeferred: true, workflowDeferState: 'draining',
      workflowInflight: 6, workflowDeferDeadlineMs: Date.now() + 6 * 60_000,
    });

    expect(screen.queryByText(/Focus pane stale-focus/)).toBeNull();
    expect(screen.getByText(/6 workflow invocations draining/)).toBeInTheDocument();
    expect(screen.getByRole('button', { name: /Update now — force restart/ })).toBeInTheDocument();
  });

  it('requires an explicit loss confirmation before forcing through workflow invocations', () => {
    const applyUpdate = vi.fn().mockResolvedValue(undefined);
    useStore.setState({
      deferredUpdate: {
        cause: 'workflow', since: Date.now(), auto: false, workflowInflight: 2,
      },
      applyUpdate: applyUpdate as never,
    });
    renderPopover({
      current: '1.0.0', latest: '2.0.0', available: true,
      workflowInflight: 2, workflowDeferState: 'backoff', workflowDeferAttempts: 1,
      workflowNextRetryMs: Date.now() + 120_000,
    });

    fireEvent.click(screen.getByRole('button', { name: /Update now — force restart/ }));
    expect(applyUpdate).not.toHaveBeenCalled();
    expect(screen.getByText(/This will interrupt 2 running workflow invocations/)).toBeInTheDocument();
    expect(screen.getByText('Their work will be lost.')).toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: 'Confirm force update' }));
    expect(applyUpdate).toHaveBeenCalledWith({ force: true });
  });

  it('keeps daemon launch failure recovery visible when no update is available', async () => {
    const staged = 'C:\\Users\\x\\AppData\\Local\\soluterminal\\shell-update\\setup.exe';
    const reveal = vi.spyOn(api, 'revealShellInstaller').mockResolvedValue({ ok: true, path: staged });
    renderPopover({
      current: '1.0.0',
      available: false,
      shellLaunch: { state: 'launch-failed', stagedPath: staged, error: 'not observed' },
    });

    expect(screen.getByText(/Installer downloaded but failed to open/)).toBeInTheDocument();
    expect(screen.getByText(staged)).toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: 'Show in folder' }));
    expect(reveal).toHaveBeenCalledOnce();
  });

  it('does not render an unusable recovery panel for an empty native staged path', async () => {
    const invoke = vi.fn(async (command: string) => {
      if (command === 'browser_pane_ping') return '0.0.1';
      if (command === 'apply_shell_update') {
        return { launched: false, path: '', error: 'staged path missing' };
      }
      return undefined;
    });
    Object.defineProperty(window, '__SOLU_TAURI__', { value: true, configurable: true });
    Object.defineProperty(window, '__TAURI_INTERNALS__', {
      value: { invoke },
      configurable: true,
    });
    useStore.setState({
      updateStatus: {
        current: '0.0.1', available: false, shellLatest: '0.0.2', shellAvailable: false,
      },
      applyAllUpdates: realApplyAllUpdates,
    });
    renderPopover({
      current: '0.0.1',
      available: false,
      shellCurrent: '0.0.1',
      shellLatest: '0.0.2',
      shellAvailable: false,
    });

    const update = await screen.findByRole('button', { name: 'Update installer' });
    fireEvent.click(update);
    await vi.waitFor(() => expect(invoke).toHaveBeenCalledWith('apply_shell_update', { forceDowngrade: false }));
    // The manual fallback is the public download page in the system browser,
    // never the release repository.
    await vi.waitFor(() => expect(invoke).toHaveBeenCalledWith(
      'plugin:opener|open_url', { url: 'https://soluterminal.com/download/' },
    ));
    expect(JSON.stringify(invoke.mock.calls)).not.toContain('github.com');
    expect(screen.queryByRole('button', { name: 'Show in folder' })).not.toBeInTheDocument();
  });

  it('shows the 0.0.932 to 0.0.916 refusal without opening a manual fallback', async () => {
    const invoke = vi.fn(async (command: string) => {
      if (command === 'browser_pane_ping') return '0.0.900';
      if (command === 'apply_shell_update') {
        return {
          launched: false,
          path: 'C:\\setup-0.0.916.exe',
          error: 'installed daemon 0.0.932 is newer than bundled daemon 0.0.916',
          downgradeRefused: true,
          installedVersion: '0.0.932',
          installerVersion: '0.0.916',
        };
      }
      return undefined;
    });
    Object.defineProperty(window, '__SOLU_TAURI__', { value: true, configurable: true });
    Object.defineProperty(window, '__TAURI_INTERNALS__', {
      value: { invoke },
      configurable: true,
    });
    // Keep this as native-refusal coverage. The separate daemon-status test
    // below pins the earlier JS guard using the true 0.0.932 current version.
    useStore.setState({
      updateStatus: {
        current: '0.0.900', available: false, shellLatest: '0.0.916', shellAvailable: false,
      },
      applyAllUpdates: realApplyAllUpdates,
    });
    renderPopover({
      current: '0.0.932',
      available: false,
      shellCurrent: '0.0.900',
      shellLatest: '0.0.916',
      shellAvailable: false,
    });

    fireEvent.click(await screen.findByRole('button', { name: 'Update installer' }));
    expect(await screen.findByText(/installed daemon v0\.0\.932 is newer than bundled daemon v0\.0\.916/)).toBeInTheDocument();
    expect(invoke).toHaveBeenCalledWith('apply_shell_update', { forceDowngrade: false });
    expect(invoke.mock.calls.some(([command]) => command === 'open_external_window')).toBe(false);
    expect(screen.queryByRole('button', { name: 'Show in folder' })).not.toBeInTheDocument();
  });

  it('uses daemon status to hold a shell-only downgrade and suppresses stale launch recovery', async () => {
    const invoke = vi.fn(async (command: string) => {
      if (command === 'browser_pane_ping') return '0.0.900';
      if (command === 'apply_shell_update') throw new Error('native admission must not be reached');
      return undefined;
    });
    Object.defineProperty(window, '__SOLU_TAURI__', { value: true, configurable: true });
    Object.defineProperty(window, '__TAURI_INTERNALS__', {
      value: { invoke },
      configurable: true,
    });
    const status: UpdateStatus = {
      current: '0.0.932',
      available: false,
      shellCurrent: '0.0.900',
      shellLatest: '0.0.916',
      shellAvailable: false,
      shellLaunch: {
        state: 'launch-failed',
        stagedPath: 'C:\\stale-setup-0.0.916.exe',
        error: 'previous launch failed',
      },
    };
    useStore.setState({ updateStatus: status, applyAllUpdates: realApplyAllUpdates });
    renderPopover(status);

    fireEvent.click(await screen.findByRole('button', { name: 'Update installer' }));
    expect(await screen.findByText(/installed daemon v0\.0\.932 is newer than bundled daemon v0\.0\.916/)).toBeInTheDocument();
    expect(invoke.mock.calls.some(([command]) => command === 'apply_shell_update')).toBe(false);
    expect(invoke.mock.calls.some(([command]) => command === 'open_external_window')).toBe(false);
    expect(screen.queryByRole('button', { name: 'Show in folder' })).not.toBeInTheDocument();
  });
});
