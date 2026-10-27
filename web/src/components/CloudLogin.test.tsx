import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { useCloud } from '../cloud';
import { CloudGate } from './CloudGate';
import { ACCOUNT_PERKS } from './CloudBrand';
import { api } from '../api';
import { captureArrival, readArrival } from '../lib/arrival';
import { DESKTOP_AUTH_REDIRECT } from '../cloud';

const sendOtpCode = vi.fn(async () => {});
const verifyOtpCode = vi.fn(async () => {});
const signInWithGoogle = vi.fn(async () => {});
const signIn = vi.fn(async (_email: string, _password: string) => {});
const lookupSigninMethod = vi.fn(async (_email: string) => 'code' as 'code' | 'password');
const DOWNLOAD_PAGE = 'https://soluterminal.com/download/';

function arrive(path: string) {
  window.history.replaceState(null, '', path);
  captureArrival();
}

function seed(overrides: Record<string, unknown> = {}) {
  useCloud.setState({
    mode: 'cloud',
    isLocal: true,
    session: null,
    authorised: null,
    selectedMachineId: null,
    error: null,
    recoveryPending: false,
    accountOptional: false,
    authProviders: [],
    withoutAccount: false,
    bootstrap: vi.fn(async () => {}),
    sendOtpCode,
    verifyOtpCode,
    signInWithGoogle,
    signIn,
    lookupSigninMethod,
    ...overrides,
  });
}

function renderGate() {
  return render(<CloudGate><div>app</div></CloudGate>);
}

// Step 1: type the email and press Continue.
function submitEmail(value: string) {
  fireEvent.change(screen.getByLabelText(/^email$/i), { target: { value } });
  fireEvent.click(screen.getByRole('button', { name: /^continue$/i }));
}

async function enterCode(value = '12345678') {
  fireEvent.change(await screen.findByLabelText(/code from the email/i), { target: { value } });
  fireEvent.click(screen.getByRole('button', { name: /^continue$/i }));
}

function resetMocks() {
  sendOtpCode.mockReset().mockResolvedValue(undefined);
  verifyOtpCode.mockReset().mockResolvedValue(undefined);
  signIn.mockReset().mockResolvedValue(undefined);
  lookupSigninMethod.mockReset().mockResolvedValue('code');
  sessionStorage.clear();
  try { localStorage.clear(); } catch { /* ignore */ }
  window.history.replaceState(null, '', '/');
}

afterEach(() => {
  vi.restoreAllMocks();
  sessionStorage.clear();
  delete (window as unknown as Record<string, unknown>).__SOLU_TAURI__;
  useCloud.setState(useCloud.getInitialState(), true);
});

