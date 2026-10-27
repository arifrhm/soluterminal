import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest';
import { render, screen, fireEvent, waitFor } from '@testing-library/react';
import { RotateTokenButton } from './RotateTokenButton';
import type { CloudMachine } from '../cloud';

const machine: CloudMachine = {
  id: 'mach-123',
  name: 'laptop',
  online: true,
  backends: ['host'],
};

describe('RotateTokenButton', () => {
  beforeEach(() => {
    vi.restoreAllMocks();
  });
  afterEach(() => {
    vi.restoreAllMocks();
  });

  it('opens the confirmation modal when the button is clicked', () => {
    const toast = vi.fn();
    render(<RotateTokenButton machine={machine} bearer="tok" toast={toast} />);
    expect(screen.queryByRole('dialog')).toBeNull();

    fireEvent.click(screen.getByRole('button', { name: /rotate token/i }));

    const dialog = screen.getByRole('dialog');
    expect(dialog).toBeInTheDocument();
    // Title interpolates the machine name.
    expect(screen.getByText(/Rotate token for laptop\?/i)).toBeInTheDocument();
    // Body copy from the spec.
    expect(screen.getByText(/keep working for 10 minutes/i)).toBeInTheDocument();
    expect(screen.getByText(/within about a minute/i)).toBeInTheDocument();
  });

  it('cancels without firing a request', async () => {
    const toast = vi.fn();
    const fetchMock = vi.fn();
    vi.stubGlobal('fetch', fetchMock);
    render(<RotateTokenButton machine={machine} bearer="tok" toast={toast} />);

    fireEvent.click(screen.getByRole('button', { name: /^rotate token$/i }));
    fireEvent.click(screen.getByRole('button', { name: /cancel/i }));

    expect(screen.queryByRole('dialog')).toBeNull();
    expect(fetchMock).not.toHaveBeenCalled();
    expect(toast).not.toHaveBeenCalled();
  });

  it('POSTs to the rotate-token endpoint with bearer auth and toasts on 200', async () => {
    const toast = vi.fn();
    const fetchMock = vi.fn().mockResolvedValue({
      ok: true,
      status: 200,
      json: async () => ({ newToken: 'a'.repeat(64), gracePeriodSec: 600 }),
    });
    vi.stubGlobal('fetch', fetchMock);

    render(<RotateTokenButton machine={machine} bearer="bearer-xyz" toast={toast} />);
    // Open modal.
    fireEvent.click(screen.getByRole('button', { name: /^rotate token$/i }));
    // Confirm — there are now two "Rotate token" buttons (trigger + confirm).
    // The trigger has been replaced underneath by a still-clickable button,
    // but the dialog confirm is the focused one inside role=dialog.
    const dialog = screen.getByRole('dialog');
    const confirmBtn = Array.from(dialog.querySelectorAll('button')).find(
      (b) => /rotate token/i.test(b.textContent ?? ''),
    )!;
    fireEvent.click(confirmBtn);

    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(1));
    const [url, init] = fetchMock.mock.calls[0];
    expect(url).toBe('/api/machines/mach-123/rotate-token');
    expect(init.method).toBe('POST');
    expect((init.headers as Record<string, string>).Authorization).toBe('Bearer bearer-xyz');

    await waitFor(() => expect(toast).toHaveBeenCalled());
    const [msg, opts] = toast.mock.calls[0];
    expect(msg).toMatch(/Rotation initiated for laptop/);
    expect(msg).toMatch(/within a minute/);
    expect(opts.kind).toBe('success');
    // The new token must NOT appear in any toast message.
    for (const call of toast.mock.calls) {
      expect(call[0]).not.toMatch(/a{16,}/); // 64-hex token, definitely 16+ a's
    }
    // Modal closes after success.
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull());
  });

  it('shows the conflict toast on 409', async () => {
    const toast = vi.fn();
    const fetchMock = vi.fn().mockResolvedValue({
      ok: false,
      status: 409,
      text: async () => 'rotation in progress',
      json: async () => ({}),
    });
    vi.stubGlobal('fetch', fetchMock);

    render(<RotateTokenButton machine={machine} bearer="tok" toast={toast} />);
    fireEvent.click(screen.getByRole('button', { name: /^rotate token$/i }));
    const dialog = screen.getByRole('dialog');
    const confirmBtn = Array.from(dialog.querySelectorAll('button')).find(
      (b) => /rotate token/i.test(b.textContent ?? ''),
    )!;
    fireEvent.click(confirmBtn);

    await waitFor(() => expect(toast).toHaveBeenCalled());
    const [msg, opts] = toast.mock.calls[0];
    expect(msg).toMatch(/already in progress/i);
    expect(opts.kind).toBe('warn');
  });

  it('shows an authorization toast on 401', async () => {
    const toast = vi.fn();
    const fetchMock = vi.fn().mockResolvedValue({
      ok: false,
      status: 401,
      text: async () => 'unauthorized',
      json: async () => ({}),
    });
    vi.stubGlobal('fetch', fetchMock);

    render(<RotateTokenButton machine={machine} bearer="tok" toast={toast} />);
    fireEvent.click(screen.getByRole('button', { name: /^rotate token$/i }));
    const dialog = screen.getByRole('dialog');
    const confirmBtn = Array.from(dialog.querySelectorAll('button')).find(
      (b) => /rotate token/i.test(b.textContent ?? ''),
    )!;
    fireEvent.click(confirmBtn);

    await waitFor(() => expect(toast).toHaveBeenCalled());
    const [msg, opts] = toast.mock.calls[0];
    expect(msg).toMatch(/Not authorized/i);
    expect(opts.kind).toBe('error');
  });

  it('surfaces a network error message', async () => {
    const toast = vi.fn();
    const fetchMock = vi.fn().mockRejectedValue(new Error('socket hang up'));
    vi.stubGlobal('fetch', fetchMock);

    render(<RotateTokenButton machine={machine} bearer="tok" toast={toast} />);
    fireEvent.click(screen.getByRole('button', { name: /^rotate token$/i }));
    const dialog = screen.getByRole('dialog');
    const confirmBtn = Array.from(dialog.querySelectorAll('button')).find(
      (b) => /rotate token/i.test(b.textContent ?? ''),
    )!;
    fireEvent.click(confirmBtn);

    await waitFor(() => expect(toast).toHaveBeenCalled());
    const [msg, opts] = toast.mock.calls[0];
    expect(msg).toMatch(/socket hang up/);
    expect(opts.kind).toBe('error');
  });
});
