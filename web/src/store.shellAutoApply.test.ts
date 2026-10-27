import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { api } from './api';
import { useCloud } from './cloud';
import {
  noteUserActivity,
  SHELL_AUTO_COUNTDOWN_MS,
  SHELL_AUTO_HOLD_MS,
  SHELL_AUTO_QUIET_MS,
  useStore,
} from './store';
import type { PaneInfo, UpdateStatus } from './types';

const native = vi.hoisted(() => ({
  desktop: true,
  installed: '1.0.0' as string | null,
  outcome: { ok: true } as { ok: boolean; staged?: string; error?: string },
  apply: vi.fn(),
  exit: vi.fn(),
}));
const drafts = vi.hoisted(() => ({ present: false }));

vi.mock('./tauri-browser', () => ({
  isDesktopApp: () => native.desktop,
  getShellVersion: () => Promise.resolve(native.installed),
  applyShellUpdateSilentNative: () => {
    native.apply();
    return Promise.resolve(native.outcome);
  },
  exitForUpdateNative: () => {
    native.exit();
    return Promise.resolve(true);
  },
}));

vi.mock('./native/useNativePane', () => ({
  hasAnyUnsentDraft: () => drafts.present,
}));

const pane = (over: Partial<PaneInfo> & { id: string }): PaneInfo => over as PaneInfo;
let versionSerial = 0;
let version = '';

function update(over: Partial<UpdateStatus> = {}): UpdateStatus {
  return {
    current: '1.0.0',
    available: false,
    shellLatest: version,
    shellCarriesLatestDaemon: true,
    ...over,
  };
}

async function becomeQuiet(): Promise<void> {
  await vi.advanceTimersByTimeAsync(SHELL_AUTO_QUIET_MS + 5);
}

async function arm(): Promise<void> {
  useStore.getState().maybeAutoApplyShellUpdate();
  await vi.advanceTimersByTimeAsync(0);
  expect(useStore.getState().shellAutoRestart?.version).toBe(version);
}