describe('email-first sign-in', () => {
  beforeEach(resetMocks);

  it('step 1 asks only for the email', () => {
    seed();
    renderGate();
    expect(screen.getByLabelText(/^email$/i)).toBeInTheDocument();
    expect(screen.queryByLabelText('Password')).toBeNull();
    expect(screen.queryByRole('button', { name: /sign in with password/i })).toBeNull();
    expect(screen.queryByLabelText(/email me product updates/i)).toBeNull();
    expect(screen.getByRole('button', { name: /^continue$/i })).toHaveClass('primary');
  });

  it('a new or code-only email gets a code straight away, then the consent box', async () => {
    seed();
    renderGate();
    submitEmail('new@example.com');
    await waitFor(() => expect(lookupSigninMethod).toHaveBeenCalledWith('new@example.com'));
    await waitFor(() => expect(sendOtpCode).toHaveBeenCalledWith('new@example.com'));
    expect(await screen.findByText(/we sent a code to new@example\.com/i)).toBeInTheDocument();
    const box = screen.getByLabelText('Email me product updates (you can unsubscribe anytime)') as HTMLInputElement;
    expect(box.checked).toBe(false);
    expect(screen.queryByLabelText('Password')).toBeNull();
    await enterCode();
    // The shared project sends 8-digit codes; 6 to 10 digits are accepted.
    await waitFor(() => expect(verifyOtpCode).toHaveBeenCalledWith('new@example.com', '12345678'));
  });

  it('an email with a password goes straight to a focused password field', async () => {
    lookupSigninMethod.mockResolvedValue('password');
    seed();
    renderGate();
    submitEmail('pw@example.com');
    const password = await screen.findByLabelText('Password');
    await waitFor(() => expect(password).toHaveFocus());
    expect(sendOtpCode).not.toHaveBeenCalled();
    expect(screen.getByText(/signing in as pw@example\.com/i)).toBeInTheDocument();
    expect(screen.queryByLabelText(/email me product updates/i)).toBeNull();
    fireEvent.change(password, { target: { value: 'secret-pass' } });
    fireEvent.click(screen.getByRole('button', { name: /^sign in$/i }));
    await waitFor(() => expect(signIn).toHaveBeenCalledWith('pw@example.com', 'secret-pass'));
    expect(localStorage.getItem('solu.signinMethod')).toBe('password');
    expect(localStorage.getItem('solu.signinEmail')).toBe('pw@example.com');
  });

  it('explains a wrong password and offers the emailed code', async () => {
    lookupSigninMethod.mockResolvedValue('password');
    signIn.mockRejectedValue({ code: 'invalid_credentials', message: 'Invalid login credentials' });
    seed();
    renderGate();
    submitEmail('pw@example.com');
    fireEvent.change(await screen.findByLabelText('Password'), { target: { value: 'wrong' } });
    fireEvent.click(screen.getByRole('button', { name: /^sign in$/i }));
    expect(await screen.findByText("That password doesn't match this email.")).toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: 'Email me a code instead' }));
    await waitFor(() => expect(sendOtpCode).toHaveBeenCalledWith('pw@example.com'));
    expect(await screen.findByLabelText(/code from the email/i)).toBeInTheDocument();
  });

  it('explains an expired code and allows a new one after the 60 s wait', async () => {
    verifyOtpCode.mockRejectedValueOnce({ code: 'otp_expired', message: 'Token has expired or is invalid' });
    seed();
    renderGate();
    submitEmail('code@example.com');
    await enterCode();
    expect(await screen.findByText(/wrong or has expired\. send a new code/i)).toBeInTheDocument();
    expect(screen.getByRole('button', { name: /send a new code \(in \d+s\)/i })).toBeDisabled();
    const later = Date.now() + 61_000;
    vi.spyOn(Date, 'now').mockReturnValue(later);
    await act(async () => { await new Promise((r) => setTimeout(r, 1100)); });
    const ready = screen.getByRole('button', { name: 'Send a new code' });
    expect(ready).not.toBeDisabled();
    fireEvent.click(ready);
    await waitFor(() => expect(sendOtpCode).toHaveBeenCalledTimes(2));
  });

  it('a returning password device opens on the password step with the email filled in', () => {
    localStorage.setItem('solu.signinMethod', 'password');
    localStorage.setItem('solu.signinEmail', 'back@example.com');
    seed();
    renderGate();
    expect(screen.getByLabelText('Password')).toBeInTheDocument();
    expect(screen.getByText(/signing in as back@example\.com/i)).toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: 'Not you?' }));
    expect(screen.getByLabelText(/^email$/i)).toHaveValue('back@example.com');
    expect(screen.queryByLabelText('Password')).toBeNull();
  });

  it('a returning code device has the email filled in on step 1', () => {
    localStorage.setItem('solu.signinMethod', 'code');
    localStorage.setItem('solu.signinEmail', 'again@example.com');
    seed();
    renderGate();
    expect(screen.getByLabelText(/^email$/i)).toHaveValue('again@example.com');
    expect(screen.queryByLabelText('Password')).toBeNull();
  });

  it('records the product-updates choice after a code sign-in', async () => {
    const post = vi.spyOn(api, 'postMarketingConsent').mockResolvedValue({ consent: true, updated_at: null });
    verifyOtpCode.mockImplementation(async () => {
      useCloud.setState({ session: { access_token: 'jwt-abc', user: { id: 'u1' } } as never });
    });
    seed();
    renderGate();
    submitEmail('new@example.com');
    fireEvent.click(await screen.findByLabelText(/email me product updates/i));
    await enterCode();
    await waitFor(() =>
      expect(post).toHaveBeenCalledWith(
        { consent: true, source: 'app-signup', wording_version: '2026-10-23-v1' },
        'jwt-abc',
      ),
    );
  });

  it('explains closed sign-ups', async () => {
    sendOtpCode.mockRejectedValue({ code: 'signup_disabled', message: 'Signups not allowed for otp' });
    seed();
    renderGate();
    submitEmail('new@example.com');
    expect(await screen.findByText(/sign-ups are paused/i)).toBeInTheDocument();
  });

  it('leads with the free account and the same perks as the hosted sign-in', () => {
    seed({ accountOptional: true });
    renderGate();
    expect(screen.getByRole('heading', { name: 'Sign in or create a free account' })).toBeInTheDocument();
    expect(screen.getByText('Enter your email to continue.')).toBeInTheDocument();
    const list = screen.getByRole('list', { name: 'Your free account adds:' });
    expect(within(list).getAllByRole('listitem').map((item) => item.textContent)).toEqual(
      ACCOUNT_PERKS.map((perk) => `✓${perk}`),
    );
    expect(screen.getByText('Free during beta.')).toBeInTheDocument();
    expect(document.body.textContent).not.toMatch(/free account: open your sessions/i);
    expect(document.body.textContent).not.toMatch(/free forever/i);
  });

  it('lets a local install skip the account from a centred link', () => {
    seed({ accountOptional: true });
    renderGate();
    expect(screen.queryByText('app')).not.toBeInTheDocument();
    const skip = screen.getByRole('button', { name: /skip for now/i });
    // A text link in the hosted page's link style, so the lime Continue is
    // the only button: the account is a bonus.
    expect(skip).toHaveClass('cloud-gate-inline-link', 'cloud-gate-skip-link');
    expect(skip).not.toHaveClass('primary');
    expect(skip).not.toHaveClass('secondary');
    expect(skip).not.toHaveClass('cloud-gate-primary');
    expect(skip.closest('.cloud-gate-skip')).toHaveTextContent('No account needed: everything runs on this PC.');
    const cont = screen.getByRole('button', { name: /^continue$/i });
    expect(cont.compareDocumentPosition(skip) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    fireEvent.click(skip);
    expect(screen.getByText('app')).toBeInTheDocument();
    expect(localStorage.getItem('solu.localNoAccount')).toBe('1');
  });

  it('never offers the skip link on the cloud relay', () => {
    seed({ isLocal: false, accountOptional: false });
    renderGate();
    expect(screen.queryByRole('button', { name: /skip for now/i })).not.toBeInTheDocument();
  });

  it('offers Google only when the project enables it', () => {
    seed({ authProviders: ['google'] });
    renderGate();
    fireEvent.click(screen.getByRole('button', { name: /continue with google/i }));
    expect(signInWithGoogle).toHaveBeenCalled();
  });
});

