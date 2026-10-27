import { beforeEach, describe, expect, it, vi } from 'vitest';
import { useStore } from '../store';
import { useEntitlements } from '../entitlements';

beforeEach(() => {
  useStore.setState({
    toasts: [],
    notificationLog: [],
    unreadNotifications: 0,
    dialog: null,
    toastPosition: 'bottom-right',
  });
  localStorage.clear();
});

describe('toast/dialog slice', () => {
  it('adds default info toasts with incrementing ids and replaces an immediate duplicate', () => {
    useStore.getState().toast('first');
    const first = useStore.getState().toasts[0];

    expect(first).toMatchObject({ message: 'first', kind: 'info' });
    expect(typeof first.id).toBe('number');

    useStore.getState().toast('second', { kind: 'success' });
    const second = useStore.getState().toasts[1];
    expect(second.id).toBe(first.id + 1);

    useStore.getState().toast('second', { kind: 'success' });
    const state = useStore.getState();
    expect(state.toasts).toHaveLength(2);
    expect(state.toasts[1].id).toBe(second.id + 1);
    expect(state.notificationLog).toHaveLength(2);
    expect(state.notificationLog[0].id).toBe(second.id + 1);
  });

  it('caps the visible queue at three and keeps the newest toasts', () => {
    for (const message of ['one', 'two', 'three', 'four']) {
      useStore.getState().toast(message);
    }

    expect(useStore.getState().toasts.map((toast) => toast.message)).toEqual([
      'two',
      'three',
      'four',
    ]);
  });

  it('keeps a newest-first notification log, collapses immediate duplicates, and caps it at 40', () => {
    useStore.getState().toast('duplicate', { kind: 'warn' });
    useStore.getState().toast('duplicate', { kind: 'warn' });
    expect(useStore.getState().notificationLog).toHaveLength(1);
    expect(useStore.getState().unreadNotifications).toBe(1);

    for (let i = 0; i < 41; i += 1) {
      useStore.getState().toast(`message-${i}`);
    }

    const log = useStore.getState().notificationLog;
    expect(log).toHaveLength(40);
    expect(log[0].message).toBe('message-40');
    expect(log[39].message).toBe('message-1');
  });

  it('can skip Recent and only increments unread for logged, non-duplicate warn/error toasts', () => {
    useStore.getState().toast('not logged', { kind: 'error', logToRecent: false });
    expect(useStore.getState().toasts[0].message).toBe('not logged');
    expect(useStore.getState().notificationLog).toEqual([]);
    expect(useStore.getState().unreadNotifications).toBe(0);

    useStore.getState().toast('info');
    useStore.getState().toast('success', { kind: 'success' });
    useStore.getState().toast('warning', { kind: 'warn' });
    useStore.getState().toast('warning', { kind: 'warn' });
    useStore.getState().toast('error', { kind: 'error' });

    expect(useStore.getState().unreadNotifications).toBe(2);
  });

  it('dismisses only the selected toast and resets notification state', () => {
    useStore.getState().toast('keep', { kind: 'warn' });
    useStore.getState().toast('dismiss', { kind: 'error' });
    const dismissId = useStore.getState().toasts[1].id;

    useStore.getState().dismissToast(dismissId);
    expect(useStore.getState().toasts.map((toast) => toast.message)).toEqual(['keep']);

    useStore.getState().markNotificationsSeen();
    expect(useStore.getState().unreadNotifications).toBe(0);
    expect(useStore.getState().notificationLog).toHaveLength(2);

    useStore.getState().toast('another warning', { kind: 'warn' });
    useStore.getState().clearNotificationLog();
    expect(useStore.getState().notificationLog).toEqual([]);
    expect(useStore.getState().unreadNotifications).toBe(0);
  });

  it('updates and persists the toast position', () => {
    useStore.getState().setToastPosition('top-center');

    expect(useStore.getState().toastPosition).toBe('top-center');
    expect(localStorage.getItem('solu.toastPosition')).toBe('top-center');
  });

  it('opens dialogs with incrementing ids and cancels replaced dialogs', () => {
    const confirmResolve = vi.fn();
    const promptResolve = vi.fn();
    const alertResolve = vi.fn();

    useStore.getState().openDialog({ kind: 'confirm', message: 'confirm?', resolve: confirmResolve });
    const confirmId = useStore.getState().dialog!.id;
    useStore.getState().openDialog({ kind: 'prompt', message: 'prompt?', resolve: promptResolve });
    const promptId = useStore.getState().dialog!.id;

    expect(confirmResolve).toHaveBeenCalledOnce();
    expect(confirmResolve).toHaveBeenCalledWith(false);
    expect(promptId).toBe(confirmId + 1);

    useStore.getState().openDialog({ kind: 'alert', message: 'alert', resolve: alertResolve });
    expect(promptResolve).toHaveBeenCalledOnce();
    expect(promptResolve).toHaveBeenCalledWith(null);
    expect(useStore.getState().dialog).toMatchObject({
      id: promptId + 1,
      kind: 'alert',
      message: 'alert',
    });
  });

  it('closes the current dialog with its value and safely no-ops without one', () => {
    const resolve = vi.fn();
    useStore.getState().openDialog({ kind: 'prompt', message: 'name?', resolve });

    useStore.getState().closeDialog('Ada');
    expect(resolve).toHaveBeenCalledOnce();
    expect(resolve).toHaveBeenCalledWith('Ada');
    expect(useStore.getState().dialog).toBeNull();

    expect(() => useStore.getState().closeDialog(null)).not.toThrow();
    expect(resolve).toHaveBeenCalledOnce();
    expect(useStore.getState().dialog).toBeNull();
  });
});

describe('task links on toasts', () => {

  it('drops the Tasks link on a public install', () => {
    useEntitlements.setState({ loaded: true, team: false, operator: false });
    useStore.getState().toast('Decision needed', { workflowTaskId: 'wft_1' });
    expect(useStore.getState().toasts[0].workflowTaskId).toBeUndefined();
    expect(useStore.getState().notificationLog[0].workflowTaskId).toBeUndefined();
  });
});
