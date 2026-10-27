import { fireEvent, render, screen } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import type { Session } from '@supabase/supabase-js';
import { useCloud } from '../../cloud';
import { AccountFooter } from './AccountFooter';

const fakeSession = {
  user: { email: 'ben@example.com' },
} as unknown as Session;

describe('AccountFooter System entry point', () => {
  afterEach(() => {
    useCloud.setState({ session: null });
  });

  it('opens System without requiring a cloud session', () => {
    const onOpenSystem = vi.fn();
    const { container } = render(<AccountFooter onOpenSystem={onOpenSystem} />);

    fireEvent.click(screen.getByRole('button', { name: /open system/i }));
    expect(onOpenSystem).toHaveBeenCalledTimes(1);
    expect(container.querySelector('.sidebar-footer-system-row')).not.toBeNull();
    expect(container.querySelector('.sidebar-footer-identity-row')).toBeNull();
  });

  it('uses a distinct icon for System beside Usage', () => {
    useCloud.setState({ session: fakeSession });
    render(<AccountFooter onOpenUsage={vi.fn()} onOpenSystem={vi.fn()} />);

    const usageIcon = screen.getByRole('button', { name: /open usage/i }).querySelector('svg');
    const systemIcon = screen.getByRole('button', { name: /open system/i }).querySelector('svg');
    expect(usageIcon).not.toBeNull();
    expect(systemIcon).not.toBeNull();
    expect(systemIcon?.innerHTML).not.toBe(usageIcon?.innerHTML);
  });
});
