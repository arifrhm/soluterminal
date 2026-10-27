import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import type { Session } from '@supabase/supabase-js';
import { api } from '../api';
import { useCloud, type CloudMachine } from '../cloud';
import { CloudGate } from './CloudGate';
import { HEARD_ABOUT_QUESTION } from './HeardAboutPrompt';

// The new-account card asks once, optionally, how the user heard about
// SoluTerminal; the answer lands in user_metadata.heard_about.

const WINDOWS_UA = 'Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 Chrome/130 Safari/537.36';
const machine: CloudMachine = { id: 'mach_1', name: 'desk', online: true, backends: ['host'] };
const TITLE = "You're in. Three steps to your first team.";

function seed(opts: { machines?: CloudMachine[]; meta?: Record<string, unknown> } = {}) {
  const saveHeardAbout = vi.fn(async (_answer: string) => {});
  useCloud.setState({
    mode: 'cloud',
    isLocal: false,
    session: {
      access_token: 'jwt-new',
      user: { id: 'user-n', email: 'n@example.com', user_metadata: opts.meta ?? { signup_app: 'soluterminal' } },
    } as unknown as Session,
    authorised: true,
    machines: opts.machines ?? [],
    machinesLoaded: true,
    selectedMachineId: null,
    error: null,
    bootstrap: vi.fn(async () => {}),
    signOut: vi.fn(async () => {}),
    refreshMachines: vi.fn(async () => {}),
    saveHeardAbout,
  });
  return saveHeardAbout;
}

const renderGate = () => render(<CloudGate><div>app</div></CloudGate>);
const question = () => screen.queryByRole('group', { name: HEARD_ABOUT_QUESTION });

beforeEach(() => {
  useCloud.setState(useCloud.getInitialState(), true);
  localStorage.clear();
  sessionStorage.clear();
  vi.spyOn(navigator, 'userAgent', 'get').mockReturnValue(WINDOWS_UA);
  vi.spyOn(navigator, 'platform', 'get').mockReturnValue('Win32');
  Object.defineProperty(navigator, 'maxTouchPoints', { configurable: true, value: 0 });
  vi.spyOn(globalThis, 'fetch').mockImplementation(async (input) => {
    if (String(input).includes('/api/entitlements')) {
      return new Response(JSON.stringify({ tier: 'account', team: false }), { status: 200 });
    }
    return new Response('{}', { status: 404 });
  });
  // The product-updates ask already has its choice; keep it off the card.
  vi.spyOn(api, 'getMarketingConsent').mockResolvedValue({ consent: false, updated_at: 'then' });
});

afterEach(() => {
  vi.restoreAllMocks();
  localStorage.clear();
  useCloud.setState(useCloud.getInitialState(), true);
});

describe('"How did you hear" on the new-account card', () => {
  it('offers the six answers and Skip, and saves one tap', async () => {
    const save = seed();
    const view = renderGate();
    expect(screen.getByRole('heading', { name: TITLE })).toBeInTheDocument();
    expect(question()).not.toBeNull();
    for (const label of ['Hacker News', 'Reddit', 'X', 'A friend', 'Search', 'Other', 'Skip']) {
      expect(screen.getByRole('button', { name: label })).toBeInTheDocument();
    }
    fireEvent.click(screen.getByRole('button', { name: 'A friend' }));
    expect(save).toHaveBeenCalledWith('friend');
    await waitFor(() => expect(question()).toBeNull());

    // Not again on this device, even before the session shows the answer.
    view.unmount();
    renderGate();
    expect(screen.getByRole('heading', { name: TITLE })).toBeInTheDocument();
    expect(question()).toBeNull();
    expect(save).toHaveBeenCalledTimes(1);
  });

  it('records Skip as skipped, and a failed save stays hidden', async () => {
    const save = seed();
    save.mockRejectedValueOnce(new Error('offline'));
    renderGate();
    fireEvent.click(screen.getByRole('button', { name: 'Skip' }));
    expect(save).toHaveBeenCalledWith('skipped');
    await waitFor(() => expect(question()).toBeNull());
  });

  it('never asks an account that already answered or skipped', () => {
    seed({ meta: { signup_app: 'soluterminal', heard_about: 'skipped' } });
    renderGate();
    expect(screen.getByRole('heading', { name: TITLE })).toBeInTheDocument();
    expect(question()).toBeNull();
  });

  it('never asks a returning user with machines', () => {
    seed({ machines: [machine] });
    renderGate();
    expect(screen.getByRole('heading', { name: 'Your machines' })).toBeInTheDocument();
    expect(question()).toBeNull();
  });
});
