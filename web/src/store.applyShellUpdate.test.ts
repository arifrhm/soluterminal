import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { useStore } from './store';
import { useCloud } from './cloud';
import { api, ApiError, SHELL_APPLY_TIMEOUT_MS } from './api';

const realApplyUpdate = useStore.getState().applyUpdate;
const realApplyShellUpdate = useStore.getState().applyShellUpdate;
const realApplyAllUpdates = useStore.getState().applyAllUpdates;

function setLaunchMode(mode: 'cloud' | 'local' | undefined) {
  if (mode === undefined) {
    delete (window as unknown as { __SOLU_BACKEND__?: unknown }).__SOLU_BACKEND__;
    return;
  }
  Object.defineProperty(window, '__SOLU_BACKEND__', {
    value: { launchMode: mode },
    configurable: true,
  });
}

// The daemon-driven applyShellUpdate hits /api/update/shell/apply on whatever
// daemon we're talking to. In CLOUD mode that daemon is REMOTE, so applying
// would download + launch the installer on the remote machine, not this one
// (the "stuck at Launching installer…, nothing came up" trap). Guard it.
describe('applyShellUpdate cloud guard', () => {
  beforeEach(() => {
    useStore.setState({
      updateStatus: { shellAvailable: true, shellLatest: '0.0.839' } as never,
      toasts: [],
    });
  });
  afterEach(() => {
    useCloud.setState({ mode: 'detecting', isLocal: false });
    setLaunchMode(undefined);
    useStore.setState({
      updateStatus: null,
      applyUpdate: realApplyUpdate,
      applyShellUpdate: realApplyShellUpdate,
      applyAllUpdates: realApplyAllUpdates,
    });
    vi.restoreAllMocks();
  });

  it('does NOT hit the daemon shell-apply endpoint in cloud mode', async () => {
    useCloud.setState({ mode: 'cloud' });
    const spy = vi.spyOn(api, 'applyShellUpdate').mockResolvedValue(undefined as never);
    await useStore.getState().applyShellUpdate();
    expect(spy).not.toHaveBeenCalled();
  });

  it('does NOT hit the daemon from the desktop app in cloud launch mode', async () => {
    setLaunchMode('cloud');
    const spy = vi.spyOn(api, 'applyShellUpdate').mockResolvedValue(undefined as never);
    await useStore.getState().applyShellUpdate();
    expect(spy).not.toHaveBeenCalled();
  });

  // A local daemon built with sign-in serves /api/config, so useCloud reports
  // mode 'cloud' with isLocal. That daemon is on this computer (2026-10-28: the
  // guard on mode alone kept a team machine's window on the public installer).
  it('applies via the local daemon when it serves the sign-in config', async () => {
    useCloud.setState({ mode: 'cloud', isLocal: true });
    const spy = vi.spyOn(api, 'applyShellUpdate').mockResolvedValue(undefined as never);
    await useStore.getState().applyShellUpdate();
    expect(spy).toHaveBeenCalledWith(false);
  });

  it('still applies via the daemon in local mode', async () => {
    useCloud.setState({ mode: 'local' });
    const spy = vi.spyOn(api, 'applyShellUpdate').mockResolvedValue(undefined as never);
    await useStore.getState().applyShellUpdate();
    expect(spy).toHaveBeenCalledWith(false);
  });

  it('gives the daemon minutes, not the default 30 s, to download and launch the installer', async () => {
    vi.useFakeTimers();
    try {
      let signal: AbortSignal | undefined;
      let answer!: (response: Response) => void;
      const fetchSpy = vi.spyOn(globalThis, 'fetch').mockImplementation((_url, init) => {
        signal = (init as RequestInit | undefined)?.signal ?? undefined;
        return new Promise<Response>((resolve) => { answer = resolve; });
      });
      const pending = api.applyShellUpdate(false);
      await vi.advanceTimersByTimeAsync(5 * 60_000);
      expect(fetchSpy).toHaveBeenCalledOnce();
      expect(String(fetchSpy.mock.calls[0][0])).toContain('/api/update/apply-shell');
      expect(signal?.aborted).toBe(false);
      answer(new Response(JSON.stringify({ applying: true }), { status: 200 }));
      await expect(pending).resolves.toEqual({ applying: true });
      await vi.advanceTimersByTimeAsync(SHELL_APPLY_TIMEOUT_MS);
      expect(signal?.aborted).toBe(false);
    } finally {
      vi.useRealTimers();
    }
  });

  it('passes the daemon downgrade override only when explicitly requested', async () => {
    useCloud.setState({ mode: 'local' });
    const spy = vi.spyOn(api, 'applyShellUpdate').mockResolvedValue(undefined as never);
    await useStore.getState().applyShellUpdate({ forceDowngrade: true });
    expect(spy).toHaveBeenCalledWith(true);
  });

  it('surfaces the structured downgrade refusal without retrying with force', async () => {
    useCloud.setState({ mode: 'local' });
    const refusal = new ApiError(409, 'refused', {
      code: 'shell_daemon_downgrade_refused',
      installedVersion: '0.0.932',
      installerVersion: '0.0.916',
      message: 'installed 0.0.932 is newer than bundled 0.0.916; use ?force=1 explicitly',
    });
    const apply = vi.spyOn(api, 'applyShellUpdate').mockRejectedValue(refusal);
    vi.spyOn(api, 'getUpdateStatus').mockResolvedValue({
      current: '0.0.932', available: false, shellAvailable: true, shellApplying: false,
    });

    await expect(useStore.getState().applyShellUpdate()).rejects.toBe(refusal);
    expect(apply).toHaveBeenCalledTimes(1);
    expect(apply).toHaveBeenCalledWith(false);
    expect(useStore.getState().toasts.at(-1)?.message).toContain('0.0.932');
    expect(useStore.getState().toasts.at(-1)?.message).toContain('0.0.916');
  });

  it('surfaces a staged launch failure instead of calling it a download failure', async () => {
    useCloud.setState({ mode: 'local' });
    const staged = 'C:\\Users\\x\\AppData\\Local\\soluterminal\\shell-update\\setup.exe';
    vi.spyOn(api, 'applyShellUpdate').mockRejectedValue(new Error('not observed'));
    const statusSpy = vi.spyOn(api, 'getUpdateStatus').mockResolvedValue({
      current: '0.0.1',
      available: false,
      shellAvailable: true,
      shellApplying: false,
      shellLaunch: { state: 'launch-failed', stagedPath: staged, error: 'not observed' },
    });

    await expect(useStore.getState().applyShellUpdate()).rejects.toThrow('not observed');

    expect(statusSpy).toHaveBeenCalled();
    expect(useStore.getState().updateStatus?.shellApplying).toBe(false);
    const toast = useStore.getState().toasts.at(-1);
    expect(toast?.message).toContain("Installer downloaded but didn't open — run it manually");
    expect(toast?.message).toContain(staged);
    expect(toast?.message).not.toContain('Installer download failed');
    expect(toast?.ttlMs).toBe(15000);
  });

  it('keeps the download-failed toast for a pre-launch failure', async () => {
    useCloud.setState({ mode: 'local' });
    vi.spyOn(api, 'applyShellUpdate').mockRejectedValue(new Error('sha256 mismatch'));
    vi.spyOn(api, 'getUpdateStatus').mockResolvedValue({
      current: '0.0.1', available: false, shellAvailable: true, shellApplying: false,
    });

    await expect(useStore.getState().applyShellUpdate()).rejects.toThrow('sha256 mismatch');
    expect(useStore.getState().toasts.at(-1)?.message).toContain('Installer download failed: sha256 mismatch');
  });
});

