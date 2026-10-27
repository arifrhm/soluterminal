import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { api } from '../api';
import { BootstrapErrorState } from './BootstrapErrorState';

describe('BootstrapErrorState', () => {
  afterEach(() => {
    vi.restoreAllMocks();
  });

  it('shows the bootstrap error without presenting the empty sessions copy', () => {
    render(<BootstrapErrorState error="503 Service Unavailable" onRetry={vi.fn()} />);

    const alert = screen.getByRole('alert');
    expect(alert).toHaveAttribute('aria-live', 'assertive');
    expect(alert).toHaveTextContent('Could not load sessions');
    // The default view is now a cause-ranked checklist; the raw error is
    // tucked behind a "Show details" toggle. Reveal it before asserting.
    expect(screen.queryByText('503 Service Unavailable')).toBeNull();
    fireEvent.click(screen.getByRole('button', { name: /show details/i }));
    expect(screen.getByText('503 Service Unavailable')).toBeInTheDocument();
    expect(screen.queryByText(/No sessions yet/i)).toBeNull();
  });

  it('runs the retry callback from the primary action', async () => {
    const onRetry = vi.fn().mockResolvedValue(undefined);
    render(<BootstrapErrorState error="network down" onRetry={onRetry} />);

    fireEvent.click(screen.getByRole('button', { name: /retry bootstrap/i }));

    await waitFor(() => expect(onRetry).toHaveBeenCalledTimes(1));
  });

  it('surfaces daemon health check failures inline', async () => {
    vi.spyOn(api, 'health').mockRejectedValue(new Error('connection refused'));
    render(<BootstrapErrorState error="network down" onRetry={vi.fn()} />);

    fireEvent.click(screen.getByRole('button', { name: /check daemon health/i }));

    await waitFor(() => {
      expect(screen.getByText(/Daemon health check failed: connection refused/i)).toBeInTheDocument();
    });
  });
});
