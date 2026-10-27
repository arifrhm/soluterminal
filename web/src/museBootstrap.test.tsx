import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest';

// --- Mocks ---------------------------------------------------------------
const refreshDaemonHealth = vi.fn();
vi.mock('./lib/useDaemonHealth', () => ({
  refreshDaemonHealth: () => refreshDaemonHealth(),
}));

const museRecheck = vi.fn();
vi.mock('./api', () => ({
  api: {
    museRecheck: () => museRecheck(),
    museRecheckOn: (backend: 'wsl' | 'win' | null) => museRecheck(backend),
  },
}));

// Router-mode toggle per test (single-backend by default).
let routerMode = false;
vi.mock('./dualBackend', () => ({
  isRouterMode: () => routerMode,
  activeBackendFromUrl: () => 'wsl' as const,
}));

// Keep the real (heavy) store out of the module graph; the scheduler only
// reads activeBackend from it (at schedule time, for the router-mode pin).
let storeActiveBackend: 'wsl' | 'win' | null = null;
vi.mock('./store', () => ({
  useStore: Object.assign(vi.fn(), {
    getState: () => ({
      panes: {}, toast: vi.fn(), createPane: vi.fn(), activeBackend: storeActiveBackend,
    }),
  }),
}));

import { scheduleMuseRecheck, MUSE_RECHECK_BACKOFF_MS } from './museBootstrap';

function status(over: Partial<{
  museChecked: boolean; museInstalled: boolean; museVersion: string; museAuthed: boolean;
}> = {}) {
  return { museChecked: true, museInstalled: false, museVersion: '', museAuthed: false, ...over };
}

// scheduleMuseRecheck closes the post-install blind window: without it the
// daemon's cached probe refreshes only on its 5-minute ticker, so a user who
// just installed/signed in saw no UI change until the next tick. These tests
// pin the backoff loop's contract (parallel of claudeBootstrap.test.tsx).
describe('scheduleMuseRecheck', () => {
  beforeEach(() => {
    vi.useFakeTimers();
    museRecheck.mockReset();
    refreshDaemonHealth.mockClear();
    routerMode = false;
    storeActiveBackend = null;
  });
  afterEach(() => {
    // Supersede any loop the test left running, THEN drop fake timers.
    scheduleMuseRecheck('install');
    vi.clearAllTimers();
    vi.useRealTimers();
  });

  it('polls on the backoff and stops once the install lands (authed too)', async () => {
    museRecheck
      .mockResolvedValueOnce(status()) // still not installed
      .mockResolvedValueOnce(status({ museInstalled: true, museAuthed: true }));
    scheduleMuseRecheck('install');

    expect(museRecheck).not.toHaveBeenCalled(); // nothing before the first delay
    await vi.advanceTimersByTimeAsync(MUSE_RECHECK_BACKOFF_MS[0]);
    expect(museRecheck).toHaveBeenCalledTimes(1);
    await vi.advanceTimersByTimeAsync(MUSE_RECHECK_BACKOFF_MS[1]);
    expect(museRecheck).toHaveBeenCalledTimes(2);
    // Fresh probe pushed into the shared health poll on every tick.
    expect(refreshDaemonHealth).toHaveBeenCalledTimes(2);

    // Done → no further polling no matter how long we wait.
    await vi.advanceTimersByTimeAsync(20 * 60_000);
    expect(museRecheck).toHaveBeenCalledTimes(2);
  });

  it('chains install → login watch when the install lands unauthenticated', async () => {
    museRecheck
      .mockResolvedValueOnce(status({ museInstalled: true })) // installed, not signed in
      .mockResolvedValueOnce(status({ museInstalled: true, museAuthed: true }));
    scheduleMuseRecheck('install');

    await vi.advanceTimersByTimeAsync(MUSE_RECHECK_BACKOFF_MS[0]);
    expect(museRecheck).toHaveBeenCalledTimes(1);
    // The chained login loop restarts the backoff from the top.
    await vi.advanceTimersByTimeAsync(MUSE_RECHECK_BACKOFF_MS[0]);
    expect(museRecheck).toHaveBeenCalledTimes(2);
    // Authed now → chain is done.
    await vi.advanceTimersByTimeAsync(20 * 60_000);
    expect(museRecheck).toHaveBeenCalledTimes(2);
  });

  it('a newer schedule supersedes the older loop', async () => {
    museRecheck.mockResolvedValue(status());
    scheduleMuseRecheck('install');
    scheduleMuseRecheck('login'); // takes over before any timer fires

    await vi.advanceTimersByTimeAsync(MUSE_RECHECK_BACKOFF_MS[0]);
    // The superseded install loop's timer fired but did nothing; only the
    // login loop actually probed.
    expect(museRecheck).toHaveBeenCalledTimes(1);
  });

  it('keeps polling through a failed recheck (transient daemon hiccup)', async () => {
    museRecheck
      .mockRejectedValueOnce(new Error('daemon restarting'))
      .mockResolvedValueOnce(status({ museInstalled: true, museAuthed: true }));
    scheduleMuseRecheck('install');

    await vi.advanceTimersByTimeAsync(MUSE_RECHECK_BACKOFF_MS[0]);
    expect(museRecheck).toHaveBeenCalledTimes(1);
    await vi.advanceTimersByTimeAsync(MUSE_RECHECK_BACKOFF_MS[1]);
    expect(museRecheck).toHaveBeenCalledTimes(2);
  });

  it('gives up after the backoff table is exhausted (abandoned install)', async () => {
    museRecheck.mockResolvedValue(status()); // never completes
    scheduleMuseRecheck('install');

    const total = MUSE_RECHECK_BACKOFF_MS.reduce((a, b) => a + b, 0);
    await vi.advanceTimersByTimeAsync(total + 60_000);
    expect(museRecheck).toHaveBeenCalledTimes(MUSE_RECHECK_BACKOFF_MS.length);
  });

  it('stops immediately on 403/404 (relay surface / older daemon)', async () => {
    const err = Object.assign(new Error('forbidden'), { status: 403 });
    museRecheck.mockRejectedValue(err);
    scheduleMuseRecheck('login');

    await vi.advanceTimersByTimeAsync(20 * 60_000);
    expect(museRecheck).toHaveBeenCalledTimes(1); // no doomed retries
  });

  it('pins the backend it was armed for across a mid-loop switch (dual mode)', async () => {
    routerMode = true;
    storeActiveBackend = 'wsl';
    museRecheck
      .mockResolvedValueOnce(status({ museInstalled: true })) // install done → chains login
      .mockResolvedValueOnce(status({ museInstalled: true, museAuthed: true }));
    scheduleMuseRecheck('install');

    // The user flips to the Windows side while the loop is waiting — the loop
    // must keep probing the WSL daemon it was armed for.
    storeActiveBackend = 'win';
    await vi.advanceTimersByTimeAsync(MUSE_RECHECK_BACKOFF_MS[0]);
    expect(museRecheck).toHaveBeenLastCalledWith('wsl');
    // The chained login watch inherits the same pin.
    await vi.advanceTimersByTimeAsync(MUSE_RECHECK_BACKOFF_MS[0]);
    expect(museRecheck).toHaveBeenLastCalledWith('wsl');
    expect(museRecheck).toHaveBeenCalledTimes(2);
  });
});
