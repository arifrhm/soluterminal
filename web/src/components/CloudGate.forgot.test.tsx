import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import type { Session } from '@supabase/supabase-js';
import { useCloud } from '../cloud';
import { CloudGate } from './CloudGate';

const sendRecoveryCode = vi.fn(async () => {});
const verifyRecoveryCode = vi.fn(async () => {});
const completePasswordReset = vi.fn(async () => {});
const cancelPasswordRecovery = vi.fn();

function seedLoginState(overrides: Record<string, unknown> = {}) {
  useCloud.setState({
    mode: 'cloud',
    isLocal: false,
    session: null,
    authorised: null,
    selectedMachineId: null,
    error: null,
    recoveryPending: false,
    bootstrap: vi.fn(async () => {}),
    sendRecoveryCode,
    verifyRecoveryCode,
    completePasswordReset,
    cancelPasswordRecovery,
    lookupSigninMethod: vi.fn(async () => 'password' as const),
    ...overrides,
  });
}

function renderGate() {
  return render(<CloudGate><div>app</div></CloudGate>);
}

// "Forgot password?" sits on the password step, which an email with a
// password reaches straight after Continue.
async function showPasswordForm() {
  if (screen.queryByLabelText('Password')) return;
  fireEvent.change(screen.getByLabelText(/^email$/i), { target: { value: 'person@example.com' } });
  fireEvent.click(screen.getByRole('button', { name: /^continue$/i }));
  await screen.findByLabelText('Password');
}

async function openRecovery() {
  await showPasswordForm();
  fireEvent.click(screen.getByRole('button', { name: /forgot password/i }));
  expect(screen.getByRole('heading', { name: /reset password/i })).toBeInTheDocument();
}

async function reachCodeStep() {
  await openRecovery();
  fireEvent.change(screen.getByLabelText(/^email$/i), { target: { value: 'person@example.com' } });
  fireEvent.click(screen.getByRole('button', { name: /send code/i }));
  await screen.findByLabelText(/code from the email/i);
}

async function reachPasswordStep() {
  await reachCodeStep();
  fireEvent.change(screen.getByLabelText(/code from the email/i), { target: { value: '123456' } });
  fireEvent.click(screen.getByRole('button', { name: /verify code/i }));
  await screen.findByLabelText(/^new password$/i);
}

