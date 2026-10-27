import { render, screen, fireEvent, cleanup, act } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { useStore } from '../store';
import type { PaneInfo, UpdateHoldCause, UpdateStatus } from '../types';
import { RequiredUpdateModal } from './RequiredUpdateModal';

// The blocking modal's contract: it enforces the operator's version floor
// WITHOUT ever destroying work — hidden while a native pane is mid-turn,
// hidden while the silent auto-apply is doing its job (applying/queued),
// and it only blocks after the state holds for HOLD_MS (anti-flash).

const status = (over: Partial<UpdateStatus>): UpdateStatus =>
  ({ current: '1.0.0', available: false, ...over }) as UpdateStatus;

const pane = (p: Partial<PaneInfo> & { id: string }): PaneInfo => ({ ...p }) as PaneInfo;

describe('RequiredUpdateModal', () => {
  const real = {
    applyUpdate: useStore.getState().applyUpdate,
    applyShellUpdate: useStore.getState().applyShellUpdate,
    checkForUpdate: useStore.getState().checkForUpdate,
  };

  beforeEach(() => {
    vi.useFakeTimers();
    useStore.setState({ panes: {}, deferredUpdate: null, updateApplyRequestInFlight: false });
  });
  afterEach(() => {
    cleanup();
    vi.useRealTimers();
    useStore.setState({ updateStatus: null, panes: {}, deferredUpdate: null, updateApplyRequestInFlight: false, ...real });
  });

  // Render + ride out the anti-flash hold.
  function renderHeld() {
    const r = render(<RequiredUpdateModal />);
    act(() => {
      vi.advanceTimersByTime(1600);
    });
    return r;
  }

  it('stays hidden when nothing is required', () => {
    useStore.setState({ updateStatus: status({ latest: '1.1.0', available: true }) });
    renderHeld();
    expect(screen.queryByRole('alertdialog')).toBeNull();
  });

  it('daemon floor + update available: blocks with "Update now" wired to applyUpdate', () => {
    const applySpy = vi.fn().mockResolvedValue(undefined);
    useStore.setState({
      updateStatus: status({
        latest: '1.1.0',
        available: true,
        required: true,
        requiredReason: 'version 1.0.0 is below the required minimum 1.1.0',
      }),
      applyUpdate: applySpy as never,
    });
    renderHeld();
    expect(screen.getByRole('alertdialog')).toBeInTheDocument();
    expect(screen.getByText(/below the required minimum 1\.1\.0/)).toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: 'Update now' }));
    expect(applySpy).toHaveBeenCalledTimes(1);
  });

  it('daemon floor + nothing downloadable: stuck state offers "Check again"', () => {
    useStore.setState({
      updateStatus: status({ required: true, lastError: 'control fetch: HTTP 500' }),
    });
    renderHeld();
    expect(screen.getByRole('button', { name: 'Check again' })).toBeInTheDocument();
    // The daemon's error text stays in its log; users see a generic line.
    expect(screen.getByText("Couldn't check for updates")).toBeInTheDocument();
    expect(screen.queryByText('control fetch: HTTP 500')).not.toBeInTheDocument();
  });

  it('shell floor: blocks with "Restart & update" wired to applyShellUpdate', () => {
    const shellSpy = vi.fn().mockResolvedValue(undefined);
    useStore.setState({
      updateStatus: status({
        shellRequired: true,
        shellAvailable: true,
        shellCurrent: '0.0.700',
        shellLatest: '0.0.800',
        shellRequiredReason: 'the desktop app (0.0.700) is below the required minimum 0.0.800',
      }),
      applyShellUpdate: shellSpy as never,
    });
    renderHeld();
    fireEvent.click(screen.getByRole('button', { name: 'Restart & update' }));
    expect(shellSpy).toHaveBeenCalledTimes(1);
  });

  it('never blocks while a native pane is mid-turn (agents finish first)', () => {
    useStore.setState({
      panes: { a: pane({ id: 'a', kind: 'native', agentMidTurn: true }) },
      updateStatus: status({ required: true }),
    });
    renderHeld();
    expect(screen.queryByRole('alertdialog')).toBeNull();
  });

  it('stays hidden while a swap is applying (the Updating… overlay owns that state)', () => {
    useStore.setState({
      updateStatus: status({ latest: '1.1.0', available: true, required: true, applying: true }),
    });
    renderHeld();
    expect(screen.queryByRole('alertdialog')).toBeNull();
  });

  it.each<UpdateHoldCause>(['focus', 'workflow', 'unknown'])(
    'stays hidden throughout a %s update hold',
    (cause) => {
      useStore.setState({
        updateStatus: status({ latest: '1.1.0', available: true, required: true }),
        deferredUpdate: { cause, since: 0, auto: true },
      });
      renderHeld();
      expect(screen.queryByRole('alertdialog')).toBeNull();
    },
  );

  it('stays hidden for a status-only workflow drain with no local hold', () => {
    useStore.setState({
      updateStatus: status({
        latest: '1.1.0', available: true, required: true,
        applying: true,
        workflowDeferred: true, workflowDeferState: 'draining',
        workflowInflight: 2, workflowDeferDeadlineMs: Date.now() + 60_000,
      }),
      deferredUpdate: null,
    });
    render(<RequiredUpdateModal />);
    expect(screen.queryByRole('alertdialog')).toBeNull();
    act(() => vi.advanceTimersByTime(3000));
    expect(screen.queryByRole('alertdialog')).toBeNull();
  });

  it('stays hidden while an apply request is awaiting admission', () => {
    useStore.setState({
      updateStatus: status({ latest: '1.1.0', available: true, required: true }),
      updateApplyRequestInFlight: true,
    });
    renderHeld();
    expect(screen.queryByRole('alertdialog')).toBeNull();
  });

  it('never flashes through a required-status → request → typed-hold transition', () => {
    useStore.setState({
      updateStatus: status({ latest: '1.1.0', available: true, required: true }),
      updateApplyRequestInFlight: true,
    });
    render(<RequiredUpdateModal />);
    expect(screen.queryByRole('alertdialog')).toBeNull();

    act(() => {
      vi.advanceTimersByTime(1600);
    });
    expect(screen.queryByRole('alertdialog')).toBeNull();

    act(() => {
      useStore.setState({
        updateApplyRequestInFlight: false,
        deferredUpdate: { cause: 'workflow', since: 10, auto: true },
      });
      vi.advanceTimersByTime(1600);
    });
    expect(screen.queryByRole('alertdialog')).toBeNull();
  });

  it('anti-flash: does not block before the hold elapses', () => {
    useStore.setState({ updateStatus: status({ required: true }) });
    render(<RequiredUpdateModal />);
    expect(screen.queryByRole('alertdialog')).toBeNull(); // 0ms — silent path gets its beat
    act(() => {
      vi.advanceTimersByTime(1600);
    });
    expect(screen.getByRole('alertdialog')).toBeInTheDocument();
  });
});
