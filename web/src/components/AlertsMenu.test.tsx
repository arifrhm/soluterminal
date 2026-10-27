import { fireEvent, render, screen } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { AlertsMenu } from './AlertsMenu';
import { useEntitlements } from '../entitlements';
import { useCloud } from '../cloud';
import { useStore } from '../store';

const IPHONE_UA =
  'Mozilla/5.0 (iPhone; CPU iPhone OS 18_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.0 Mobile/15E148 Safari/604.1';

function openMenu() {
  render(<AlertsMenu />);
  fireEvent.click(screen.getByRole('button', { name: /alerts/i }));
}

beforeEach(() => {
  useCloud.setState({ session: null, withoutAccount: false });
  useStore.setState({ notifyEnabled: true });
});

afterEach(() => {
  vi.restoreAllMocks();
  useEntitlements.setState({ loaded: false, team: false, operator: false, account: false });
  useStore.setState({ notifyEnabled: false });
});

describe('AlertsMenu push by tier', () => {
  it('no account: keeps the bell but offers no phone push test or iPhone hint', () => {
    vi.spyOn(navigator, 'userAgent', 'get').mockReturnValue(IPHONE_UA);
    useEntitlements.setState({ loaded: true, team: false, operator: false, account: false });
    openMenu();
    expect(screen.getByText('Notifications')).toBeInTheDocument();
    expect(screen.queryByText('Test & setup')).toBeNull();
    expect(screen.queryByText(/add SoluTerminal to your Home Screen/)).toBeNull();
  });

  it('free account: offers the push test', () => {
    useEntitlements.setState({ loaded: true, team: false, operator: false, account: true });
    openMenu();
    expect(screen.getByText('Test & setup')).toBeInTheDocument();
  });

  it('free account on an iPhone browser tab: explains the Home Screen requirement', () => {
    vi.spyOn(navigator, 'userAgent', 'get').mockReturnValue(IPHONE_UA);
    useEntitlements.setState({ loaded: true, team: false, operator: false, account: true });
    openMenu();
    expect(
      screen.getByText('On iPhone, add SoluTerminal to your Home Screen to get push alerts.'),
    ).toBeInTheDocument();
  });

  it('no iPhone hint once launched from the Home Screen', () => {
    vi.spyOn(navigator, 'userAgent', 'get').mockReturnValue(IPHONE_UA);
    (navigator as unknown as { standalone?: boolean }).standalone = true;
    try {
      useEntitlements.setState({ loaded: true, team: true, operator: true, account: true });
      openMenu();
      expect(screen.queryByText(/add SoluTerminal to your Home Screen/)).toBeNull();
    } finally {
      delete (navigator as unknown as { standalone?: boolean }).standalone;
    }
  });
});
