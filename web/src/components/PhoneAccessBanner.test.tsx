import { render, screen, waitFor } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { PhoneAccessBanner } from './PhoneAccessBanner';
import { useEntitlements } from '../entitlements';
import { resetRelayConfiguredForTests } from '../lib/relayConfigured';

function cloudStatus(configured: boolean) {
  return {
    configured,
    hasCreds: false,
    credsInvalid: false,
    claimCorrupt: false,
    relayState: configured ? 'offline' : 'unconfigured',
  };
}

function stubStatus(configured: boolean) {
  const fetchMock = vi.fn(async () => new Response(JSON.stringify(cloudStatus(configured)), {
    status: 200,
    headers: { 'Content-Type': 'application/json' },
  }));
  vi.stubGlobal('fetch', fetchMock);
  return fetchMock;
}

describe('PhoneAccessBanner', () => {
  beforeEach(() => {
    localStorage.clear();
    Object.defineProperty(window, '__SOLU_TAURI__', { value: true, configurable: true });
    // A team verdict carries the account features phone access needs.
    useEntitlements.setState({ loaded: true, team: true, operator: false, account: true });
  });

  afterEach(() => {
    delete (window as unknown as { __SOLU_TAURI__?: boolean }).__SOLU_TAURI__;
    useEntitlements.setState({ loaded: false, team: false, operator: false, account: false });
    resetRelayConfiguredForTests();
    vi.unstubAllGlobals();
  });

  it('offers phone access setup when the daemon has a relay', async () => {
    stubStatus(true);
    render(<PhoneAccessBanner />);
    expect(await screen.findByText(/Control this computer from your phone/)).toBeInTheDocument();
  });

  it('offers nothing when the daemon has no relay (a build without a hosted cloud)', async () => {
    const fetchMock = stubStatus(false);
    render(<PhoneAccessBanner />);
    await waitFor(() => expect(fetchMock).toHaveBeenCalled());
    // Let the status land, then confirm the banner stays away.
    await new Promise((resolve) => setTimeout(resolve, 20));
    expect(screen.queryByText(/Control this computer from your phone/)).toBeNull();
  });
});
