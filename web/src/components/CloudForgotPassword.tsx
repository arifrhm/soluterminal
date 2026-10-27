import { useState } from 'react';
import { useCloud } from '../cloud';
import { BrandBar } from './CloudBrand';
import { SIGNIN_EMAIL_MAY_CONTAIN_LINK } from '../lib/authEmailCopy';

// Password reset by emailed code, reached from the sign-in screen's
// "Forgot password?". Recovery stays inside the window: no clickable links.

function recoveryErrorMessage(err: any): string {
  if (err?.code === 'over_email_send_rate_limit' || err?.status === 429) {
    return 'Too many reset emails sent recently — the mail service limits how often codes go out. Wait a few minutes, then try again.';
  }
  if (err?.code === 'otp_expired' || String(err?.message ?? '').toLowerCase().includes('expired or is invalid')) {
    return 'That code is wrong or has expired. Request a new code and try again.';
  }
  if (err?.code === 'weak_password') {
    return `Password not accepted: ${err.message ?? String(err)}`;
  }
  if (err?.code === 'same_password') {
    return 'New password must be different from your old one.';
  }
  return err?.message ?? String(err);
}

export function ForgotPassword({ brand, onBack }: { brand: boolean; onBack: () => void }) {
  type Step = 'email' | 'code' | 'password' | 'done';
  const sendRecoveryCode = useCloud((s) => s.sendRecoveryCode);
  const verifyRecoveryCode = useCloud((s) => s.verifyRecoveryCode);
  const completePasswordReset = useCloud((s) => s.completePasswordReset);
  const cancelPasswordRecovery = useCloud((s) => s.cancelPasswordRecovery);
  const [step, setStep] = useState<Step>(useCloud.getState().recoveryPending ? 'password' : 'email');
  const [email, setEmail] = useState('');
  const [code, setCode] = useState('');
  const [password, setPassword] = useState('');
  const [confirm, setConfirm] = useState('');
  const [busy, setBusy] = useState(false);
  const [message, setMessage] = useState<string | null>(null);

  const onSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    if (step === 'done') {
      onBack();
      return;
    }
    if (step === 'password' && password !== confirm) {
      setMessage("Passwords don't match.");
      return;
    }
    setBusy(true);
    setMessage(null);
    try {
      if (step === 'email') {
        await sendRecoveryCode(email);
        setStep('code');
      } else if (step === 'code') {
        await verifyRecoveryCode(email, code);
        setStep('password');
      } else {
        await completePasswordReset(password);
        setStep('done');
      }
    } catch (err: any) {
      setMessage(recoveryErrorMessage(err));
    } finally {
      setBusy(false);
    }
  };

  const resend = async () => {
    setBusy(true);
    setMessage(null);
    try {
      await sendRecoveryCode(email);
    } catch (err: any) {
      setMessage(recoveryErrorMessage(err));
    } finally {
      setBusy(false);
    }
  };

  const back = () => {
    cancelPasswordRecovery();
    onBack();
  };

  const subtitle = step === 'email'
    ? 'Enter your account email to receive a password reset code.'
    : step === 'code'
      ? `We emailed a sign-in code to ${email}. Enter it below${SIGNIN_EMAIL_MAY_CONTAIN_LINK ? '; you can ignore any link in the email.' : '.'}`
      : step === 'password'
        ? 'Choose a new password for your account.'
        : 'Password updated. Sign in with your new password.';

  return (
    <form onSubmit={onSubmit} className="cloud-gate-panel">
      {brand && <BrandBar />}
      <h1 className="cloud-gate-title">Reset password</h1>
      <div className="cloud-gate-subtitle">{subtitle}</div>

      {step === 'email' && (
        <label className="cloud-gate-label">
          Email
          <input type="email" value={email} onChange={(e) => setEmail(e.target.value)} required autoFocus className="cloud-gate-input" />
        </label>
      )}

      {step === 'code' && (
        <label className="cloud-gate-label">
          Code from the email
          <input value={code} onChange={(e) => setCode(e.target.value)} required autoFocus inputMode="numeric" pattern="[0-9]{6,10}" minLength={6} maxLength={10} className="cloud-gate-input" />
        </label>
      )}

      {step === 'password' && (
        <>
          <label className="cloud-gate-label">
            New password
            <input type="password" value={password} onChange={(e) => setPassword(e.target.value)} required autoFocus className="cloud-gate-input" />
          </label>
          <label className="cloud-gate-label">
            Confirm password
            <input type="password" value={confirm} onChange={(e) => setConfirm(e.target.value)} required className="cloud-gate-input" />
          </label>
        </>
      )}

      <button type="submit" disabled={busy} className="primary cloud-gate-primary">
        {step === 'email' ? (busy ? 'Sending…' : 'Send code')
          : step === 'code' ? (busy ? 'Verifying…' : 'Verify code')
            : step === 'password' ? (busy ? 'Saving…' : 'Update password')
              : 'Back to sign in'}
      </button>

      {step === 'code' && (
        <button type="button" disabled={busy} onClick={() => void resend()} className="cloud-gate-link cloud-gate-link--spaced">Resend code</button>
      )}
      {message && <div className="cloud-gate-error">{message}</div>}
      {(step === 'email' || step === 'code') && (
        <button type="button" className="cloud-gate-link cloud-gate-link--action" onClick={back}>Back to sign in</button>
      )}
    </form>
  );
}
