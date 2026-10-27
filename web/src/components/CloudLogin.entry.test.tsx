import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { useCloud } from '../cloud';
import { CloudGate } from './CloudGate';
import { captureArrival, readArrival } from '../lib/arrival';

// The hosted app's two website entries: "Get SoluTerminal" (intent=start)
// leads with what the free account adds, and every "Welcome back" screen
// leads new people to it instead of to the file.

const sendOtpCode = vi.fn(async () => {});
const lookupSigninMethod = vi.fn(async (_email: string) => 'code' as 'code' | 'password');
const DOWNLOAD_PAGE = 'https://soluterminal.com/download/';
const PERKS = [
  'Your agents on your phone or any browser',
  'A push when an agent needs you',
  'Voice input and smart approvals',
];

function arrive(path: string) {
  window.history.replaceState(null, '', path);
  captureArrival();
}

function seed(overrides: Record<string, unknown> = {}) {
  useCloud.setState({
    mode: 'cloud',
    isLocal: false,
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
    lookupSigninMethod,
    ...overrides,
  });
}

const renderGate = () => render(<CloudGate><div>app</div></CloudGate>);
const downloadLink = () => screen.queryByRole('link', { name: 'Download without an account' });

function rememberPasswordDevice() {
  localStorage.setItem('solu.signinMethod', 'password');
  localStorage.setItem('solu.signinEmail', 'pw@example.com');
}

beforeEach(() => {
  sendOtpCode.mockReset().mockResolvedValue(undefined);
  lookupSigninMethod.mockReset().mockResolvedValue('code');
  sessionStorage.clear();
  localStorage.clear();
  window.history.replaceState(null, '', '/');
});

afterEach(() => {
  vi.restoreAllMocks();
  sessionStorage.clear();
  localStorage.clear();
  useCloud.setState(useCloud.getInitialState(), true);
});

