import { useEffect, useRef, useState } from 'react';
import { useCloud } from '../cloud';
import { isEmbedded } from '../embed';
import { isTauri } from '../voice';
import { MARKETING_CONSENT_LABEL, noteConsentAskedAtSignup, recordSignupConsent } from '../marketingConsent';
import { arrivalIntent, setArrivalIntent, type ArrivalIntent } from '../lib/arrival';
import { siteDownloadPage } from '../lib/siteInstaller';
import { AccountPerks, BrandBar, signinShellClass } from './CloudBrand';
import { ForgotPassword } from './CloudForgotPassword';
import { SIGNIN_EMAIL_MAY_CONTAIN_LINK } from '../lib/authEmailCopy';

// Email-first sign-in for the public beta. Step 1 asks only for the email.
// The cloud then says which step 2 fits: the password field for an account
// that has a password, otherwise an emailed code (which also creates a new
// free account). New users are never asked for a password. On the local
// daemon the account is optional ("Skip for now").

type Step = 'email' | 'password' | 'code';
type SigninMethod = 'code' | 'password';

const SIGNIN_METHOD_KEY = 'solu.signinMethod';
const SIGNIN_EMAIL_KEY = 'solu.signinEmail';
// Supabase refuses a new code for the same address within 60 seconds.
const RESEND_WAIT_SECONDS = 60;

function readStored(key: string): string {
  try {
    return localStorage.getItem(key) ?? '';
  } catch {
    return '';
  }
}

// The method that worked last on this device. A returning password user goes
// straight to the password step with the email filled in.
export function readSigninMethod(): SigninMethod {
  return readStored(SIGNIN_METHOD_KEY) === 'password' ? 'password' : 'code';
}

function rememberSignin(method: SigninMethod, email: string) {
  try {
    localStorage.setItem(SIGNIN_METHOD_KEY, method);
    localStorage.setItem(SIGNIN_EMAIL_KEY, email);
  } catch {
    /* storage blocked: step 1 simply starts empty next time */
  }
}

function otpErrorMessage(err: any): string {
  if (err?.code === 'over_email_send_rate_limit' || err?.status === 429) {
    return 'Too many codes sent recently. Wait a few minutes, then try again.';
  }
  if (err?.code === 'signup_disabled' || /signups not allowed/i.test(String(err?.message ?? ''))) {
    return 'New sign-ups are paused right now. Please try again later.';
  }
  if (err?.code === 'otp_expired' || /expired or is invalid/i.test(String(err?.message ?? ''))) {
    return 'That code is wrong or has expired. Send a new code and try again.';
  }
  return err?.message ?? String(err);
}

function passwordErrorMessage(err: any): string {
  if (err?.code === 'invalid_credentials' || /invalid login credentials/i.test(String(err?.message ?? ''))) {
    return "That password doesn't match this email.";
  }
  if (err?.status === 429) return 'Too many attempts. Wait a few minutes, or email yourself a code instead.';
  return err?.message ?? String(err);
}

// A visitor from the website's "Get started", and anyone whose email code
// failed (rate limit, paused sign-ups, refused address), can still get the
// installer without an account: the website's download page, with the
// checksum and the install steps. "Welcome back" screens offer the way to
// sign up instead.
function DownloadWithoutAccount() {
  const installerUrl = useCloud((s) => s.siteInstaller.url);
  return (
    <div className="cloud-gate-footnote cloud-gate-download-fallback">
      Just want the file?{' '}
      <a className="cloud-gate-inline-link" href={siteDownloadPage(installerUrl)}>
        Download without an account
      </a>
    </div>
  );
}

// "Welcome back" screens: someone new to SoluTerminal switches to the
// "Get SoluTerminal" screen in place.
function NewHereLink({ onClick }: { onClick: () => void }) {
  return (
    <div className="cloud-gate-footnote cloud-gate-new-here">
      New here?{' '}
      <button type="button" className="cloud-gate-inline-link" onClick={onClick}>
        Get started
      </button>
    </div>
  );
}

// Step 1's heading for each way in from the website.
function emailStepCopy(intent: ArrivalIntent | null): { title: string; subtitle: string } {
  if (intent === 'start') return { title: 'Get SoluTerminal', subtitle: 'Enter your email to continue.' };
  if (intent === 'signin') return { title: 'Welcome back', subtitle: 'Sign in to SoluTerminal.' };
  return { title: 'Sign in or create a free account', subtitle: 'Enter your email to continue.' };
}

