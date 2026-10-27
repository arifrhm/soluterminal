import { render, screen } from '@testing-library/react';
import type { Session } from '@supabase/supabase-js';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { api } from './api';
import { useCloud } from './cloud';
import {
  accountFeaturesAllowed,
  useAccountFeatures,
  useEntitlements,
  usePhoneAccessAllowed,
  useTeamFeatures,
} from './entitlements';

function session(extra: Record<string, unknown> = {}): Session {
  return {
    access_token: 'tok',
    refresh_token: 'r',
    expires_in: 3600,
    token_type: 'bearer',
    user: { id: 'u1', app_metadata: {}, user_metadata: {}, aud: 'authenticated', created_at: '', ...extra },
  } as unknown as Session;
}

function Probe() {
  const account = useAccountFeatures();
  const phone = usePhoneAccessAllowed();
  const team = useTeamFeatures();
  return <div>{`account=${account} phone=${phone} team=${team}`}</div>;
}

function verdictText(): string {
  render(<Probe />);
  return screen.getByText(/account=/).textContent ?? '';
}

beforeEach(() => {
  useCloud.setState({ session: null, withoutAccount: false, isLocal: true });
  useEntitlements.setState({ loaded: false, tier: 'none', team: false, operator: false, account: false });
});

afterEach(() => {
  vi.restoreAllMocks();
  useCloud.setState({ session: null, withoutAccount: false });
  useEntitlements.setState({ loaded: false, tier: 'none', team: false, operator: false, account: false, smartApprovals: null });
});

describe('entitlements load', () => {
  it('reads the tier fields from a current daemon', async () => {
    vi.spyOn(api, 'getEntitlements').mockResolvedValue({
      tier: 'account',
      team: false,
      account: true,
      accountSwitching: false,
      teamCredentials: false,
      smartApprovals: true,
    });
    await useEntitlements.getState().load();
    const s = useEntitlements.getState();
    expect(s).toMatchObject({ loaded: true, tier: 'account', team: false, account: true, smartApprovals: true });
  });

  it('derives the tier from a daemon that only reports team', async () => {
    vi.spyOn(api, 'getEntitlements').mockResolvedValue({
      team: false,
      accountSwitching: false,
      teamCredentials: false,
    });
    await useEntitlements.getState().load();
    expect(useEntitlements.getState()).toMatchObject({ tier: 'none', account: false, smartApprovals: null });
  });

  it('treats a daemon without the endpoint as the free local app, never as team', async () => {
    vi.spyOn(api, 'getEntitlements').mockResolvedValue(null);
    await useEntitlements.getState().load();
    expect(useEntitlements.getState()).toMatchObject({
      loaded: true, tier: 'none', team: false, account: false, accountSwitching: false, teamCredentials: false,
    });
  });

  it('keeps team surfaces off before the first answer and after a failed one', async () => {
    useEntitlements.setState({ loaded: false, team: false, operator: false, accountSwitching: false, teamCredentials: false });
    vi.spyOn(api, 'getEntitlements').mockRejectedValue(new Error('offline'));
    await useEntitlements.getState().load();
    expect(useEntitlements.getState()).toMatchObject({ loaded: false, team: false, accountSwitching: false, teamCredentials: false });
  });
});

describe('tier gating', () => {
  it('none: no account features, no team features', () => {
    useEntitlements.setState({ loaded: true, tier: 'none', team: false, operator: false, account: false });
    expect(verdictText()).toBe('account=false phone=false team=false');
    expect(accountFeaturesAllowed()).toBe(false);
  });

  it('account: phone, push and voice, but not team surfaces', () => {
    useEntitlements.setState({ loaded: true, tier: 'account', team: false, operator: false, account: true });
    expect(verdictText()).toBe('account=true phone=true team=false');
    expect(accountFeaturesAllowed()).toBe(true);
  });

  it('team: everything', () => {
    useEntitlements.setState({ loaded: true, tier: 'team', team: true, operator: true, account: true });
    expect(verdictText()).toBe('account=true phone=true team=true');
  });

  it('a signed-in session unlocks account features before the daemon verdict arrives', () => {
    useCloud.setState({ session: session() });
    expect(verdictText()).toBe('account=true phone=true team=false');
    expect(accountFeaturesAllowed()).toBe(true);
  });

  it('an anonymous session does not count as an account', () => {
    useCloud.setState({ session: session({ is_anonymous: true }) });
    expect(verdictText()).toBe('account=false phone=false team=false');
  });

  it('skipping sign-in hides account features even if the daemon reports an account', () => {
    useCloud.setState({ withoutAccount: true, session: null });
    useEntitlements.setState({ loaded: true, tier: 'account', team: false, operator: false, account: true });
    expect(verdictText()).toBe('account=false phone=false team=false');
    expect(accountFeaturesAllowed()).toBe(false);
  });

  it('a team install keeps everything without a browser session', () => {
    useCloud.setState({ withoutAccount: true, session: null });
    useEntitlements.setState({ loaded: true, tier: 'team', team: true, operator: true, account: true });
    expect(verdictText()).toBe('account=true phone=true team=true');
  });

  it('hides account features while the verdict is still loading', () => {
    useEntitlements.setState({ loaded: false, tier: 'none', team: false, operator: false, account: true });
    expect(verdictText()).toBe('account=false phone=false team=false');
  });
});
