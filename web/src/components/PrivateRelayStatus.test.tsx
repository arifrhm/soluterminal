import { act, render, screen } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { useCloud } from '../cloud';
import * as privateRelay from '../lib/privateRelay';
import { PrivateRelayStatus, privateRelayStatusText } from './PrivateRelayStatus';

afterEach(() => vi.restoreAllMocks());

describe('PrivateRelayStatus', () => {
  it('shows a persistent note while a strict computer is unreachable, by name', () => {
    useCloud.setState({ machines: [{ id: 'mach_1', name: 'Studio PC', online: true }] as never });
    const targets = vi.spyOn(privateRelay, 'privateRelayUnreachableTargets').mockReturnValue([]);
    render(<PrivateRelayStatus />);
    expect(screen.queryByRole('status')).toBeNull();

    targets.mockReturnValue(['mach_1/wsl']);
    act(() => {
      window.dispatchEvent(new CustomEvent(privateRelay.PRIVATE_RELAY_STATUS_EVENT, { detail: { target: 'mach_1/wsl', reachable: false } }));
    });
    expect(screen.getByRole('status')).toHaveTextContent(privateRelayStatusText('Studio PC'));
    expect(privateRelayStatusText('Studio PC')).toBe("Can't reach Studio PC securely — retrying");

    targets.mockReturnValue([]);
    act(() => {
      window.dispatchEvent(new CustomEvent(privateRelay.PRIVATE_RELAY_STATUS_EVENT, { detail: { target: 'mach_1/wsl', reachable: true } }));
    });
    expect(screen.queryByRole('status')).toBeNull();
  });
});
