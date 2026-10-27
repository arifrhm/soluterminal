import { useEffect } from 'react';
import { useCloud } from './cloud';
import { useEntitlements } from './entitlements';
import { useStore } from './store';
import { confirmDialog } from './lib/dialog';

// Connecting this computer to the signed-in free account. The cloud only
// spends its voice and smart-approval allowance for an account that owns a
// connected computer, so a sign-in alone used to leave the mic answering
// "run solud --claim". The daemon's POST /api/cloud/pair/account mints a
// pairing code and accepts it with the account's token (daemon/
// pairing_account.go). After sign-in this file asks once ("Connect this PC
// to your account?"); "Not now" is remembered by the daemon, like an
// explicit Disconnect, so it is never asked again automatically. The
// "Connect" actions (voice toast, smart-approvals hint) connect on demand.

export type ConnectStatus = 'available' | 'connecting' | 'connected' | 'declined' | 'machine_limit' | 'signin' | 'error';

export interface ConnectResult {
  status: ConnectStatus;
  message?: string;
}

function accountToken(): string | null {
  const cloud = useCloud.getState();
  const session = cloud.session;
  if (!session || !cloud.isLocal) return null;
  if ((session.user as { is_anonymous?: boolean } | undefined)?.is_anonymous === true) return null;
  return session.access_token ?? null;
}

export async function connectThisComputer(opts: { force?: boolean; check?: boolean; decline?: boolean } = {}): Promise<ConnectResult> {
  const token = accountToken();
  if (!token) return { status: 'signin', message: 'Sign in to your free account first.' };
  const payload: Record<string, boolean> = { force: opts.force === true };
  if (opts.check) payload.check = true;
  if (opts.decline) payload.decline = true;
  try {
    const res = await fetch('/api/cloud/pair/account', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json', 'X-Solu-Account-Token': token },
      body: JSON.stringify(payload),
    });
    if (res.status === 404) return { status: 'error', message: 'Update SoluTerminal to connect this computer.' };
    const body = (await res.json().catch(() => null)) as ConnectResult | null;
    if (body && typeof body.status === 'string') return body;
    return { status: 'error', message: `Couldn't connect this computer (${res.status}).` };
  } catch {
    return { status: 'error', message: "Couldn't reach SoluTerminal. Try again." };
  }
}

// After a connect, the daemon's account verdict follows the machine token.
function refreshVerdictSoon(): void {
  window.setTimeout(() => { void useEntitlements.getState().load(); }, 4000);
}

/** Connect on demand (the mic's "Connect" action) and say what happened. */
export async function connectThisComputerWithToast(): Promise<void> {
  const toast = useStore.getState().toast;
  const result = await connectThisComputer({ force: true });
  switch (result.status) {
    case 'connecting':
    case 'connected':
      toast('This computer is connected to your account. Try voice again in a few seconds.', { kind: 'info', ttlMs: 6000 });
      refreshVerdictSoon();
      return;
    case 'signin':
      toast(result.message || 'Sign in to your free account first.', {
        kind: 'warn',
        action: { label: 'Sign in', run: () => useCloud.getState().requestSignIn() },
      });
      return;
    default:
      toast(result.message || "Couldn't connect this computer.", { kind: 'warn', ttlMs: 8000 });
  }
}

export const CONNECT_PROMPT_TITLE = 'Connect this PC to your account?';
export const CONNECT_PROMPT_BODY =
  "Turns on voice, smart approvals and phone access. Anyone signed into this account can then use this PC's terminals from their phone.";

const tried = new Set<string>();

/**
 * Asks once, right after the user signs in on the desktop, whether to
 * connect this PC. Skipped when it is already connected, or when the user
 * said "Not now" or disconnected it before (the daemon answers "declined").
 */
export async function askToConnectAfterSignIn(): Promise<void> {
  const check = await connectThisComputer({ check: true });
  if (check.status !== 'available') return;
  const yes = await confirmDialog({
    title: CONNECT_PROMPT_TITLE,
    message: CONNECT_PROMPT_BODY,
    confirmLabel: 'Connect',
    cancelLabel: 'Not now',
  });
  const toast = useStore.getState().toast;
  if (!yes) {
    // Escape and a click outside count as "Not now" too.
    await connectThisComputer({ decline: true });
    toast('Not connected. You can connect this PC any time from the mic or Settings → Devices.', { kind: 'info', ttlMs: 6000 });
    return;
  }
  const result = await connectThisComputer({ force: true });
  if (result.status === 'connecting' || result.status === 'connected') {
    toast('This PC is connected. You can disconnect it in Settings → Devices.', { kind: 'info', ttlMs: 7000 });
    refreshVerdictSoon();
  } else {
    toast(result.message || "Couldn't connect this PC.", { kind: 'warn', ttlMs: 10000 });
  }
}

export function useAskToConnectOnSignIn(): void {
  const userId = useCloud((s) => (s.isLocal && s.session ? s.session.user.id : null));
  const withoutAccount = useCloud((s) => s.withoutAccount);
  useEffect(() => {
    if (!userId || withoutAccount || tried.has(userId)) return;
    tried.add(userId);
    void askToConnectAfterSignIn();
  }, [userId, withoutAccount]);
}

export function resetAutoConnectForTests(): void {
  tried.clear();
}
