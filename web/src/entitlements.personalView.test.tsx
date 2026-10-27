import { act, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { api } from './api';
import { useCloud } from './cloud';
import {
  applyPersonalView,
  operatorFeaturesAllowed,
  teamFeaturesAllowed,
  useEntitlements,
  type EntitlementsVerdict,
} from './entitlements';
import { PersonalViewPill } from './components/PersonalViewPill';
import { PersonalViewSetting } from './components/settings/PersonalViewSetting';

const OPERATOR: EntitlementsVerdict = {
  tier: 'team',
  team: true,
  operator: true,
  account: true,
  accountSwitching: true,
  teamCredentials: true,
  smartApprovals: true,
};

function resetStore() {
  useEntitlements.setState({
    loaded: false,
    tier: 'none',
    team: false,
    operator: false,
    account: false,
    accountSwitching: false,
    teamCredentials: false,
    smartApprovals: null,
    viewPersonal: false,
    real: { tier: 'none', team: false, operator: false, account: false, accountSwitching: false, teamCredentials: false, smartApprovals: null },
  });
}

beforeEach(() => {
  useCloud.setState({ session: null, withoutAccount: false, isLocal: true });
  resetStore();
});

afterEach(() => {
  vi.restoreAllMocks();
  resetStore();
});

describe('applyPersonalView', () => {
  it('shows an operator install as a free account', () => {
    expect(applyPersonalView(OPERATOR, true)).toEqual({
      tier: 'account',
      team: false,
      operator: false,
      account: true,
      accountSwitching: false,
      teamCredentials: false,
      smartApprovals: true,
    });
  });

  it('leaves the verdict alone when off or when there is nothing to hide', () => {
    expect(applyPersonalView(OPERATOR, false)).toBe(OPERATOR);
    const free: EntitlementsVerdict = { ...OPERATOR, tier: 'account', team: false, operator: false, accountSwitching: false, teamCredentials: false };
    expect(applyPersonalView(free, true)).toBe(free);
  });
});

describe('operator and team flags', () => {
  it('follows team for daemons from before the split', async () => {
    vi.spyOn(api, 'getEntitlements').mockResolvedValue({ tier: 'team', team: true, account: true, accountSwitching: true, teamCredentials: true });
    await useEntitlements.getState().load();
    expect(operatorFeaturesAllowed()).toBe(true);
    expect(teamFeaturesAllowed()).toBe(true);
  });

  it('keeps operator tooling off for a team-plan member who is not an operator', async () => {
    vi.spyOn(api, 'getEntitlements').mockResolvedValue({
      tier: 'team', team: true, operator: false, account: true, accountSwitching: true, teamCredentials: false,
    });
    await useEntitlements.getState().load();
    expect(teamFeaturesAllowed()).toBe(true);
    expect(operatorFeaturesAllowed()).toBe(false);
  });

  it('applies the personal view the daemon reports and keeps the real verdict', async () => {
    vi.spyOn(api, 'getEntitlements').mockResolvedValue({
      tier: 'team', team: true, operator: true, account: true, accountSwitching: true, teamCredentials: true, viewPersonal: true,
    });
    await useEntitlements.getState().load();
    const s = useEntitlements.getState();
    expect(s.viewPersonal).toBe(true);
    expect(s.team).toBe(false);
    expect(s.operator).toBe(false);
    expect(s.tier).toBe('account');
    expect(s.real.operator).toBe(true);
  });

  it('ignores a personal view on an install with nothing to hide', async () => {
    vi.spyOn(api, 'getEntitlements').mockResolvedValue({
      tier: 'account', team: false, operator: false, account: true, accountSwitching: false, teamCredentials: false, viewPersonal: true,
    });
    await useEntitlements.getState().load();
    expect(useEntitlements.getState().viewPersonal).toBe(false);
  });
});

describe('personal view controls', () => {
  it('switches the view from Settings and reloads the verdict', async () => {
    let personal = false;
    vi.spyOn(api, 'setPersonalView').mockImplementation(async (on: boolean) => {
      personal = on;
      return { personal: on, available: true };
    });
    vi.spyOn(api, 'getEntitlements').mockImplementation(async () => ({ ...OPERATOR, smartApprovals: true, viewPersonal: personal }));
    await useEntitlements.getState().load();
    render(<PersonalViewSetting />);
    fireEvent.click(screen.getByLabelText('View as personal'));
    await waitFor(() => expect(useEntitlements.getState().viewPersonal).toBe(true));
    expect(api.setPersonalView).toHaveBeenCalledWith(true);
    expect(useEntitlements.getState().operator).toBe(false);
  });

  it('hides the Settings switch on installs with nothing to hide', async () => {
    vi.spyOn(api, 'getEntitlements').mockResolvedValue({ tier: 'account', team: false, account: true, accountSwitching: false, teamCredentials: false });
    await useEntitlements.getState().load();
    render(<PersonalViewSetting />);
    expect(screen.queryByLabelText('View as personal')).toBeNull();
  });

  it('shows the pill only in the personal view, and one click leaves it', async () => {
    let personal = true;
    vi.spyOn(api, 'setPersonalView').mockImplementation(async (on: boolean) => {
      personal = on;
      return { personal: on, available: true };
    });
    vi.spyOn(api, 'getEntitlements').mockImplementation(async () => ({ ...OPERATOR, smartApprovals: true, viewPersonal: personal }));
    await useEntitlements.getState().load();
    render(<PersonalViewPill />);
    const pill = screen.getByRole('button', { name: /Personal view/ });
    await act(async () => { fireEvent.click(pill); });
    await waitFor(() => expect(screen.queryByRole('button', { name: /Personal view/ })).toBeNull());
    expect(api.setPersonalView).toHaveBeenCalledWith(false);
    expect(useEntitlements.getState().operator).toBe(true);
  });
});
