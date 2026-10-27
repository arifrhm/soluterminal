import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

// Keep importing prefs.ts side-effect-free (no network, no live subscription).
vi.mock('./cloud', () => ({
  useCloud: { subscribe: vi.fn(), getState: () => ({ session: null }) },
}));
const getUserPrefs = vi.fn();
const putUserPrefs = vi.fn();
vi.mock('./api', () => ({
  api: {
    getUserPrefs: (...a: unknown[]) => getUserPrefs(...a),
    putUserPrefs: (...a: unknown[]) => putUserPrefs(...a),
  },
}));

import { usePrefs } from './prefs';
import { useEntitlements } from './entitlements';

describe('usePrefs', () => {
  beforeEach(() => {
    vi.useFakeTimers();
    localStorage.clear();
    getUserPrefs.mockReset();
    putUserPrefs.mockReset();
    putUserPrefs.mockResolvedValue({ prefs: {}, updated_at: '2026-07-04T00:00:00.000Z' });
    usePrefs.setState({ prefs: {}, updatedAt: null, userId: null, hydrated: false });
  });

  afterEach(() => {
    vi.useRealTimers();
    useEntitlements.setState({ viewPersonal: false });
  });

  it('pauses layout sync in both directions while "View as personal" is on', async () => {
    useEntitlements.setState({ viewPersonal: true });
    usePrefs.getState().setPrefs((p) => ({ ...p, sidebar: { hidden: ['ports'] } }));
    vi.advanceTimersByTime(800);
    expect(putUserPrefs).not.toHaveBeenCalled();
    await usePrefs.getState().hydrateFromCloud('user-1');
    expect(getUserPrefs).not.toHaveBeenCalled();
    expect(usePrefs.getState().hydrated).toBe(true);
  });

  it('setPrefs updates state and writes the local cache', () => {
    usePrefs.getState().setPrefs((p) => ({ ...p, sidebar: { hidden: ['usage'] } }));

    const st = usePrefs.getState();
    expect(st.prefs.sidebar?.hidden).toEqual(['usage']);
    expect(st.updatedAt).toBeTruthy();

    const cached = JSON.parse(localStorage.getItem('solu.userPrefs.v1')!);
    expect(cached.prefs.sidebar.hidden).toEqual(['usage']);
  });

  it('setPrefs pushes to the cloud, debounced', () => {
    usePrefs.getState().setPrefs((p) => ({ ...p, sidebar: { order: ['files', 'usage'] } }));
    expect(putUserPrefs).not.toHaveBeenCalled();

    vi.advanceTimersByTime(800);
    expect(putUserPrefs).toHaveBeenCalledWith({ sidebar: { order: ['files', 'usage'] } });
  });

  it('hydrate adopts the cloud copy when it is newer', async () => {
    usePrefs.setState({ prefs: { sidebar: { hidden: ['a'] } }, updatedAt: '2026-01-01T00:00:00.000Z', userId: 'u1', hydrated: false });
    getUserPrefs.mockResolvedValue({ prefs: { sidebar: { hidden: ['b'] } }, updated_at: '2026-06-01T00:00:00.000Z' });

    await usePrefs.getState().hydrateFromCloud('u1');

    expect(usePrefs.getState().prefs.sidebar?.hidden).toEqual(['b']);
    expect(usePrefs.getState().hydrated).toBe(true);
    expect(putUserPrefs).not.toHaveBeenCalled();
  });

  it('hydrate pushes the local copy when it is newer than the cloud', async () => {
    usePrefs.setState({ prefs: { sidebar: { hidden: ['local'] } }, updatedAt: '2026-06-01T00:00:00.000Z', userId: 'u1', hydrated: false });
    getUserPrefs.mockResolvedValue({ prefs: { sidebar: { hidden: ['old'] } }, updated_at: '2026-01-01T00:00:00.000Z' });

    await usePrefs.getState().hydrateFromCloud('u1');

    expect(usePrefs.getState().prefs.sidebar?.hidden).toEqual(['local']);
    expect(putUserPrefs).toHaveBeenCalledWith({ sidebar: { hidden: ['local'] } });
  });

  it('hydrate pushes local when the cloud has no saved copy', async () => {
    usePrefs.setState({ prefs: { sidebar: { order: ['x'] } }, updatedAt: '2026-06-01T00:00:00.000Z', userId: 'u1', hydrated: false });
    getUserPrefs.mockResolvedValue({ prefs: {}, updated_at: null });

    await usePrefs.getState().hydrateFromCloud('u1');

    expect(putUserPrefs).toHaveBeenCalledWith({ sidebar: { order: ['x'] } });
  });

  it('hydrate stays local when the cloud is unreachable', async () => {
    usePrefs.setState({ prefs: { sidebar: { hidden: ['keep'] } }, updatedAt: '2026-06-01T00:00:00.000Z', userId: 'u1', hydrated: false });
    getUserPrefs.mockRejectedValue(new Error('offline'));

    await usePrefs.getState().hydrateFromCloud('u1');

    expect(usePrefs.getState().prefs.sidebar?.hidden).toEqual(['keep']);
    expect(usePrefs.getState().hydrated).toBe(true);
    expect(putUserPrefs).not.toHaveBeenCalled();
  });

  it('discards a different user’s cached prefs and never uploads them (shared machine)', async () => {
    // Cache belongs to user A; user B signs in on the same browser.
    usePrefs.setState({ prefs: { sidebar: { hidden: ['A-only'] } }, updatedAt: '2026-06-01T00:00:00.000Z', userId: 'A', hydrated: false });
    // B has nothing saved in the cloud yet.
    getUserPrefs.mockResolvedValue({ prefs: {}, updated_at: null });

    await usePrefs.getState().hydrateFromCloud('B');

    // A's layout must be discarded, not shown to B...
    expect(usePrefs.getState().prefs).toEqual({});
    expect(usePrefs.getState().userId).toBe('B');
    // ...and must NEVER be pushed into B's cloud row.
    expect(putUserPrefs).not.toHaveBeenCalled();
  });
});