describe('applyAllUpdates monotonic ordering', () => {
  beforeEach(() => {
    useCloud.setState({ mode: 'local' });
    setLaunchMode('local');
    useStore.setState({ toasts: [], applyAllUpdates: realApplyAllUpdates });
  });

  afterEach(() => {
    setLaunchMode(undefined);
    useStore.setState({
      updateStatus: null,
      applyUpdate: realApplyUpdate,
      applyShellUpdate: realApplyShellUpdate,
      applyAllUpdates: realApplyAllUpdates,
    });
  });

  it('never updates to 0.0.932 and then launches the carried 0.0.916 installer', async () => {
    const order: string[] = [];
    const applyDaemon = vi.fn(async () => { order.push('daemon-0.0.932'); });
    const applyShell = vi.fn(async () => { order.push('shell-0.0.916'); });
    useStore.setState({
      updateStatus: {
        current: '0.0.900', latest: '0.0.932', available: true,
        shellLatest: '0.0.916', shellAvailable: true,
      },
      applyUpdate: applyDaemon as never,
    });

    await useStore.getState().applyAllUpdates({ applyShell });
    expect(order).toEqual(['shell-0.0.916']);
    expect(applyDaemon).not.toHaveBeenCalled();
  });

  it('holds an installer older than the current daemon and applies only the safe daemon leg', async () => {
    const order: string[] = [];
    const applyDaemon = vi.fn(async () => { order.push('daemon'); });
    const applyShell = vi.fn(async () => { order.push('shell'); });
    useStore.setState({
      updateStatus: {
        current: '0.0.932', latest: '0.0.940', available: true,
        shellLatest: '0.0.916', shellAvailable: true,
      },
      applyUpdate: applyDaemon as never,
    });

    const result = await useStore.getState().applyAllUpdates({ applyShell });
    expect(order).toEqual(['daemon']);
    expect(applyShell).not.toHaveBeenCalled();
    expect(result).toEqual({
      downgradeRefused: true,
      installedVersion: '0.0.932',
      installerVersion: '0.0.916',
    });
    expect(useStore.getState().toasts.at(-1)?.message).toContain('will not be forced');
  });

  it('launches a monotonic installer before any remaining daemon upgrade', async () => {
    const applyDaemon = vi.fn().mockResolvedValue(undefined);
    const applyShell = vi.fn().mockResolvedValue(undefined);
    useStore.setState({
      updateStatus: {
        current: '0.0.916', latest: '0.0.940', available: true,
        shellLatest: '0.0.932', shellAvailable: true,
      },
      applyUpdate: applyDaemon as never,
    });

    await useStore.getState().applyAllUpdates({ applyShell });
    expect(applyShell).toHaveBeenCalledOnce();
    expect(applyDaemon).not.toHaveBeenCalled();
  });

  it('uses an explicitly client-detected native shell update without trusting the callback alone', async () => {
    const applyDaemon = vi.fn().mockResolvedValue(undefined);
    const applyShell = vi.fn().mockResolvedValue(undefined);
    useStore.setState({
      updateStatus: {
        current: '0.0.900', latest: '0.0.932', available: true,
        shellLatest: '0.0.916', shellAvailable: false,
      },
      applyUpdate: applyDaemon as never,
    });

    await useStore.getState().applyAllUpdates({ applyShell, shellActionable: true });
    expect(applyShell).toHaveBeenCalledOnce();
    expect(applyDaemon).not.toHaveBeenCalled();
  });

  it('applies a daemon-only update without relaunching a same-version shell installer', async () => {
    const applyDaemon = vi.fn().mockResolvedValue(undefined);
    const applyShell = vi.fn().mockResolvedValue(undefined);
    useStore.setState({
      updateStatus: {
        current: '0.0.930', latest: '0.0.932', available: true,
        shellLatest: '0.0.930', shellAvailable: false,
      },
      applyUpdate: applyDaemon as never,
    });

    await useStore.getState().applyAllUpdates({ applyShell });
    expect(applyShell).not.toHaveBeenCalled();
    expect(applyDaemon).toHaveBeenCalledOnce();
  });

  it('delegates cloud shell admission to the local native updater', async () => {
    setLaunchMode('cloud');
    const applyDaemon = vi.fn().mockResolvedValue(undefined);
    const applyShell = vi.fn().mockResolvedValue(undefined);
    useStore.setState({
      updateStatus: {
        current: '0.0.932', latest: '0.0.932', available: false,
        shellLatest: '0.0.916', shellAvailable: false,
      },
      applyUpdate: applyDaemon as never,
    });

    await useStore.getState().applyAllUpdates({ applyShell, shellActionable: true });

    expect(applyShell).toHaveBeenCalledOnce();
    expect(applyDaemon).not.toHaveBeenCalled();
    expect(useStore.getState().toasts).toEqual([]);
  });

  it('never applies the remote daemon leg from cloud launch mode', async () => {
    setLaunchMode('cloud');
    const applyDaemon = vi.fn().mockResolvedValue(undefined);
    const applyShell = vi.fn().mockResolvedValue(undefined);
    useStore.setState({
      updateStatus: {
        current: '0.0.932', latest: '0.0.940', available: true,
        shellLatest: undefined, shellAvailable: false,
      },
      applyUpdate: applyDaemon as never,
    });

    await useStore.getState().applyAllUpdates({ applyShell, shellActionable: true });

    expect(applyShell).not.toHaveBeenCalled();
    expect(applyDaemon).not.toHaveBeenCalled();
  });
});
