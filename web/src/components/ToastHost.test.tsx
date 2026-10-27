import { fireEvent, render, screen } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { ToastHost } from './ToastHost';
import { useStore } from '../store';

const originalDismissToast = useStore.getState().dismissToast;
const originalRevealPane = useStore.getState().revealPane;

describe('ToastHost', () => {
  beforeEach(() => {
    vi.useFakeTimers();
    useStore.setState({ toasts: [] });
  });

  afterEach(() => {
    useStore.setState({
      toasts: [],
      dismissToast: originalDismissToast,
      revealPane: originalRevealPane,
    });
    vi.useRealTimers();
    vi.restoreAllMocks();
  });

  // ttlMs 0 is how a notice stays up (a restart request, a pending device):
  // it must not be dismissed by a timer, only by the person.
  it('keeps a zero-ttl toast until it is clicked', () => {
    const dismissToast = vi.fn();
    useStore.setState({ dismissToast, toasts: [{ id: 7, message: 'Restart to finish this update', kind: 'warn', ttlMs: 0 }] });
    render(<ToastHost />);
    vi.advanceTimersByTime(10 * 60_000);
    expect(dismissToast).not.toHaveBeenCalled();
    fireEvent.click(screen.getByRole('button', { name: /restart to finish this update/i }));
    expect(dismissToast).toHaveBeenCalledWith(7);
  });

  it('renders pane toasts as focusable controls that reveal and dismiss', () => {
    const dismissToast = vi.fn();
    const revealPane = vi.fn().mockResolvedValue(undefined);
    useStore.setState({
      dismissToast,
      revealPane,
      toasts: [{
        id: 1,
        message: 'Pane needs input',
        kind: 'info',
        paneId: 'pane-1',
        ttlMs: 60_000,
      }],
    });

    render(<ToastHost />);

    const toast = screen.getByRole('button', { name: /go to pane: pane needs input/i });
    toast.focus();
    expect(toast).toHaveFocus();

    fireEvent.click(toast);
    expect(revealPane).toHaveBeenCalledWith('pane-1');
    expect(dismissToast).toHaveBeenCalledWith(1);
  });

  it('renders regular toasts as focusable dismiss controls', () => {
    const dismissToast = vi.fn();
    const revealPane = vi.fn();
    useStore.setState({
      dismissToast,
      revealPane,
      toasts: [{
        id: 2,
        message: 'Saved workspace',
        kind: 'success',
        ttlMs: 60_000,
      }],
    });

    render(<ToastHost />);

    const toast = screen.getByRole('button', { name: /dismiss notification: saved workspace/i });
    toast.focus();
    expect(toast).toHaveFocus();

    fireEvent.click(toast);
    expect(revealPane).not.toHaveBeenCalled();
    expect(dismissToast).toHaveBeenCalledWith(2);
  });

  it('renders the fix-agent glyph as an SVG beside glyph-free message copy', () => {
    useStore.setState({
      toasts: [{ id: 4, message: 'Fix agent: apply exit 0', kind: 'info', icon: '🤖', ttlMs: 60_000 }],
    });
    const { container } = render(<ToastHost />);

    expect(container.querySelector('.toast-icon svg')).toBeInTheDocument();
    expect(container.querySelector('.toast-msg')).toHaveTextContent('Fix agent: apply exit 0');
    expect(container.querySelector('.toast-msg')).not.toHaveTextContent('🤖');
  });

  it('renders the agents diamond glyph as an SVG', () => {
    useStore.setState({
      toasts: [{ id: 5, message: 'Docs agent: ok', kind: 'info', icon: '◇', ttlMs: 60_000 }],
    });
    const { container } = render(<ToastHost />);
    expect(container.querySelector('.toast-icon svg')).toBeInTheDocument();
  });

  it('renders the default alert SVG for warning toasts without a supplied icon', () => {
    useStore.setState({
      toasts: [{ id: 6, message: 'Fix agent failed', kind: 'warn', ttlMs: 60_000 }],
    });
    const { container } = render(<ToastHost />);
    expect(container.querySelector('.toast-icon svg')).toBeInTheDocument();
  });
});