describe('step copy and layout', () => {
  beforeEach(resetMocks);

  it('titles step 1 for each way in from the website', () => {
    const cases: [string, string, string][] = [
      ['/', 'Sign in or create a free account', 'Enter your email to continue.'],
      ['/?intent=start&ref=hn', 'Get SoluTerminal', 'Enter your email to continue.'],
      ['/?intent=signin', 'Welcome back', 'Sign in to SoluTerminal.'],
      // The retired website step still works, as "Get started".
      ['/?intent=download', 'Get SoluTerminal', 'Enter your email to continue.'],
    ];
    for (const [path, title, subtitle] of cases) {
      sessionStorage.clear();
      useCloud.setState(useCloud.getInitialState(), true);
      arrive(path);
      seed({ isLocal: false });
      const view = renderGate();
      expect(screen.getByRole('heading', { name: title })).toBeInTheDocument();
      expect(screen.getByText(subtitle)).toBeInTheDocument();
      const link = screen.queryByRole('link', { name: 'Download without an account' });
      // "Welcome back" offers the way to sign up instead of the file.
      if (path === '/' || path === '/?intent=signin') {
        expect(link).toBeNull();
      } else {
        // At the bottom of the form, after every other control.
        expect(link).toHaveAttribute('href', DOWNLOAD_PAGE);
        expect(link?.closest('.cloud-gate-download-fallback')).toHaveTextContent(
          'Just want the file? Download without an account',
        );
        const form = link?.closest('form');
        expect(form?.lastElementChild).toContainElement(link as HTMLElement);
      }
      // New users are never asked for a password on step 1.
      expect(screen.queryByLabelText('Password')).toBeNull();
      view.unmount();
    }
  });

  it('keeps "Sign in" free of the sign-up pitch', () => {
    arrive('/?intent=signin');
    seed({ isLocal: false });
    renderGate();
    expect(screen.queryByText(/free account: open your sessions/i)).toBeNull();
  });

  it('keeps the remembered device on "Welcome back · Not you?"', () => {
    localStorage.setItem('solu.signinMethod', 'password');
    localStorage.setItem('solu.signinEmail', 'pw@example.com');
    arrive('/?intent=start');
    seed({ isLocal: false });
    renderGate();
    expect(screen.getByRole('heading', { name: 'Welcome back' })).toBeInTheDocument();
    expect(screen.getByText(/signing in as pw@example\.com/i)).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Not you?' })).toBeInTheDocument();
    expect(screen.queryByRole('link', { name: 'Download without an account' })).toBeNull();
    expect(screen.getByRole('button', { name: 'Get started' }).closest('.cloud-gate-new-here')).toHaveTextContent(
      'New here? Get started',
    );
  });

  it('links the download page the cloud names', () => {
    arrive('/?intent=start');
    seed({ isLocal: false, siteInstaller: { url: 'https://www.soluterminal.com/download/SoluTerminal-Setup.exe', size: '80 MB' } });
    renderGate();
    expect(screen.getByRole('link', { name: 'Download without an account' })).toHaveAttribute(
      'href',
      'https://www.soluterminal.com/download/',
    );
  });

  it('no longer tells people to ignore a link: the router sends codes only', async () => {
    seed();
    renderGate();
    submitEmail('new@example.com');
    expect(await screen.findByText(/enter it below\./i)).toBeInTheDocument();
    expect(screen.queryByText(/ignore any link/i)).not.toBeInTheDocument();
  });

  it('keeps every secondary link left-aligned', async () => {
    lookupSigninMethod.mockResolvedValue('password');
    seed();
    renderGate();
    submitEmail('pw@example.com');
    await screen.findByLabelText('Password');
    for (const name of ['Email me a code instead', 'Forgot password?']) {
      expect(screen.getByRole('button', { name })).not.toHaveClass('cloud-gate-link--action');
    }
    fireEvent.click(screen.getByRole('button', { name: 'Email me a code instead' }));
    await screen.findByLabelText(/code from the email/i);
    for (const name of [/send a new code/i, /use a different email/i]) {
      expect(screen.getByRole('button', { name })).not.toHaveClass('cloud-gate-link--action');
    }
  });
});

