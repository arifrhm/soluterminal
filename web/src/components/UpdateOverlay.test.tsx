import { act, cleanup, render, screen } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { api, ApiError } from '../api';
import { cancelDeferredUpdateRetry, resetUpdateRecoveryForTests, useStore } from '../store';
import type { UpdateStatus } from '../types';
import { UpdateOverlay } from './UpdateOverlay';

const status = (over: Partial<UpdateStatus>): UpdateStatus =>
  ({ current: '1.0.0', latest: '2.0.0', available: true, applying: false, ...over }) as UpdateStatus;

describe('UpdateOverlay admission sequence', () => {
  beforeEach(() => {
    vi.useFakeTimers();
    cancelDeferredUpdateRetry();
    resetUpdateRecoveryForTests();
    useStore.setState({
      panes: {},
      deferredUpdate: null,
      updateApplyRequestInFlight: false,
      updateStatus: status({}),
    });
  });

  afterEach(() => {
    cleanup();
    cancelDeferredUpdateRetry();
    resetUpdateRecoveryForTests();
    useStore.setState({
      deferredUpdate: null,
      updateApplyRequestInFlight: false,
      updateStatus: null,
    });
    vi.restoreAllMocks();
    vi.useRealTimers();
  });

  it('requires verified admission and dismisses after successor recovery', async () => {
    render(<UpdateOverlay />);
    expect(screen.queryByRole('dialog')).toBeNull();

    vi.spyOn(api, 'applyUpdate').mockRejectedValueOnce(
      new ApiError(409, 'workflow drain', {
        busy: true,
        cause: 'workflow',
        workflowInflight: 2,
      }),
    );
    let deferredRequest!: Promise<void>;
    act(() => {
      deferredRequest = useStore.getState().applyUpdate();
    });
    expect(useStore.getState().updateApplyRequestInFlight).toBe(true);
    expect(screen.queryByRole('dialog')).toBeNull();
    await act(async () => {
      await deferredRequest;
    });
    expect(useStore.getState().deferredUpdate?.cause).toBe('workflow');
    expect(useStore.getState().updateStatus?.applying).toBe(false);
    expect(screen.queryByRole('dialog')).toBeNull();

    // A status poll can overlap the daemon's short pre-gate apply
    // reservation. The established hold keeps that transient value from
    // masquerading as an admitted swap.
    const statusSpy = vi.spyOn(api, 'getUpdateStatus').mockResolvedValueOnce(
      status({ applying: true, workflowDeferred: true, workflowInflight: 2 }),
    );
    await act(async () => {
      await useStore.getState().refreshUpdateStatus();
    });
    expect(useStore.getState().updateStatus?.applying).toBe(false);
    expect(screen.queryByRole('dialog')).toBeNull();

    // A replayed update-applying frame is only a hint. Fresh false level state
    // must not mount the dialog.
    statusSpy.mockResolvedValueOnce(status({ applying: false }));
    await act(async () => {
      await useStore.getState().confirmUpdateApplyingEvent('2.0.0');
    });
    expect(screen.queryByRole('dialog')).toBeNull();

    // A live event confirmed by applying=true level state does mount it.
    statusSpy.mockResolvedValueOnce(status({ applying: true }));
    await act(async () => {
      await useStore.getState().confirmUpdateApplyingEvent('2.0.0');
    });
    expect(screen.getByRole('dialog')).toBeInTheDocument();
    expect(screen.getByText(/activates automatically at the next bounded safe boundary/i)).toBeInTheDocument();
    expect(screen.getByText(/small\s+dot in its header/i)).toBeInTheDocument();

    // update-aborted SSE clears the committed-swap overlay.
    act(() => {
      useStore.getState().abortUpdateRecovery();
    });
    expect(screen.queryByRole('dialog')).toBeNull();

    // If the SSE frame is lost, an accepted 200 is the fallback signal.
    vi.mocked(api.applyUpdate).mockResolvedValueOnce({ applying: true });
    statusSpy.mockResolvedValueOnce(
      status({ current: '2.0.0', latest: '2.0.0', available: false }),
    );
    let acceptedRequest!: Promise<void>;
    await act(async () => {
      acceptedRequest = useStore.getState().applyUpdate({ skipReload: true });
      await Promise.resolve();
      await Promise.resolve();
    });
    expect(screen.getByRole('dialog')).toBeInTheDocument();

    await act(async () => {
      await vi.advanceTimersByTimeAsync(500);
      await acceptedRequest;
    });
    expect(screen.queryByRole('dialog')).toBeNull();
  });
});
