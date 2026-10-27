import { useState } from 'react';
import { useCloud } from '../cloud';
import { countSiteDownload } from '../lib/countDownload';
import { SITE_DOWNLOAD_URL } from '../lib/siteLinks';
import { detectVisitorOS } from './DesktopInstallerLink';
import { useIsMobileDevice } from './useIsMobileView';

// The hosted app's first screen for an account with no machines, however it
// signed in: download the Windows app from the website, sign in there with
// the same email, and the PC shows up here. Visitors who aren't on Windows get the link to take to their PC instead;
// never the headless install command.

export const GET_STARTED_TITLE = "You're in. Three steps to your first team.";
const SITE_DOWNLOAD_LABEL = SITE_DOWNLOAD_URL.replace(/^https:\/\//, '').replace(/\/$/, '');

export function usesWindowsDownload(isMobile: boolean): boolean {
  if (isMobile) return false;
  const os = detectVisitorOS();
  return os === 'windows' || os === 'other';
}

/** A direct link to the website's installer file, counted as the account path. */
export function SiteInstallerLink({ className, children }: { className?: string; children: React.ReactNode }) {
  const installer = useCloud((s) => s.siteInstaller);
  return (
    <a className={className} href={installer.url} download onClick={countSiteDownload}>
      {children}
    </a>
  );
}

// The page heading already is GET_STARTED_TITLE: only an account with no
// machines sees these steps.
export function GetStartedCard({ email }: { email: string | null }) {
  const isMobile = useIsMobileDevice();
  const windows = usesWindowsDownload(isMobile);
  const size = useCloud((s) => s.siteInstaller.size);

  return (
    <section className="cloud-gate-start" aria-label={GET_STARTED_TITLE}>
      <ol className="cloud-gate-start-steps">
        <li>
          {windows ? (
            <>
              <strong>Download SoluTerminal for Windows</strong>
              <SiteInstallerLink className="cloud-gate-start-download">Download for Windows</SiteInstallerLink>
              <span className="cloud-gate-footnote">{size}, from soluterminal.com.</span>
            </>
          ) : (
            <OtherDeviceStep email={email} />
          )}
        </li>
        <li>
          <strong>Open it and sign in with the same email.</strong>{' '}
          {email && <span className="cloud-gate-start-email">{email}</span>}{' '}
          We&apos;ll send a new code.
        </li>
        <li>
          <strong>Your PC appears here.</strong> Then you can check on your agents from this page, on any
          device.
          <span className="cloud-gate-footnote">
            When the app asks &ldquo;Connect this PC to your account?&rdquo;, choose Connect.
          </span>
        </li>
      </ol>
    </section>
  );
}

function OtherDeviceStep({ email }: { email: string | null }) {
  const [copied, setCopied] = useState<'idle' | 'copied' | 'failed'>('idle');
  const copy = async () => {
    try {
      await navigator.clipboard.writeText(SITE_DOWNLOAD_URL);
      setCopied('copied');
    } catch {
      // No clipboard (insecure origin, old browser): the link is on screen.
      setCopied('failed');
    }
    window.setTimeout(() => setCopied('idle'), 1800);
  };
  // Opens the visitor's own mail app addressed to themselves; nothing is sent
  // by us.
  const mailto =
    `mailto:${email ? encodeURIComponent(email) : ''}` +
    `?subject=${encodeURIComponent('SoluTerminal for Windows')}` +
    `&body=${encodeURIComponent(`SoluTerminal for Windows: ${SITE_DOWNLOAD_URL}`)}`;
  return (
    <>
      <strong>Download it on your Windows PC: {SITE_DOWNLOAD_LABEL}</strong>
      <span className="cloud-gate-start-actions">
        <button type="button" className="cloud-gate-link" onClick={() => void copy()}>
          {copied === 'copied' ? 'Copied' : copied === 'failed' ? 'Select and copy the link' : 'Copy link'}
        </button>
        <a className="cloud-gate-inline-link" href={mailto}>Email me the link</a>
      </span>
      <span className="cloud-gate-footnote">SoluTerminal runs on Windows 10 &amp; 11. macOS and Linux coming soon.</span>
    </>
  );
}