describe('the lookup itself', () => {
  beforeEach(resetMocks);

  it('answers password only when the server says so, and code on any failure', async () => {
    const real = useCloud.getInitialState();
    const fetchSpy = vi.spyOn(globalThis, 'fetch');
    fetchSpy.mockResolvedValueOnce(new Response(JSON.stringify({ method: 'password' }), { status: 200 }));
    expect(await real.lookupSigninMethod('pw@example.com')).toBe('password');
    const [url, init] = fetchSpy.mock.calls[0] as [string, RequestInit];
    expect(url).toBe('/api/auth/method');
    expect(JSON.parse(String(init.body))).toEqual({ email: 'pw@example.com' });

    fetchSpy.mockResolvedValueOnce(new Response(JSON.stringify({ error: 'rate_limited' }), { status: 429 }));
    expect(await real.lookupSigninMethod('pw@example.com')).toBe('code');
    fetchSpy.mockRejectedValueOnce(new Error('offline'));
    expect(await real.lookupSigninMethod('pw@example.com')).toBe('code');
    fetchSpy.mockResolvedValueOnce(new Response('<html>', { status: 200 }));
    expect(await real.lookupSigninMethod('pw@example.com')).toBe('code');
  });
});

describe('hosted sign-up from the website download step', () => {
  beforeEach(resetMocks);

  it('records the consent choice as a web sign-up in the hosted app', async () => {
    const post = vi.spyOn(api, 'postMarketingConsent').mockResolvedValue({ consent: false, updated_at: null });
    verifyOtpCode.mockImplementation(async () => {
      useCloud.setState({ session: { access_token: 'jwt-web', user: { id: 'u2' } } as never });
    });
    seed({ isLocal: false });
    renderGate();
    submitEmail('web@example.com');
    await enterCode();
    await waitFor(() =>
      expect(post).toHaveBeenCalledWith(
        { consent: false, source: 'web-signup', wording_version: '2026-10-23-v1' },
        'jwt-web',
      ),
    );
  });

  it('leads with the download and keeps new users on the code path', async () => {
    arrive('/?intent=start');
    seed({ isLocal: false });
    renderGate();
    expect(screen.getByRole('heading', { name: 'Get SoluTerminal' })).toBeInTheDocument();
    expect(screen.getByRole('link', { name: 'Download without an account' })).toHaveAttribute('href', DOWNLOAD_PAGE);
    expect(screen.queryByLabelText('Password')).toBeNull();
    submitEmail('new@example.com');
    expect(await screen.findByLabelText(/code from the email/i)).toBeInTheDocument();
    expect(screen.queryByLabelText('Password')).toBeNull();
    // Still offered while the visitor waits for the code.
    expect(screen.getByRole('link', { name: 'Download without an account' })).toHaveAttribute('href', DOWNLOAD_PAGE);
  });

  it('offers the download without an account on every email-code error', async () => {
    seed({ isLocal: false });
    renderGate();
    expect(screen.queryByRole('link', { name: 'Download without an account' })).toBeNull();

    // Sending the code fails (the email router's budget, for example).
    sendOtpCode.mockRejectedValueOnce({ status: 429, message: 'Too many emails' });
    submitEmail('web@example.com');
    expect(await screen.findByText(/too many codes sent recently/i)).toBeInTheDocument();
    expect(screen.getByRole('link', { name: 'Download without an account' })).toHaveAttribute('href', DOWNLOAD_PAGE);

    // Verifying fails.
    fireEvent.click(screen.getByRole('button', { name: /^continue$/i }));
    verifyOtpCode.mockRejectedValueOnce({ code: 'otp_expired', message: 'Token has expired or is invalid' });
    await enterCode();
    expect(await screen.findByText(/wrong or has expired/i)).toBeInTheDocument();
    expect(screen.getByRole('link', { name: 'Download without an account' })).toBeInTheDocument();
  });

  it('never offers the download inside the desktop app', async () => {
    sendOtpCode.mockRejectedValueOnce({ status: 429 });
    seed({ isLocal: true });
    renderGate();
    submitEmail('web@example.com');
    expect(await screen.findByText(/too many codes sent recently/i)).toBeInTheDocument();
    expect(screen.queryByRole('link', { name: 'Download without an account' })).toBeNull();
  });

  it('carries the first-touch tag from arrival into the sign-up call', async () => {
    window.history.replaceState(null, '', '/?intent=start&ref=hn&utm_medium=social');
    captureArrival();
    expect(window.location.search).toBe('');
    const signInWithOtp = vi.fn(async () => ({ error: null }));
    const verifyOtp = vi.fn(async () => ({ error: null }));
    const real = useCloud.getInitialState();
    useCloud.setState({
      ...real,
      mode: 'cloud',
      isLocal: false,
      bootstrap: vi.fn(async () => {}),
      lookupSigninMethod,
      supabase: { auth: { signInWithOtp, verifyOtp } } as never,
    });
    renderGate();
    submitEmail('new@example.com');
    await waitFor(() =>
      expect(signInWithOtp).toHaveBeenCalledWith({
        email: 'new@example.com',
        options: {
          shouldCreateUser: true,
          data: {
            signup_app: 'soluterminal',
            signup_surface: 'web-start',
            first_touch: { ref: 'hn', utm_medium: 'social' },
          },
          emailRedirectTo: expect.stringMatching(/^https:\/\/.+\/auth\/signin$/),
        },
      }),
    );
    await enterCode();
    await waitFor(() => expect(verifyOtp).toHaveBeenCalled());
    // The tag stays for this tab: the get-started card counts its download
    // with it, and Supabase ignores sign-up metadata for existing accounts.
    expect(readArrival()).toEqual({ intent: 'start', firstTouch: { ref: 'hn', utm_medium: 'social' } });
  });

  it('sends only the app tag from the desktop app', async () => {
    window.history.replaceState(null, '', '/?ref=hn');
    captureArrival();
    const signInWithOtp = vi.fn(async () => ({ error: null }));
    useCloud.setState({ isLocal: true, supabase: { auth: { signInWithOtp } } as never });
    await useCloud.getState().sendOtpCode('a@example.com');
    expect(signInWithOtp).toHaveBeenCalledWith({
      email: 'a@example.com',
      options: {
        shouldCreateUser: true,
        data: { signup_app: 'soluterminal' },
        emailRedirectTo: expect.stringMatching(/^https:\/\/.+\/auth\/signin$/),
      },
    });
  });
});

