import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import type { Session } from '@supabase/supabase-js';
import { api } from '../api';
import { useCloud, type CloudMachine } from '../cloud';
import { consentAskedAtSignup, MARKETING_CONSENT_LABEL } from '../marketingConsent';
import { CloudGate } from './CloudGate';

// Google sign-ups skip the code step's product-updates box, so the
// new-account card asks once, only when no choice is recorded.

const WINDOWS_UA = 'Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 Chrome/130 Safari/537.36';
const machine: CloudMachine = { id: 'mach_1', name: 'desk', online: true, backends: ['host'] };
const NEVER_ASKED = { consent: null, updated_at: null };

function seed(machines: CloudMachine[] = []) {
  useCloud.setState({
    mode: 'cloud',
    isLocal: false,
    session: { access_token: 'jwt-google', user: { id: 'user-g', email: 'g@example.com' } } as Session,
    authorised: true,
    machines,
    machinesLoaded: true,
    selectedMachineId: null,
    error: null,
    bootstrap: vi.fn(async () => {}),
    signOut: vi.fn(async () => {}),
    refreshMachines: vi.fn(async () => {}),
  });
}

function stubFetch() {
  vi.spyOn(globalThis, 'fetch').mockImplementation(async (input) => {
    if (String(input).includes('/api/entitlements')) {
      return new Response(JSON.stringify({ tier: 'account', team: false }), { status: 200 });
    }
    return new Response('{}', { status: 404 });
  });
}

const renderGate = () => render(<CloudGate><div>app</div></CloudGate>);
const box = () => screen.queryByLabelText(MARKETING_CONSENT_LABEL) as HTMLInputElement | null;

beforeEach(() => {
  useCloud.setState(useCloud.getInitialState(), true);
  localStorage.clear();
  sessionStorage.clear();
  vi.spyOn(navigator, 'userAgent', 'get').mockReturnValue(WINDOWS_UA);
  vi.spyOn(navigator, 'platform', 'get').mockReturnValue('Win32');
  Object.defineProperty(navigator, 'maxTouchPoints', { configurable: true, value: 0 });
  stubFetch();
});

afterEach(() => {
  delete (window as unknown as Record<string, unknown>).__TAURI_INTERNALS__;
  vi.restoreAllMocks();
  localStorage.clear();
  useCloud.setState(useCloud.getInitialState(), true);
});

