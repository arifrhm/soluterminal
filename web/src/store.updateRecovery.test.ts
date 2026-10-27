import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { api } from './api';
import {
  resetUpdateRecoveryForTests,
  UPDATE_RECOVERY_TIMEOUT_MS,
  useStore,
} from './store';
import type { UpdateStatus } from './types';

const status = (over: Partial<UpdateStatus> = {}): UpdateStatus => ({
  current: '1.0.0',
  latest: '2.0.0',
  available: true,
  applying: false,
  ...over,
});

describe('shared update recovery controller', () => {
  const real = {
    applyUpdate: useStore.getState().applyUpdate,
    refreshUpdateStatus: useStore.getState().refreshUpdateStatus,
    confirmUpdateApplyingEvent: useStore.getState().confirmUpdateApplyingEvent,
    abortUpdateRecovery: useStore.getState().abortUpdateRecovery,
  };

  beforeEach(() => {
    vi.useFakeTimers();
    vi.setSystemTime(Date.parse('2026-09-04T01:15:00Z'));
    resetUpdateRecoveryForTests();
    useStore.setState({
      panes: {},
      deferredUpdate: null,
      updateApplyRequestInFlight: false,
      updateSettings: { autoApply: 'off' },
      updateStatus: status(),
      toasts: [],
      ...real,
    });
  });

  afterEach(() => {
    resetUpdateRecoveryForTests();
    useStore.setState({
      deferredUpdate: null,
      updateApplyRequestInFlight: false,
      updateStatus: null,
      toasts: [],
      ...real,
    });
    vi.restoreAllMocks();
    vi.useRealTimers();
  });

  it('ignores a replayed applying event when fresh level state says false', async () => {
    vi.spyOn(api, 'getUpdateStatus').mockResolvedValue(status({ applying: false }));

    await useStore.getState().confirmUpdateApplyingEvent('2.0.0');

    expect(useStore.getState().updateStatus?.applying).toBe(false);
    expect(vi.getTimerCount()).toBe(0);
  });

  it('joins confirmed SSE and accepted HTTP signals without extending the first deadline', async () => {
    const getStatus = vi.spyOn(api, 'getUpdateStatus');
    getStatus.mockResolvedValueOnce(status({ applying: true }));
    getStatus.mockResolvedValue(status({ applying: false }));
    vi.spyOn(api, 'applyUpdate').mockResolvedValue({ applying: true });

    await useStore.getState().confirmUpdateApplyingEvent('2.0.0');
    expect(useStore.getState().updateStatus?.applying).toBe(true);
    expect(vi.getTimerCount()).toBe(2);

    await vi.advanceTimersByTimeAsync(10_000);
    const accepted = useStore.getState().applyUpdate({ skipReload: true });
    await vi.advanceTimersByTimeAsync(0);
    await useStore.getState().refreshUpdateStatus();

    await vi.advanceTimersByTimeAsync(UPDATE_RECOVERY_TIMEOUT_MS - 10_001);
    expect(useStore.getState().updateStatus?.applying).toBe(true);
    await vi.advanceTimersByTimeAsync(1);
    await accepted;

    expect(useStore.getState().updateStatus?.applying).toBe(false);
    expect(useStore.getState().toasts.filter((toast) => toast.kind === 'warn')).toHaveLength(1);
    expect(vi.getTimerCount()).toBe(0);

    const retry = useStore.getState().applyUpdate({ skipReload: true });
    await vi.advanceTimersByTimeAsync(0);
    expect(useStore.getState().updateStatus?.applying).toBe(true);
    useStore.getState().abortUpdateRecovery();
    await retry;
    expect(useStore.getState().updateStatus?.applying).toBe(false);
  });

  it('bounds repeated relay errors and clears the overlay with a warning', async () => {
    vi.spyOn(api, 'applyUpdate').mockResolvedValue({ applying: true });
    vi.spyOn(api, 'getUpdateStatus').mockRejectedValue(new Error('relay unavailable'));

    const applying = useStore.getState().applyUpdate({ skipReload: true });
    await vi.advanceTimersByTimeAsync(0);
    expect(useStore.getState().updateStatus?.applying).toBe(true);

    await vi.advanceTimersByTimeAsync(UPDATE_RECOVERY_TIMEOUT_MS);
    await applying;

    expect(useStore.getState().updateStatus?.applying).toBe(false);
    expect(useStore.getState().toasts.at(-1)?.message).toContain('daemon did not return');
    expect(vi.getTimerCount()).toBe(0);
  });

  it('does not resurrect a timed-out admission from late HTTP, status, or SSE signals', async () => {
    let resolveApply!: (value: { applying: boolean }) => void;
    vi.spyOn(api, 'applyUpdate').mockReturnValue(new Promise((resolve) => { resolveApply = resolve; }));
    const getStatus = vi.spyOn(api, 'getUpdateStatus');
    getStatus.mockResolvedValueOnce(status({ applying: true }));
    getStatus.mockRejectedValue(new Error('relay unavailable'));

    const pendingApply = useStore.getState().applyUpdate({ skipReload: true });
    await vi.advanceTimersByTimeAsync(0);
    await useStore.getState().confirmUpdateApplyingEvent('2.0.0');
    expect(useStore.getState().updateStatus?.applying).toBe(true);

    await vi.advanceTimersByTimeAsync(UPDATE_RECOVERY_TIMEOUT_MS);
    expect(useStore.getState().updateStatus?.applying).toBe(false);
    expect(useStore.getState().toasts.filter((toast) => toast.kind === 'warn')).toHaveLength(1);

    resolveApply({ applying: true });
    await vi.advanceTimersByTimeAsync(0);
    expect(useStore.getState().updateStatus?.applying).toBe(false);
    await pendingApply;

    getStatus.mockResolvedValue(status({ applying: true }));
    await useStore.getState().refreshUpdateStatus();
    await useStore.getState().confirmUpdateApplyingEvent('2.0.0');

    expect(useStore.getState().updateStatus?.applying).toBe(false);
    expect(useStore.getState().toasts.filter((toast) => toast.kind === 'warn')).toHaveLength(1);
    expect(vi.getTimerCount()).toBe(0);
  });

  it('ignores a pending SSE confirmation that resolves after update-aborted', async () => {
    let resolveStatus!: (value: UpdateStatus) => void;
    vi.spyOn(api, 'getUpdateStatus').mockReturnValue(new Promise((resolve) => { resolveStatus = resolve; }));

    const confirmation = useStore.getState().confirmUpdateApplyingEvent('2.0.0');
    await vi.advanceTimersByTimeAsync(0);
    useStore.getState().abortUpdateRecovery();
    resolveStatus(status({ applying: true }));
    await confirmation;

    expect(useStore.getState().updateStatus?.applying).toBe(false);
    expect(vi.getTimerCount()).toBe(0);
  });

  it('cancels admitted recovery when authoritative status reports a workflow hold', async () => {
    vi.spyOn(api, 'applyUpdate').mockResolvedValue({ applying: true });
    vi.spyOn(api, 'getUpdateStatus').mockResolvedValue(status({
      applying: true,
      workflowDeferred: true,
      workflowDeferState: 'draining',
      workflowInflight: 3,
      workflowDeferDeadlineMs: Date.now() + 60_000,
    }));

    const applying = useStore.getState().applyUpdate({ skipReload: true });
    await vi.advanceTimersByTimeAsync(500);
    await applying;

    expect(useStore.getState().updateStatus).toMatchObject({
      applying: false,
      workflowDeferred: true,
      workflowInflight: 3,
    });
    expect(useStore.getState().toasts.filter((toast) => toast.kind === 'warn')).toHaveLength(0);
    expect(vi.getTimerCount()).toBe(0);
  });

  it('lets an explicit accepted admission replace stale cached workflow metadata', async () => {
    useStore.setState({
      updateStatus: status({
        applying: false,
        workflowDeferred: true,
        workflowDeferState: 'draining',
        workflowInflight: 1,
      }),
    });
    vi.spyOn(api, 'applyUpdate').mockResolvedValue({ applying: true });
    vi.spyOn(api, 'getUpdateStatus').mockResolvedValue(status({
      current: '2.0.0', available: false, applying: false,
    }));

    const applying = useStore.getState().applyUpdate({ skipReload: true, force: true });
    await vi.advanceTimersByTimeAsync(0);
    expect(useStore.getState().updateStatus).toMatchObject({ applying: true, workflowDeferred: false });
    await vi.advanceTimersByTimeAsync(500);
    await applying;
    expect(useStore.getState().updateStatus?.applying).toBe(false);
  });

  it('completes once when the successor version appears and cancels the deadline', async () => {
    vi.spyOn(api, 'applyUpdate').mockResolvedValue({ applying: true });
    const successor = status({ current: '2.0.0', available: false, applying: false });
    vi.spyOn(api, 'getUpdateStatus').mockResolvedValue(successor);

    const applying = useStore.getState().applyUpdate({ skipReload: true });
    await vi.advanceTimersByTimeAsync(500);
    await applying;

    expect(useStore.getState().updateStatus).toMatchObject({ current: '2.0.0', applying: false });
    expect(useStore.getState().toasts.filter((toast) => toast.message === 'Updated to 2.0.0')).toHaveLength(1);
    expect(vi.getTimerCount()).toBe(0);

    await useStore.getState().refreshUpdateStatus();
    expect(useStore.getState().toasts.filter((toast) => toast.message === 'Updated to 2.0.0')).toHaveLength(1);
  });
});