describe('desktop app in cloud launch mode', () => {
  beforeEach(() => {
    resetMocks();
    Object.defineProperty(window, '__SOLU_TAURI__', { configurable: true, value: true });
  });

  it('records app-signup and never offers the download, though it loads the cloud bundle', async () => {
    const post = vi.spyOn(api, 'postMarketingConsent').mockResolvedValue({ consent: false, updated_at: null });
    verifyOtpCode.mockImplementation(async () => {
      useCloud.setState({ session: { access_token: 'jwt-desk', user: { id: 'u3' } } as never });
    });
    sendOtpCode.mockRejectedValueOnce({ status: 429 });
    seed({ isLocal: false });
    renderGate();
    submitEmail('desk@example.com');
    expect(await screen.findByText(/too many codes sent recently/i)).toBeInTheDocument();
    expect(screen.queryByRole('link', { name: 'Download without an account' })).toBeNull();
    fireEvent.click(screen.getByRole('button', { name: /^continue$/i }));
    await enterCode();
    await waitFor(() =>
      expect(post).toHaveBeenCalledWith(
        { consent: false, source: 'app-signup', wording_version: '2026-10-23-v1' },
        'jwt-desk',
      ),
    );
  });

  it('sends no website tags from the desktop window', async () => {
    window.history.replaceState(null, '', '/?ref=hn');
    captureArrival();
    const signInWithOtp = vi.fn(async () => ({ error: null }));
    useCloud.setState({ isLocal: false, supabase: { auth: { signInWithOtp } } as never });
    await useCloud.getState().sendOtpCode('desk@example.com');
    expect(signInWithOtp).toHaveBeenCalledWith({
      email: 'desk@example.com',
      options: {
        shouldCreateUser: true,
        data: { signup_app: 'soluterminal' },
        emailRedirectTo: DESKTOP_AUTH_REDIRECT,
      },
    });
  });
});

