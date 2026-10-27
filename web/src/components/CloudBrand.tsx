import { useCloud } from '../cloud';
import { isTauri } from '../voice';

// The sign-in screens wear the website's look (mark, lime accent, Instrument
// Sans / Martian Mono) everywhere, so the desktop app's first screen, the
// hosted app and soluterminal.com read as one product. The hosted pages stay
// dark like the website; the desktop app's sign-in follows its light or dark
// theme. The hosted app's other gate screens keep the brand to the browser.

/** The hosted app in a browser: not the local daemon, not the desktop window. */
export function useHostedBrand(): boolean {
  const isLocal = useCloud((s) => s.isLocal);
  return !isLocal && !isTauri();
}

export function brandShellClass(hosted: boolean): string {
  return hosted ? 'cloud-gate-shell cloud-brand' : 'cloud-gate-shell';
}

/** Sign-in: always branded; outside the hosted page it follows the app theme. */
export function signinShellClass(hosted: boolean): string {
  return hosted ? brandShellClass(true) : 'cloud-gate-shell cloud-brand cloud-brand--themed';
}

/** The website header's mark and wordmark, at the top of the panel. */
export function BrandBar() {
  return (
    <div className="cloud-brand-bar">
      <svg className="cloud-brand-mark" viewBox="0 0 20 20" width="20" height="20" aria-hidden="true" focusable="false">
        <rect className="cloud-brand-mark-dim" x="1" y="1" width="8" height="18" rx="1.5" />
        <rect className="cloud-brand-mark-acc" x="11" y="1" width="8" height="10" rx="1.5" />
        <rect className="cloud-brand-mark-dim" x="11" y="13" width="8" height="6" rx="1.5" />
      </svg>
      <span className="cloud-brand-name">SoluTerminal</span>
      <span className="cloud-brand-tag">beta</span>
    </div>
  );
}

// What the free account adds, above the email on every sign-in that pitches
// the account: the hosted "Get SoluTerminal" and "Sign in or create a free
// account", and the desktop app's first screen. One list, so they stay in
// sync. No quotas here: the limits live on the website's pricing.
export const ACCOUNT_PERKS = [
  'Your agents on your phone or any browser',
  'A push when an agent needs you',
  'Voice input and smart approvals',
] as const;

export function AccountPerks() {
  return (
    <div className="cloud-gate-perks">
      <div className="cloud-gate-perks-title" id="cloud-gate-perks-title">
        Your free account adds:
      </div>
      <ul className="cloud-gate-perks-list" role="list" aria-labelledby="cloud-gate-perks-title">
        {ACCOUNT_PERKS.map((perk) => (
          <li key={perk}>
            <span className="cloud-gate-perk-check" aria-hidden="true">
              ✓
            </span>
            {perk}
          </li>
        ))}
      </ul>
      <div className="cloud-gate-footnote cloud-gate-perks-note">Free during beta.</div>
    </div>
  );
}