describe('product-updates ask on the new-account card', () => {
  it('asks an account with no recorded choice, unticked, and saves a yes as a web sign-up', async () => {
    const get = vi.spyOn(api, 'getMarketingConsent').mockResolvedValue(NEVER_ASKED);
    const post = vi.spyOn(api, 'postMarketingConsent').mockResolvedValue({ consent: true, updated_at: 'now' });
    seed();
    const view = renderGate();

    expect(screen.getByRole('heading', { name: "You're in. Three steps to your first team." })).toBeInTheDocument();
    await waitFor(() => expect(box()).not.toBeNull());
    expect(get).toHaveBeenCalledWith('jwt-google');
    expect(box()?.checked).toBe(false);
    fireEvent.click(box() as HTMLInputElement);
    fireEvent.click(screen.getByRole('button', { name: 'Save' }));
    await waitFor(() =>
      expect(post).toHaveBeenCalledWith(
        { consent: true, source: 'web-signup', wording_version: '2026-10-23-v1' },
        'jwt-google',
      ),
    );
    expect(await screen.findByRole('status')).toHaveTextContent('Saved. You can change this in Settings.');
    expect(box()).toBeNull();

    // Not again, even before the server's answer changes.
    view.unmount();
    renderGate();
    await screen.findByRole('heading', { name: "You're in. Three steps to your first team." });
    expect(box()).toBeNull();
    expect(get).toHaveBeenCalledTimes(1);
  });

  it('saves an unticked box as no', async () => {
    vi.spyOn(api, 'getMarketingConsent').mockResolvedValue(NEVER_ASKED);
    const post = vi.spyOn(api, 'postMarketingConsent').mockResolvedValue({ consent: false, updated_at: 'now' });
    seed();
    renderGate();
    await waitFor(() => expect(box()).not.toBeNull());
    fireEvent.click(screen.getByRole('button', { name: 'Save' }));
    await waitFor(() =>
      expect(post).toHaveBeenCalledWith(
        { consent: false, source: 'web-signup', wording_version: '2026-10-23-v1' },
        'jwt-google',
      ),
    );
  });

  it('records a dismiss as no and never shows the box again', async () => {
    const get = vi.spyOn(api, 'getMarketingConsent').mockResolvedValue(NEVER_ASKED);
    const post = vi.spyOn(api, 'postMarketingConsent').mockRejectedValue(new Error('offline'));
    seed();
    const view = renderGate();
    await waitFor(() => expect(box()).not.toBeNull());
    fireEvent.click(screen.getByRole('button', { name: 'Dismiss' }));
    await waitFor(() => expect(box()).toBeNull());
    expect(post).toHaveBeenCalledWith(
      { consent: false, source: 'web-signup', wording_version: '2026-10-23-v1' },
      'jwt-google',
    );
    expect(screen.queryByRole('status')).toBeNull();

    // The save failed, but this device still remembers the dismiss.
    view.unmount();
    renderGate();
    await screen.findByRole('heading', { name: "You're in. Three steps to your first team." });
    expect(box()).toBeNull();
    expect(get).toHaveBeenCalledTimes(1);
  });

  it('stays away when a choice is already recorded, or the lookup fails', async () => {
    const get = vi
      .spyOn(api, 'getMarketingConsent')
      .mockResolvedValueOnce({ consent: false, source: 'web-signup', updated_at: 'then' })
      .mockRejectedValueOnce(new Error('offline'));
    seed();
    const view = renderGate();
    await waitFor(() => expect(get).toHaveBeenCalledTimes(1));
    await new Promise((r) => setTimeout(r, 0));
    expect(box()).toBeNull();
    view.unmount();

    renderGate();
    await waitFor(() => expect(get).toHaveBeenCalledTimes(2));
    await new Promise((r) => setTimeout(r, 0));
    expect(box()).toBeNull();
  });

  it('never asks a returning user with machines', async () => {
    const get = vi.spyOn(api, 'getMarketingConsent').mockResolvedValue(NEVER_ASKED);
    seed([machine]);
    renderGate();
    expect(screen.getByRole('heading', { name: 'Your machines' })).toBeInTheDocument();
    await new Promise((r) => setTimeout(r, 0));
    expect(get).not.toHaveBeenCalled();
    expect(box()).toBeNull();
  });

  it('records a desktop-app window sign-up as app-signup', async () => {
    Object.defineProperty(window, '__TAURI_INTERNALS__', { configurable: true, value: {} });
    vi.spyOn(api, 'getMarketingConsent').mockResolvedValue(NEVER_ASKED);
    const post = vi.spyOn(api, 'postMarketingConsent').mockResolvedValue({ consent: false, updated_at: 'now' });
    seed();
    renderGate();
    await waitFor(() => expect(box()).not.toBeNull());
    fireEvent.click(screen.getByRole('button', { name: 'Save' }));
    await waitFor(() =>
      expect(post).toHaveBeenCalledWith(
        { consent: false, source: 'app-signup', wording_version: '2026-10-23-v1' },
        'jwt-google',
      ),
    );
  });

  // Last: the code-step mark lasts for the page.
  it('never asks again after the code step showed the box', async () => {
    const get = vi.spyOn(api, 'getMarketingConsent').mockResolvedValue(NEVER_ASKED);
    const verifyOtpCode = vi.fn(async () => {
      seed();
    });
    vi.spyOn(api, 'postMarketingConsent').mockResolvedValue({ consent: false, updated_at: 'now' });
    useCloud.setState({
      mode: 'cloud',
      isLocal: false,
      session: null,
      bootstrap: vi.fn(async () => {}),
      lookupSigninMethod: vi.fn(async () => 'code' as const),
      sendOtpCode: vi.fn(async () => {}),
      verifyOtpCode,
    });
    renderGate();
    expect(consentAskedAtSignup()).toBe(false);
    fireEvent.change(screen.getByLabelText(/^email$/i), { target: { value: 'new@example.com' } });
    fireEvent.click(screen.getByRole('button', { name: /^continue$/i }));
    fireEvent.change(await screen.findByLabelText(/code from the email/i), { target: { value: '12345678' } });
    fireEvent.click(screen.getByRole('button', { name: /^continue$/i }));

    expect(await screen.findByRole('heading', { name: "You're in. Three steps to your first team." })).toBeInTheDocument();
    expect(consentAskedAtSignup()).toBe(true);
    await new Promise((r) => setTimeout(r, 0));
    expect(get).not.toHaveBeenCalled();
    expect(box()).toBeNull();
  });
});