describe('hosted sign-in look', () => {
  beforeEach(resetMocks);

  it('wears the website look everywhere; only the desktop app follows its theme', () => {
    const expectBrand = (container: HTMLElement, themed: boolean) => {
      const shell = container.querySelector('.cloud-gate-shell');
      expect(shell).toHaveClass('cloud-brand');
      if (themed) expect(shell).toHaveClass('cloud-brand--themed');
      else expect(shell).not.toHaveClass('cloud-brand--themed');
      expect(shell?.querySelector('.cloud-brand-bar')).toHaveTextContent('SoluTerminalbeta');
      expect(shell?.querySelector('.cloud-brand-mark')).toHaveAttribute('aria-hidden', 'true');
      expect(shell?.querySelector('button.primary')).toHaveTextContent('Continue');
    };

    // The hosted page is always dark, like the website.
    seed({ isLocal: false });
    const hosted = renderGate();
    expectBrand(hosted.container, false);
    hosted.unmount();

    // The desktop app (local daemon) follows its light or dark theme.
    useCloud.setState(useCloud.getInitialState(), true);
    seed({ isLocal: true, accountOptional: true });
    const local = renderGate();
    expectBrand(local.container, true);
    local.unmount();

    // So does the desktop window in its cloud launch mode.
    Object.defineProperty(window, '__SOLU_TAURI__', { configurable: true, value: true });
    useCloud.setState(useCloud.getInitialState(), true);
    seed({ isLocal: false });
    const desk = renderGate();
    expectBrand(desk.container, true);
  });

  it("brands the desktop app's code and password steps too", async () => {
    seed({ isLocal: true, accountOptional: true });
    const view = renderGate();
    submitEmail('new@example.com');
    expect(await screen.findByRole('heading', { name: 'Check your email' })).toBeInTheDocument();
    expect(view.container.querySelector('.cloud-brand--themed .cloud-brand-bar')).not.toBeNull();
    // The product-updates box still starts unticked.
    expect(screen.getByLabelText(/email me product updates/i)).not.toBeChecked();
    view.unmount();

    localStorage.setItem('solu.signinMethod', 'password');
    localStorage.setItem('solu.signinEmail', 'back@example.com');
    const pw = renderGate();
    expect(screen.getByLabelText('Password')).toBeInTheDocument();
    expect(pw.container.querySelector('.cloud-brand--themed .cloud-brand-bar')).not.toBeNull();
  });
});
