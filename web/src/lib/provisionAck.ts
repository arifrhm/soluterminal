// One-time acknowledgment gate shown before the FIRST team-credential
// provisioning action (Claude mint / Codex assign) in this browser profile.
//
// Why: provisioning places a working login for an account the OWNER controls
// onto a teammate's machine. That's legitimate for accounts/seats the team is
// entitled to, and not for splitting one consumer subscription across several
// people — a distinction the provider's terms draw, not us. The gate makes
// sure every owner has seen that distinction once before their first
// provision, with the responsibility stated plainly. It never blocks a
// feature: confirm once and it doesn't ask again (the ? help popovers keep
// the durable copy + links to the actual terms).
import { confirmDialog } from './dialog';

const ACK_KEY = 'solu.teamProvisionAck.v1';

export function hasProvisionAck(): boolean {
  try { return localStorage.getItem(ACK_KEY) === '1'; } catch { return false; }
}

export async function ensureProvisionAcknowledged(): Promise<boolean> {
  if (hasProvisionAck()) return true;
  const ok = await confirmDialog({
    title: 'Provisioning accounts to teammates',
    message:
      "This places a working login for an account you control onto a teammate's machine.\n\n" +
      'Using your own accounts across your own devices is always fine. Splitting one consumer ' +
      "subscription across several people generally is not — Anthropic's and OpenAI's consumer " +
      'terms expect each person to use their own account, and team/enterprise plans are the ' +
      'supported way to share access.\n\n' +
      "You're responsible for making sure your team's setup complies with your provider's " +
      'terms (links live in the ? help next to the team header).',
    confirmLabel: 'I understand — continue',
    cancelLabel: 'Cancel',
  });
  if (ok) {
    try { localStorage.setItem(ACK_KEY, '1'); } catch { /* ignore */ }
  }
  return ok;
}