describe('"Get SoluTerminal" perks', () => {
  it.each(['start', 'download'])('lists what the account adds above the email (intent=%s)', (intent) => {
    arrive(`/?intent=${intent}&ref=hn`);
    seed();
    renderGate();

    expect(screen.getByRole('heading', { name: 'Get SoluTerminal' })).toBeInTheDocument();
    const list = screen.getByRole('list', { name: 'Your free account adds:' });
    const items = within(list).getAllByRole('listitem');
    expect(items.map((item) => item.textContent)).toEqual(PERKS.map((perk) => `✓${perk}`));
    // The checks are decoration; a screen reader hears the list.
    for (const item of items) {
      expect(item.querySelector('.cloud-gate-perk-check')).toHaveAttribute('aria-hidden', 'true');
    }
    expect(screen.getByText('Free during beta.')).toBeInTheDocument();
    // The old one-line pitch and any quota are gone.
    expect(document.body.textContent).not.toMatch(/free account: open your sessions/i);
    expect(document.body.textContent).not.toMatch(/\d+\s*\/\s*day|per day|a day/i);

    // Above the email field, and the download link last.
    const email = screen.getByLabelText(/^email$/i);
    expect(list.compareDocumentPosition(email) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    const link = downloadLink();
    expect(link).toHaveAttribute('href', DOWNLOAD_PAGE);
    expect(link?.closest('form')?.lastElementChild).toContainElement(link as HTMLElement);
    expect(screen.queryByRole('button', { name: 'Get started' })).toBeNull();
  });

  it('lists the perks on the plain hosted sign-in too, without the download link', () => {
    seed();
    renderGate();
    expect(screen.getByRole('heading', { name: 'Sign in or create a free account' })).toBeInTheDocument();
    const list = screen.getByRole('list', { name: 'Your free account adds:' });
    expect(within(list).getAllByRole('listitem').map((item) => item.textContent)).toEqual(
      PERKS.map((perk) => `✓${perk}`),
    );
    expect(screen.getByText('Free during beta.')).toBeInTheDocument();
    expect(list.compareDocumentPosition(screen.getByLabelText(/^email$/i)) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    expect(document.body.textContent).not.toMatch(/free account: open your sessions/i);
    expect(downloadLink()).toBeNull();
    expect(screen.queryByRole('button', { name: 'Get started' })).toBeNull();
  });

  it('keeps the perks off "Welcome back", and the desktop app lists the same perks', () => {
    arrive('/?intent=signin');
    seed();
    const view = renderGate();
    expect(screen.queryByText('Your free account adds:')).toBeNull();
    view.unmount();

    sessionStorage.clear();
    window.history.replaceState(null, '', '/');
    seed({ isLocal: true });
    renderGate();
    const list = screen.getByRole('list', { name: 'Your free account adds:' });
    expect(within(list).getAllByRole('listitem').map((item) => item.textContent)).toEqual(
      PERKS.map((perk) => `✓${perk}`),
    );
    expect(document.body.textContent).not.toMatch(/free account: open your sessions/i);
  });

  it('never shows the website entries inside the desktop app', () => {
    arrive('/?intent=start');
    seed({ isLocal: true });
    renderGate();
    expect(screen.getByRole('heading', { name: 'Sign in or create a free account' })).toBeInTheDocument();
    expect(screen.queryByRole('heading', { name: 'Get SoluTerminal' })).toBeNull();
    expect(downloadLink()).toBeNull();
  });
});

describe('"Welcome back" → "New here? Get started"', () => {
  it('replaces the download link on the website sign-in', () => {
    arrive('/?intent=signin&ref=hn');
    seed();
    renderGate();

    expect(screen.getByRole('heading', { name: 'Welcome back' })).toBeInTheDocument();
    expect(downloadLink()).toBeNull();
    expect(document.body.textContent).not.toMatch(/just want the file/i);
    const getStarted = screen.getByRole('button', { name: 'Get started' });
    expect(getStarted.closest('.cloud-gate-new-here')).toHaveTextContent('New here? Get started');
    expect(getStarted.closest('form')?.lastElementChild).toContainElement(getStarted);
  });

  it('switches to "Get SoluTerminal" in place, keeping the typed email and the first-touch tags', async () => {
    arrive('/?intent=signin&ref=hn&utm_source=newsletter');
    seed();
    renderGate();
    fireEvent.change(screen.getByLabelText(/^email$/i), { target: { value: 'new@example.com' } });
    fireEvent.click(screen.getByRole('button', { name: 'Get started' }));

    expect(screen.getByRole('heading', { name: 'Get SoluTerminal' })).toBeInTheDocument();
    expect(screen.getByText('Your free account adds:')).toBeInTheDocument();
    expect(downloadLink()).toHaveAttribute('href', DOWNLOAD_PAGE);
    expect(screen.queryByRole('button', { name: 'Get started' })).toBeNull();
    expect(screen.getByLabelText(/^email$/i)).toHaveValue('new@example.com');
    // Same page, no navigation; the sign-up is credited to the first link.
    expect(window.location.pathname).toBe('/');
    expect(readArrival()).toEqual({ intent: 'start', firstTouch: { ref: 'hn', utm_source: 'newsletter' } });

    fireEvent.click(screen.getByRole('button', { name: /^continue$/i }));
    await waitFor(() => expect(sendOtpCode).toHaveBeenCalledWith('new@example.com'));
    expect(await screen.findByLabelText(/code from the email/i)).toBeInTheDocument();
  });

  it('sends the start surface and the first touch in the sign-up call after switching', async () => {
    arrive('/?intent=signin&ref=hn');
    const signInWithOtp = vi.fn(async () => ({ error: null }));
    const real = useCloud.getInitialState();
    useCloud.setState({
      ...real,
      mode: 'cloud',
      isLocal: false,
      bootstrap: vi.fn(async () => {}),
      lookupSigninMethod,
      supabase: { auth: { signInWithOtp } } as never,
    });
    renderGate();
    fireEvent.click(screen.getByRole('button', { name: 'Get started' }));
    fireEvent.change(screen.getByLabelText(/^email$/i), { target: { value: 'new@example.com' } });
    fireEvent.click(screen.getByRole('button', { name: /^continue$/i }));
    await waitFor(() =>
      expect(signInWithOtp).toHaveBeenCalledWith({
        email: 'new@example.com',
        options: {
          shouldCreateUser: true,
          data: { signup_app: 'soluterminal', signup_surface: 'web-start', first_touch: { ref: 'hn' } },
          emailRedirectTo: expect.stringMatching(/^https:\/\/.+\/auth\/signin$/),
        },
      }),
    );
  });

  it.each(['/?intent=start', '/?intent=signin', '/'])(
    'offers it on the remembered-device password step (%s) and starts with an empty email',
    (path) => {
      rememberPasswordDevice();
      arrive(path);
      seed();
      renderGate();

      expect(screen.getByRole('heading', { name: 'Welcome back' })).toBeInTheDocument();
      expect(screen.getByText(/signing in as pw@example\.com/i)).toBeInTheDocument();
      expect(downloadLink()).toBeNull();
      fireEvent.click(screen.getByRole('button', { name: 'Get started' }));

      expect(screen.getByRole('heading', { name: 'Get SoluTerminal' })).toBeInTheDocument();
      expect(screen.queryByLabelText('Password')).toBeNull();
      expect(screen.getByLabelText(/^email$/i)).toHaveValue('');
      expect(downloadLink()).toHaveAttribute('href', DOWNLOAD_PAGE);
      expect(readArrival().intent).toBe('start');
    },
  );

  it('keeps "Welcome back" on the code path free of the file link, even when a code fails', async () => {
    arrive('/?intent=signin');
    sendOtpCode.mockRejectedValueOnce({ status: 429 });
    seed();
    renderGate();
    fireEvent.change(screen.getByLabelText(/^email$/i), { target: { value: 'me@example.com' } });
    fireEvent.click(screen.getByRole('button', { name: /^continue$/i }));
    expect(await screen.findByText(/too many codes sent recently/i)).toBeInTheDocument();
    expect(downloadLink()).toBeNull();
    expect(screen.getByRole('button', { name: 'Get started' })).toBeInTheDocument();
  });

  it('is not offered in the desktop app', () => {
    rememberPasswordDevice();
    seed({ isLocal: true });
    renderGate();
    expect(screen.getByRole('heading', { name: 'Welcome back' })).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Get started' })).toBeNull();
  });
});
