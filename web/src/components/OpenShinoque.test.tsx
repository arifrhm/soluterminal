import { fireEvent, render, screen } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { Session } from '@supabase/supabase-js';
import { configureApi, currentRoutingScope, setBackend } from '../api';
import { useCloud } from '../cloud';
import { OpenShinoque, shinoqueRelayURL } from './OpenShinoque';

const health = vi.hoisted(() => ({ current: null as null | { shinoqueRelay: boolean; backendID?: string }, scopeKey: null as string | null }));
vi.mock('../lib/useDaemonHealth', () => ({ useDaemonHealth: () => ({ health: health.current, healthScopeKey: health.scopeKey }) }));

describe('Open shinoque', () => {
  beforeEach(() => {
    useCloud.setState(useCloud.getInitialState(), true);
    configureApi({ prefix: '/m/machine_1', token: 'jwt token/+' });
    setBackend(null);
    useCloud.setState({
      mode: 'cloud', isLocal: false, selectedMachineId: 'machine_1',
      session: { access_token: 'jwt token/+' } as Session,
    });
    health.current = { shinoqueRelay: true };
    health.scopeKey = currentRoutingScope().key;
  });

  afterEach(() => {
    configureApi({ prefix: '', token: null });
    setBackend(null);
    useCloud.setState(useCloud.getInitialState(), true);
  });

  it('links to the selected machine without putting the JWT in the URL', () => {
    render(<OpenShinoque />);
    expect(screen.getByRole('link', { name: 'Open shinoque' })).toHaveAttribute(
      'href', '/m/machine_1/shinoque/',
    );
  });

  it('mints a single-use ticket on click and navigates with it, not the JWT', async () => {
    const fetchMock = vi.fn(async () => new Response(JSON.stringify({ ticket: 'rt1.click' }), { status: 200 }));
    vi.stubGlobal('fetch', fetchMock);
    const assign = vi.fn();
    vi.stubGlobal('location', { ...window.location, assign });
    try {
      render(<OpenShinoque />);
      fireEvent.click(screen.getByRole('link', { name: 'Open shinoque' }));
      await vi.waitFor(() => expect(assign).toHaveBeenCalledWith('/m/machine_1/shinoque/?ticket=rt1.click'));
      const [, init] = fetchMock.mock.calls[0] as unknown as [string, RequestInit];
      expect(new Headers(init.headers).get('Authorization')).toBe('Bearer jwt token/+');
    } finally {
      vi.unstubAllGlobals();
    }
  });

  it('includes the active backend when selected', () => {
    setBackend('wsl');
    health.current = { shinoqueRelay: true, backendID: 'wsl' };
    health.scopeKey = currentRoutingScope().key;
    render(<OpenShinoque />);
    expect(screen.getByRole('link', { name: 'Open shinoque' })).toHaveAttribute(
      'href', '/m/machine_1/b/wsl/shinoque/',
    );
  });

  it('hides when unavailable, local, or missing a JWT', () => {
    health.current = { shinoqueRelay: false };
    const { rerender } = render(<OpenShinoque />);
    expect(screen.queryByRole('link')).toBeNull();
    health.current = { shinoqueRelay: true };
    useCloud.setState({ isLocal: true });
    rerender(<OpenShinoque />);
    expect(screen.queryByRole('link')).toBeNull();
    useCloud.setState({ isLocal: false });
    configureApi({ token: null });
    rerender(<OpenShinoque />);
    expect(screen.queryByRole('link')).toBeNull();
  });

  it('rejects a stale or invalid routing scope', () => {
    expect(shinoqueRelayURL('machine_1', { prefix: '/m/other', backendPrefix: '', key: '' }, 'jwt')).toBeNull();
    expect(shinoqueRelayURL('machine_1', { prefix: '/m/machine_1', backendPrefix: '/b/wsl/extra', key: '' }, 'jwt')).toBeNull();
  });

  it('hides health from the previously selected machine', () => {
    configureApi({ prefix: '/m/another_machine' });
    useCloud.setState({ selectedMachineId: 'another_machine' });
    render(<OpenShinoque />);
    expect(screen.queryByRole('link', { name: 'Open shinoque' })).toBeNull();
  });

  it('hides an available fallback backend when the selected backend is offline', () => {
    setBackend('wsl');
    health.scopeKey = currentRoutingScope().key;
    health.current = { shinoqueRelay: true, backendID: 'win' };
    render(<OpenShinoque />);
    expect(screen.queryByRole('link', { name: 'Open shinoque' })).toBeNull();
  });
});
