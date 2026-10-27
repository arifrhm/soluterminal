import { fireEvent, render, screen } from '@testing-library/react';
import { describe, expect, it, vi } from 'vitest';
import { APPROVAL_PENDING_TITLE, DeviceApprovalPendingState } from './DeviceApprovalPendingState';

describe('DeviceApprovalPendingState', () => {
  it('says the browser awaits approval, not that the daemon is down, and retries', () => {
    const onRetry = vi.fn();
    render(<DeviceApprovalPendingState onRetry={onRetry} />);
    expect(screen.getByText(APPROVAL_PENDING_TITLE)).toBeInTheDocument();
    expect(screen.queryByText(/daemon/i)).toBeNull();
    fireEvent.click(screen.getByRole('button', { name: 'Try again' }));
    expect(onRetry).toHaveBeenCalledTimes(1);
  });
});