describe('desktop shell auto-apply', () => {
  beforeEach(() => {
    vi.useFakeTimers();
    version = `2.0.${++versionSerial}`;
    // Module-level activity time intentionally survives store resets. Keep fake
    // clocks monotonic across cases so a prior test cannot appear "in future".
    vi.setSystemTime(1_900_000_000_000 + versionSerial * 24 * 60 * 60 * 1000);
    native.desktop = true;
    native.installed = '1.0.0';
    native.outcome = { ok: true };
    native.apply.mockClear();
    native.exit.mockClear();
    drafts.present = false;
    useCloud.setState({ mode: 'local' });
    noteUserActivity();
    useStore.setState({
      panes: {},
      deferredUpdate: null,
      updateSettings: { autoApply: 'off', shellAutoApply: 'when-idle' },
      updateStatus: update(),
      shellAutoRestart: null,
      toasts: [],
    });
    vi.spyOn(api, 'getUpdateStatus').mockImplementation(async () => update());
  });

  afterEach(async () => {
    noteUserActivity();
    await vi.advanceTimersByTimeAsync(1000);
    useStore.setState({ panes: {}, updateStatus: null, shellAutoRestart: null });
    localStorage.clear();
    vi.restoreAllMocks();
    vi.useRealTimers();
  });

  it('arms when quiet, silently installs at the deadline, and exits for relaunch', async () => {
    await becomeQuiet();
    await arm();
    expect(native.apply).not.toHaveBeenCalled();

    await vi.advanceTimersByTimeAsync(SHELL_AUTO_COUNTDOWN_MS);
    expect(native.apply).toHaveBeenCalledTimes(1);
    expect(useStore.getState().updateStatus?.shellApplying).toBe(true);
    await vi.advanceTimersByTimeAsync(1500);
    expect(native.exit).toHaveBeenCalledTimes(1);
  });

  it('keeps manual behavior when the preference is off', async () => {
    useStore.setState({ updateSettings: { autoApply: 'off', shellAutoApply: 'off' } });
    await becomeQuiet();
    useStore.getState().maybeAutoApplyShellUpdate();
    await vi.advanceTimersByTimeAsync(SHELL_AUTO_COUNTDOWN_MS + 1000);
    expect(useStore.getState().shellAutoRestart).toBeNull();
    expect(native.apply).not.toHaveBeenCalled();
  });

  it.each([
    ['native pane mid-turn', () => useStore.setState({ panes: { p: pane({ id: 'p', kind: 'native', agentMidTurn: true }) } })],
    ['pending approval', () => useStore.setState({ panes: { p: pane({ id: 'p', pendingApproval: { requestId: 'r', toolName: 'Bash' } }) } })],
    ['unanswered prompt', () => useStore.setState({ panes: { p: pane({ id: 'p', attention: 'needs-user' }) } })],
    ['workflow invocation', () => useStore.setState({ updateStatus: update({ workflowInflight: 1 }) })],
    ['workflow drain', () => useStore.setState({ updateStatus: update({ workflowDeferred: true }) })],
    ['unsent draft', () => { drafts.present = true; }],
  ])('does not arm with the %s hold', async (_name, hold) => {
    await becomeQuiet();
    hold();
    useStore.getState().maybeAutoApplyShellUpdate();
    await vi.advanceTimersByTimeAsync(0);
    expect(useStore.getState().shellAutoRestart).toBeNull();
  });

  it('does not arm after recent keyboard or pointer activity', async () => {
    await becomeQuiet();
    noteUserActivity();
    useStore.getState().maybeAutoApplyShellUpdate();
    await vi.advanceTimersByTimeAsync(0);
    expect(useStore.getState().shellAutoRestart).toBeNull();
  });

  it('activity during countdown disarms and a later status refresh re-arms after fresh quiet', async () => {
    await becomeQuiet();
    await arm();
    noteUserActivity();
    await vi.advanceTimersByTimeAsync(1000);
    expect(useStore.getState().shellAutoRestart).toBeNull();

    await becomeQuiet();
    await useStore.getState().refreshUpdateStatus();
    await vi.advanceTimersByTimeAsync(0);
    expect(useStore.getState().shellAutoRestart?.version).toBe(version);
  });

  it('Hold 1 hour persists the per-version deferral until it expires', async () => {
    await becomeQuiet();
    await arm();
    useStore.getState().postponeShellAutoRestart();
    expect(useStore.getState().shellAutoRestart).toBeNull();

    await vi.advanceTimersByTimeAsync(SHELL_AUTO_HOLD_MS - 1);
    useStore.getState().maybeAutoApplyShellUpdate();
    await vi.advanceTimersByTimeAsync(0);
    expect(useStore.getState().shellAutoRestart).toBeNull();

    await vi.advanceTimersByTimeAsync(1);
    await arm();
  });

  it('Restart now bypasses the remaining countdown', async () => {
    await becomeQuiet();
    await arm();
    await useStore.getState().restartShellNowForUpdate();
    expect(native.apply).toHaveBeenCalledTimes(1);
    expect(useStore.getState().shellAutoRestart).toBeNull();
  });

  it('old-shell failure never exits and the cooldown plus two-attempt cap prevent a loop', async () => {
    native.outcome = { ok: false, error: 'unknown command' };
    await becomeQuiet();
    await arm();
    await useStore.getState().restartShellNowForUpdate();
    expect(native.apply).toHaveBeenCalledTimes(1);
    expect(native.exit).not.toHaveBeenCalled();

    useStore.getState().maybeAutoApplyShellUpdate();
    await vi.advanceTimersByTimeAsync(0);
    expect(useStore.getState().shellAutoRestart).toBeNull();

    await vi.advanceTimersByTimeAsync(6 * 60 * 60 * 1000);
    await arm();
    await useStore.getState().restartShellNowForUpdate();
    expect(native.apply).toHaveBeenCalledTimes(2);

    await vi.advanceTimersByTimeAsync(6 * 60 * 60 * 1000);
    useStore.getState().maybeAutoApplyShellUpdate();
    await vi.advanceTimersByTimeAsync(0);
    expect(useStore.getState().shellAutoRestart).toBeNull();
    expect(native.apply).toHaveBeenCalledTimes(2);
  });

  it('never arms in cloud launch mode', async () => {
    useCloud.setState({ mode: 'cloud' });
    await becomeQuiet();
    useStore.getState().maybeAutoApplyShellUpdate();
    await vi.advanceTimersByTimeAsync(0);
    expect(useStore.getState().shellAutoRestart).toBeNull();
  });

  it('waits for a daemon-first update when the installer does not carry it', async () => {
    useStore.setState({
      updateStatus: update({
        current: '0.0.932', latest: '0.0.932', available: false,
        shellLatest: '0.0.916', shellAvailable: true, shellCarriesLatestDaemon: false,
      }),
    });
    await becomeQuiet();
    useStore.getState().maybeAutoApplyShellUpdate();
    await vi.advanceTimersByTimeAsync(0);
    expect(useStore.getState().shellAutoRestart).toBeNull();
    expect(native.apply).not.toHaveBeenCalled();
  });

  it('arms a monotonic shell-first update even when a daemon-only publish is newer', async () => {
    native.installed = '0.0.900';
    useStore.setState({
      updateStatus: update({
        current: '0.0.900', latest: '0.0.932', available: true,
        shellLatest: '0.0.916', shellAvailable: true, shellCarriesLatestDaemon: false,
      }),
    });
    await becomeQuiet();
    useStore.getState().maybeAutoApplyShellUpdate();
    await vi.advanceTimersByTimeAsync(0);
    expect(useStore.getState().shellAutoRestart?.version).toBe('0.0.916');
  });
});
