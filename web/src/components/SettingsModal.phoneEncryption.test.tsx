import { cleanup, render, screen } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';

const view = vi.hoisted(() => ({ mobile: true, candidate: null as string | null }));
vi.mock('./useIsMobileView', async (importOriginal) => ({
  ...(await importOriginal<typeof import('./useIsMobileView')>()),
  useIsMobileDevice: () => view.mobile,
}));
vi.mock('./settings/RemoteDevicesSetting', async (importOriginal) => ({
  ...(await importOriginal<typeof import('./settings/RemoteDevicesSetting')>()),
  privateRelayCandidateInView: () => view.candidate,
  PhoneEncryptionSetting: () => <div>phone encryption switch</div>,
}));
vi.mock('./settings/DevicesTab', () => ({ DevicesTab: () => <div>full devices tab</div> }));

import { useSettings } from '../settings';
import { SettingsModal } from './SettingsModal';

// A phone browser has no Devices tab, except for its end-to-end encryption
// switch when that applies (the computer approved it and its key is pinned).
describe('SettingsModal on a phone', () => {
  afterEach(() => {
    cleanup();
    useSettings.setState({ open: false });
  });

  it('hides Devices when encryption does not apply', () => {
    view.mobile = true;
    view.candidate = null;
    useSettings.setState({ open: true, tab: 'devices' });
    render(<SettingsModal />);
    expect(screen.queryByRole('tab', { name: 'Devices' })).toBeNull();
    expect(screen.queryByText('phone encryption switch')).toBeNull();
  });

  it('shows Devices with only the encryption switch when it applies', () => {
    view.mobile = true;
    view.candidate = 'mach_1/wsl';
    useSettings.setState({ open: true, tab: 'devices' });
    render(<SettingsModal />);
    expect(screen.getByRole('tab', { name: 'Devices' })).toBeInTheDocument();
    expect(screen.getByText('phone encryption switch')).toBeInTheDocument();
    expect(screen.queryByText('full devices tab')).toBeNull();
  });

  it('keeps the full Devices tab on a computer', () => {
    view.mobile = false;
    view.candidate = 'mach_1/wsl';
    useSettings.setState({ open: true, tab: 'devices' });
    render(<SettingsModal />);
    expect(screen.getByText('full devices tab')).toBeInTheDocument();
    expect(screen.queryByText('phone encryption switch')).toBeNull();
  });
});
