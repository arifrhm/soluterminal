import { fireEvent, render, screen } from '@testing-library/react';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import type { BundleParityState } from '../lib/bundleParity';
import { beginBundleReloadAttempt, classifyBundleParity } from '../lib/bundleParity';

let bundleState: BundleParityState;
let lastOkMs: number;
let health: { status: string; version: string } | null;
const reloadApp = vi.fn();
vi.mock('../lib/useDaemonHealth', () => ({
  useDaemonHealth: () => ({
    health,
    lastOkMs,
    polling: false,
    bundleState,
  }),
  refreshDaemonHealth: vi.fn(),
}));
vi.mock('../store', () => ({
  useStore: (selector: (state: unknown) => unknown) => selector({ updateStatus: null, reloadApp }),
}));

import { DaemonHealthBanner } from './DaemonHealthBanner';

const state = (kind: BundleParityState['kind'], over: Partial<BundleParityState> = {}): BundleParityState => ({
  kind,
  runningBundleVersion: '0.0.991',
  shellDaemonVersion: '0.0.992',
  shellWebVersion: '0.0.992',
  targetVersion: '0.0.992',
  legacyStaleVersion: '',
  ...over,
});

describe('DaemonHealthBanner bundle states', () => {
  beforeEach(() => {
    vi.restoreAllMocks();
    sessionStorage.clear();
    reloadApp.mockClear();
    bundleState = state('ok');
    lastOkMs = Date.now();
    health = { status: 'ok', version: bundleState.shellDaemonVersion };
  });

  it('offers Reload only when the shell origin serves newer bytes', () => {
    bundleState = state('transient-stale');
    render(<DaemonHealthBanner />);
    fireEvent.click(screen.getByRole('button', { name: 'Reload' }));
    expect(reloadApp).toHaveBeenCalledWith('0.0.992');
  });

  it('shows quit-and-reopen only after bounded transient retries are exhausted', () => {
    bundleState = state('transient-stale');
    for (let i = 0; i < 3; i += 1) beginBundleReloadAttempt('0.0.992');
    render(<DaemonHealthBanner />);
    expect(screen.getByText(/quit and reopen SoluTerminal/i)).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Reload' })).toBeNull();
  });

  it('explains an activation gap without give-up language or Reload', () => {
    bundleState = state('activation-gap', {
      shellDaemonVersion: '0.0.991',
      shellWebVersion: '0.0.991',
      targetVersion: '0.0.992',
    });
    render(<DaemonHealthBanner />);
    expect(screen.getByText(/finishing the update on the shell backend/i)).toBeInTheDocument();
    expect(screen.getByText(/will reload automatically/i)).toBeInTheDocument();
    expect(screen.queryByText(/quit and reopen/i)).toBeNull();
    expect(screen.queryByRole('button', { name: 'Reload' })).toBeNull();
  });

  it('names a packaged mismatch and gives only the reinstall remedy', () => {
    bundleState = state('packaged-mismatch', { shellWebVersion: '0.0.991' });
    render(<DaemonHealthBanner />);
    expect(screen.getByText(/package contains an older interface/i)).toBeInTheDocument();
    expect(screen.getByText(/web 0\.0\.991, daemon 0\.0\.992/i)).toBeInTheDocument();
    expect(screen.getByText(/reinstall SoluTerminal from the latest installer/i)).toBeInTheDocument();
    expect(screen.queryByText(/quit and reopen/i)).toBeNull();
    expect(screen.queryByRole('button', { name: 'Reload' })).toBeNull();
  });

  it.each(['dev', '0.0.0', '', 'dev-abc123'])(
    'renders no package warning when the daemon reports dev-like version %j',
    (shellDaemonVersion) => {
      bundleState = classifyBundleParity({
        runningBundleVersion: '0.0.1047',
        shellDaemonVersion,
        shellWebVersion: '0.0.1047',
      });
      health = { status: 'ok', version: shellDaemonVersion };
      const { container } = render(<DaemonHealthBanner />);
      expect(container.querySelector('.daemon-banner.is-package-warning')).toBeNull();
      expect(container.querySelector('.daemon-banner')).toBeNull();
    },
  );

  it('still names a genuine packaged mismatch', () => {
    bundleState = classifyBundleParity({
      runningBundleVersion: '0.0.1047',
      shellDaemonVersion: '0.0.990',
      shellWebVersion: '0.0.1047',
    });
    health = { status: 'ok', version: '0.0.990' };
    const { container } = render(<DaemonHealthBanner />);
    expect(container.querySelector('.daemon-banner.is-package-warning')).not.toBeNull();
    expect(screen.getByText(/web 0\.0\.1047, daemon 0\.0\.990/i)).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Dismiss' })).toBeInTheDocument();
  });

  it('combines the packaged-artifact remedy with a later daemon outage', () => {
    bundleState = state('packaged-mismatch', { shellWebVersion: '0.0.991' });
    health = null;
    lastOkMs = Date.now() - 60_000;
    render(<DaemonHealthBanner />);
    expect(screen.getByText(/reinstall SoluTerminal from the latest installer/i)).toBeInTheDocument();
    expect(screen.getByText(/Daemon unreachable/i)).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Retry now' })).toBeInTheDocument();
  });

  it('combines activation progress with a later daemon outage', () => {
    bundleState = state('activation-gap', {
      shellDaemonVersion: '0.0.991',
      shellWebVersion: '0.0.991',
      targetVersion: '0.0.992',
    });
    health = null;
    lastOkMs = Date.now() - 60_000;
    render(<DaemonHealthBanner />);
    expect(screen.getByText(/finishing the update on the shell backend/i)).toBeInTheDocument();
    expect(screen.getByText(/Daemon unreachable/i)).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Retry now' })).toBeInTheDocument();
  });
});