export function Login() {
  const signIn = useCloud((s) => s.signIn);
  const lookupSigninMethod = useCloud((s) => s.lookupSigninMethod);
  const sendOtpCode = useCloud((s) => s.sendOtpCode);
  const verifyOtpCode = useCloud((s) => s.verifyOtpCode);
  const signInWithGoogle = useCloud((s) => s.signInWithGoogle);
  const accountOptional = useCloud((s) => s.accountOptional);
  const authProviders = useCloud((s) => s.authProviders);
  const continueWithoutAccount = useCloud((s) => s.continueWithoutAccount);
  const isLocal = useCloud((s) => s.isLocal);
  // The hosted app in a browser, not the desktop app (local, or its cloud
  // launch mode, which loads this same bundle from the cloud).
  const hosted = !isLocal && !isTauri();
  // How the visitor came from the website: "Get started" or "Sign in".
  const [intent, setIntent] = useState(() => (hosted ? arrivalIntent() : null));
  const shellClass = signinShellClass(hosted);
  const [email, setEmail] = useState(() => readStored(SIGNIN_EMAIL_KEY));
  const [step, setStep] = useState<Step>(() =>
    readSigninMethod() === 'password' && readStored(SIGNIN_EMAIL_KEY) ? 'password' : 'email',
  );
  const [forgot, setForgot] = useState(useCloud.getState().recoveryPending);
  const [password, setPassword] = useState('');
  const [code, setCode] = useState('');
  const [busy, setBusy] = useState<null | 'checking' | 'sending' | 'signing-in' | 'verifying'>(null);
  const [message, setMessage] = useState<string | null>(null);
  // Set when sending or verifying an email code failed.
  const [codeFailed, setCodeFailed] = useState(false);
  // Unticked by default: product-update emails need an explicit yes.
  const [productUpdates, setProductUpdates] = useState(false);
  const [resendAt, setResendAt] = useState(0);
  const [now, setNow] = useState(() => Date.now());
  // Google refuses to sign in inside embedded webviews and iframes, so it
  // is offered only in a normal browser tab.
  const googleAvailable = authProviders.includes('google') && !isEmbedded() && !isTauri();
  const passwordInput = useRef<HTMLInputElement>(null);

  useEffect(() => {
    if (step !== 'code' || resendAt <= Date.now()) return;
    const timer = window.setInterval(() => setNow(Date.now()), 1000);
    return () => window.clearInterval(timer);
  }, [step, resendAt]);
  useEffect(() => {
    if (step === 'password') passwordInput.current?.focus();
  }, [step]);

  const resendWait = Math.max(0, Math.ceil((resendAt - now) / 1000));
  const cleanEmail = email.trim();

  const sendCode = async () => {
    setBusy('sending');
    setMessage(null);
    setCodeFailed(false);
    try {
      await sendOtpCode(cleanEmail);
      setCode('');
      setResendAt(Date.now() + RESEND_WAIT_SECONDS * 1000);
      setNow(Date.now());
      setStep('code');
    } catch (err: any) {
      setMessage(otpErrorMessage(err));
      setCodeFailed(true);
    } finally {
      setBusy(null);
    }
  };

  const onEmailSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!cleanEmail) {
      setMessage('Enter your email first.');
      return;
    }
    setBusy('checking');
    setMessage(null);
    setCodeFailed(false);
    const method = await lookupSigninMethod(cleanEmail);
    if (method === 'password') {
      setBusy(null);
      setPassword('');
      setStep('password');
      return;
    }
    await sendCode();
  };

  const onPasswordSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    setBusy('signing-in');
    setMessage(null);
    try {
      await signIn(cleanEmail, password);
      rememberSignin('password', cleanEmail);
    } catch (err: any) {
      setMessage(passwordErrorMessage(err));
    } finally {
      setBusy(null);
    }
  };

  const onCodeSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    setBusy('verifying');
    setMessage(null);
    setCodeFailed(false);
    // Before the session lands: the card after sign-in must not ask again.
    noteConsentAskedAtSignup();
    try {
      await verifyOtpCode(cleanEmail, code);
      rememberSignin('code', cleanEmail);
      // The cloud keeps the first choice and only lets a later sign-in opt
      // in, so an unticked box here never withdraws an earlier yes.
      await recordSignupConsent(productUpdates, hosted ? 'web-signup' : 'app-signup');
    } catch (err: any) {
      setMessage(otpErrorMessage(err));
      setCodeFailed(true);
    } finally {
      setBusy(null);
    }
  };

  const changeEmail = () => {
    setStep('email');
    setPassword('');
    setCode('');
    setMessage(null);
    setCodeFailed(false);
  };

  // "Welcome back": the website's "Sign in", and the password step of a
  // remembered device. These lead new people to sign up, not to the file.
  const welcomeBack = step === 'password' || (step === 'email' && intent === 'signin');
  const showNewHere = hosted && welcomeBack;
  const showDownloadFallback = hosted && !welcomeBack && (intent === 'start' || codeFailed);
  const copy = emailStepCopy(intent);

  // Keeps the first-touch tags, so the account is still credited to the
  // link that brought the visitor. A remembered device's address belongs to
  // someone else, so it starts empty.
  const startInstead = () => {
    setArrivalIntent('start');
    setIntent('start');
    if (step === 'password') setEmail('');
    changeEmail();
  };

  if (forgot) {
    return (
      <div className={shellClass}>
        <ForgotPassword brand onBack={() => setForgot(false)} />
      </div>
    );
  }

  if (step === 'password') {
    return (
      <div className={shellClass}>
        <form onSubmit={onPasswordSubmit} className="cloud-gate-panel">
          <BrandBar />
          <h1 className="cloud-gate-title">Welcome back</h1>
          <div className="cloud-gate-subtitle">
            Signing in as {cleanEmail}.{' '}
            <button type="button" className="cloud-gate-inline-link" onClick={changeEmail}>Not you?</button>
          </div>
          <label className="cloud-gate-label">
            Password
            <input
              ref={passwordInput}
              type="password"
              autoComplete="current-password"
              value={password}
              onChange={(e) => setPassword(e.target.value)}
              required
              className="cloud-gate-input"
            />
          </label>
          <button type="submit" disabled={busy !== null} className="primary cloud-gate-primary">
            {busy === 'signing-in' ? 'Signing in…' : 'Sign in'}
          </button>
          {message && <div className="cloud-gate-error">{message}</div>}
          <button type="button" disabled={busy !== null} className="cloud-gate-link cloud-gate-link--spaced" onClick={() => void sendCode()}>
            {busy === 'sending' ? 'Sending a code…' : 'Email me a code instead'}
          </button>
          <button type="button" className="cloud-gate-link" onClick={() => setForgot(true)}>
            Forgot password?
          </button>
          {showNewHere && <NewHereLink onClick={startInstead} />}
          {showDownloadFallback && <DownloadWithoutAccount />}
        </form>
      </div>
    );
  }

  if (step === 'code') {
    return (
      <div className={shellClass}>
        <form onSubmit={onCodeSubmit} className="cloud-gate-panel">
          <BrandBar />
          <h1 className="cloud-gate-title">Check your email</h1>
          <div className="cloud-gate-subtitle">
            We sent a code to {cleanEmail}. Enter it below
            {SIGNIN_EMAIL_MAY_CONTAIN_LINK ? '; you can ignore any link in the email.' : '.'}
          </div>
          <label className="cloud-gate-label">
            Code from the email
            <input
              value={code}
              onChange={(e) => setCode(e.target.value)}
              required
              autoFocus
              autoComplete="one-time-code"
              inputMode="numeric"
              pattern="[0-9]{6,10}"
              minLength={6}
              maxLength={10}
              className="cloud-gate-input"
            />
          </label>
          <label className="cloud-gate-consent">
            <input type="checkbox" checked={productUpdates} onChange={(e) => setProductUpdates(e.target.checked)} />
            <span>{MARKETING_CONSENT_LABEL}</span>
          </label>
          <button type="submit" disabled={busy !== null} className="primary cloud-gate-primary">
            {busy === 'verifying' ? 'Verifying…' : 'Continue'}
          </button>
          {message && <div className="cloud-gate-error">{message}</div>}
          <button
            type="button"
            disabled={busy !== null || resendWait > 0}
            onClick={() => void sendCode()}
            className="cloud-gate-link cloud-gate-link--spaced"
          >
            {resendWait > 0 ? `Send a new code (in ${resendWait}s)` : 'Send a new code'}
          </button>
          <button type="button" className="cloud-gate-link" onClick={changeEmail}>
            Use a different email
          </button>
          {showDownloadFallback && <DownloadWithoutAccount />}
        </form>
      </div>
    );
  }

  return (
    <div className={shellClass}>
      <form onSubmit={(e) => void onEmailSubmit(e)} className="cloud-gate-panel">
        <BrandBar />
        <h1 className="cloud-gate-title">{copy.title}</h1>
        <div className="cloud-gate-subtitle">{copy.subtitle}</div>
        {/* A returning user from "Sign in" needs no pitch. */}
        {intent !== 'signin' && <AccountPerks />}
        <label className="cloud-gate-label">
          Email
          <input
            type="email"
            autoComplete="email"
            value={email}
            onChange={(e) => setEmail(e.target.value)}
            required
            autoFocus
            className="cloud-gate-input"
          />
        </label>
        <button type="submit" disabled={busy !== null} className="primary cloud-gate-primary">
          {busy === 'checking' ? 'Checking…' : busy === 'sending' ? 'Sending code…' : 'Continue'}
        </button>
        {googleAvailable && (
          <button type="button" disabled={busy !== null} className="cloud-gate-primary" onClick={() => void signInWithGoogle()}>
            Continue with Google
          </button>
        )}
        {message && <div className="cloud-gate-error">{message}</div>}
        {/* Local-first: the account is a bonus, never required. The way past
            it is a centred link in the hosted page's link style, so the lime
            Continue stays the only button. */}
        {accountOptional && (
          <div className="cloud-gate-skip">
            <button type="button" disabled={busy !== null} className="cloud-gate-inline-link cloud-gate-skip-link" onClick={continueWithoutAccount}>
              Skip for now
            </button>
            <span className="cloud-gate-footnote">No account needed: everything runs on this PC.</span>
          </div>
        )}
        {showNewHere && <NewHereLink onClick={startInstead} />}
        {showDownloadFallback && <DownloadWithoutAccount />}
      </form>
    </div>
  );
}
