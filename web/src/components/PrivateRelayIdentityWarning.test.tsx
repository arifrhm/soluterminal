import { act, fireEvent, render, screen } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { deviceEnrolledFor, markDeviceEnrolled, pinComputerIdentity, pinnedComputerIdentity } from '../lib/deviceIdentity';
import { PRIVATE_RELAY_IDENTITY_EVENT, PRIVATE_RELAY_IDENTITY_MESSAGE } from '../lib/privateRelay';
import { PRIVATE_RELAY_IDENTITY_TITLE, PrivateRelayIdentityWarning } from './PrivateRelayIdentityWarning';

afterEach(() => {
  vi.restoreAllMocks();
  localStorage.clear();
});

const announce = (target: string) =>
  act(() => {
    window.dispatchEvent(new CustomEvent(PRIVATE_RELAY_IDENTITY_EVENT, { detail: { target } }));
  });

describe('PrivateRelayIdentityWarning', () => {
  it('stays hidden until a computer fails to prove its pinned identity', () => {
    render(<PrivateRelayIdentityWarning />);
    expect(screen.queryByText(PRIVATE_RELAY_IDENTITY_TITLE)).toBeNull();
    announce('mach_1/wsl');
    expect(screen.getByRole('alertdialog')).toBeInTheDocument();
    expect(screen.getByText(PRIVATE_RELAY_IDENTITY_TITLE)).toBeInTheDocument();
    expect(screen.getByText(PRIVATE_RELAY_IDENTITY_MESSAGE)).toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: 'Not now' }));
    expect(screen.queryByRole('alertdialog')).toBeNull();
  });

  it('forgets the old key and enrolment so the next approval compares codes afresh', () => {
    markDeviceEnrolled('mach_1/wsl');
    pinComputerIdentity('mach_1/wsl', 'MFkwOLD');
    markDeviceEnrolled('mach_9/wsl');
    pinComputerIdentity('mach_9/wsl', 'MFkwKEEP');
    const reload = vi.fn();
    vi.spyOn(window, 'location', 'get').mockReturnValue({ ...window.location, reload } as Location);
    render(<PrivateRelayIdentityWarning />);
    announce('mach_1/wsl');
    fireEvent.click(screen.getByRole('button', { name: 'Forget the old key and approve again' }));
    expect(pinnedComputerIdentity('mach_1/wsl')).toBeNull();
    expect(deviceEnrolledFor('mach_1/wsl')).toBe(false);
    expect(pinnedComputerIdentity('mach_9/wsl')).toBe('MFkwKEEP');
    expect(deviceEnrolledFor('mach_9/wsl')).toBe(true);
    expect(reload).toHaveBeenCalled();
  });
});