describe('CloudGate forgot password', () => {
  beforeEach(() => {
    useCloud.setState(useCloud.getInitialState(), true);
    sendRecoveryCode.mockReset().mockResolvedValue(undefined);
    verifyRecoveryCode.mockReset().mockResolvedValue(undefined);
    completePasswordReset.mockReset().mockResolvedValue(undefined);
    cancelPasswordRecovery.mockReset();
    seedLoginState();
  });

  afterEach(() => {
    vi.restoreAllMocks();
    useCloud.setState(useCloud.getInitialState(), true);
  });

  it('opens the reset-password email step from Login', async () => {
    renderGate();

    await showPasswordForm();
    expect(screen.getByRole('button', { name: /forgot password/i })).toBeInTheDocument();
    await openRecovery();
    expect(screen.getByLabelText(/^email$/i)).toBeInTheDocument();
  });

  it('walks email, code, password, success, and back to sign in', async () => {
    renderGate();
    await reachCodeStep();
    expect(sendRecoveryCode).toHaveBeenCalledWith('person@example.com');

    fireEvent.change(screen.getByLabelText(/code from the email/i), { target: { value: '123456' } });
    fireEvent.click(screen.getByRole('button', { name: /verify code/i }));
    await screen.findByLabelText(/^new password$/i);
    expect(verifyRecoveryCode).toHaveBeenCalledWith('person@example.com', '123456');

    fireEvent.change(screen.getByLabelText(/^new password$/i), { target: { value: 'valid-password' } });
    fireEvent.change(screen.getByLabelText(/^confirm password$/i), { target: { value: 'valid-password' } });
    fireEvent.click(screen.getByRole('button', { name: /update password/i }));
    await screen.findByText(/password updated\. sign in with your new password\./i);
    expect(completePasswordReset).toHaveBeenCalledWith('valid-password');

    // Back lands on the password step for the same email, ready for the
    // new password.
    fireEvent.click(screen.getByRole('button', { name: /back to sign in/i }));
    expect(screen.getByRole('heading', { name: /welcome back/i })).toBeInTheDocument();
    expect(screen.getByLabelText('Password')).toBeInTheDocument();
  });

  it('keeps the code step open and explains an expired code', async () => {
    verifyRecoveryCode.mockRejectedValueOnce({ message: 'Token has expired or is invalid' });
    renderGate();
    await reachCodeStep();

    fireEvent.change(screen.getByLabelText(/code from the email/i), { target: { value: '123456' } });
    fireEvent.click(screen.getByRole('button', { name: /verify code/i }));

    expect(await screen.findByText(/that code is wrong or has expired/i)).toBeInTheDocument();
    expect(screen.getByLabelText(/code from the email/i)).toBeInTheDocument();
  });

  it('turns reset-email rate limits into wait-a-bit guidance', async () => {
    sendRecoveryCode.mockRejectedValueOnce({ status: 429, message: 'raw error' });
    renderGate();
    await openRecovery();

    fireEvent.change(screen.getByLabelText(/^email$/i), { target: { value: 'person@example.com' } });
    fireEvent.click(screen.getByRole('button', { name: /send code/i }));

    expect(await screen.findByText(/too many reset emails sent recently/i)).toBeInTheDocument();
    expect(screen.queryByText('raw error')).toBeNull();
  });

  it("rejects mismatched passwords without calling Supabase", async () => {
    renderGate();
    await reachPasswordStep();

    fireEvent.change(screen.getByLabelText(/^new password$/i), { target: { value: 'one-password' } });
    fireEvent.change(screen.getByLabelText(/^confirm password$/i), { target: { value: 'other-password' } });
    fireEvent.click(screen.getByRole('button', { name: /update password/i }));

    expect(screen.getByText("Passwords don't match.")).toBeInTheDocument();
    expect(completePasswordReset).not.toHaveBeenCalled();
  });

  it.each([
    [
      { code: 'weak_password', message: 'Password should be at least 12 characters.' },
      /password not accepted: password should be at least 12 characters/i,
    ],
    [
      { code: 'same_password', message: 'New password should be different.' },
      /new password must be different from your old one/i,
    ],
  ])('shows password policy guidance and stays on the password step', async (error, expected) => {
    completePasswordReset.mockRejectedValueOnce(error);
    renderGate();
    await reachPasswordStep();

    fireEvent.change(screen.getByLabelText(/^new password$/i), { target: { value: 'valid-password' } });
    fireEvent.change(screen.getByLabelText(/^confirm password$/i), { target: { value: 'valid-password' } });
    fireEvent.click(screen.getByRole('button', { name: /update password/i }));

    expect(await screen.findByText(expected)).toBeInTheDocument();
    expect(screen.getByLabelText(/^new password$/i)).toBeInTheDocument();
  });

  it('maps resend rate limits and remains on the code step', async () => {
    renderGate();
    await reachCodeStep();
    sendRecoveryCode.mockRejectedValueOnce({ code: 'over_email_send_rate_limit', message: 'raw resend error' });

    fireEvent.click(screen.getByRole('button', { name: /resend code/i }));

    expect(await screen.findByText(/too many reset emails sent recently/i)).toBeInTheDocument();
    expect(screen.queryByText('raw resend error')).toBeNull();
    expect(screen.getByLabelText(/code from the email/i)).toBeInTheDocument();
  });

  it('resends successfully without advancing from the code step', async () => {
    renderGate();
    await reachCodeStep();
    sendRecoveryCode.mockClear();

    fireEvent.click(screen.getByRole('button', { name: /resend code/i }));

    await waitFor(() => expect(sendRecoveryCode).toHaveBeenCalledWith('person@example.com'));
    expect(screen.getByLabelText(/code from the email/i)).toBeInTheDocument();
  });

  it('disables the submit button while sending', async () => {
    let resolveSend!: () => void;
    sendRecoveryCode.mockImplementationOnce(() => new Promise<void>((resolve) => { resolveSend = resolve; }));
    renderGate();
    await openRecovery();
    fireEvent.change(screen.getByLabelText(/^email$/i), { target: { value: 'person@example.com' } });

    fireEvent.click(screen.getByRole('button', { name: /send code/i }));

    expect(screen.getByRole('button', { name: /sending…/i })).toBeDisabled();
    resolveSend();
    await screen.findByLabelText(/code from the email/i);
  });

  it('does not offer recovery cancellation after a recovery session exists', async () => {
    renderGate();
    await reachPasswordStep();

    expect(screen.queryByRole('button', { name: /back to sign in/i })).toBeNull();
  });

  it('keeps recovery UI mounted when verification establishes a session', async () => {
    seedLoginState({
      recoveryPending: true,
      session: { access_token: 'token', user: { email: 'person@example.com' } } as Session,
    });

    renderGate();

    await waitFor(() => expect(screen.getByRole('heading', { name: /reset password/i })).toBeInTheDocument());
    expect(screen.getByLabelText(/^new password$/i)).toBeInTheDocument();
    expect(screen.queryByText('app')).toBeNull();
  });
});
