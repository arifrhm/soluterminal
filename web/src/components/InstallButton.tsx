import { useEffect, useState } from 'react';
import { useIsMobileView } from './useIsMobileView';

// Chrome/Edge fire `beforeinstallprompt` when the site passes all PWA
// installability checks (manifest + service worker + offline-capable +
// https + engagement heuristic). The event gives us the deferred
// prompt to invoke on user gesture — the native install dialog that
// adds soluterminal to the Android home screen / Windows Start menu.
//
// Why we ship an explicit button rather than relying on Chrome's
// automatic mini-infobar: the automatic one is heuristic, easy to
// miss, and not offered at all in many locales / Chrome builds. An
// explicit button is the recommended pattern for 2026-era PWAs —
// installability-aware and user-initiated.
//
// The button also doubles as a diagnostic. It renders in four
// visible states so the user (and us, debugging remotely) can tell
// exactly where the installability pipeline is stuck:
//   - prompt-ready         → "⤓ Install" (active)
//   - sw-missing           → "⤓ SW not registered" (warn)
//   - criteria-not-met     → "⤓ Install…" (dim, tooltip explains)
// Hidden entirely inside Tauri and installed PWA windows — both are
// already app contexts, so an installed-state badge is redundant.
type BeforeInstallPromptEvent = Event & {
  prompt: () => Promise<void>;
  userChoice: Promise<{ outcome: 'accepted' | 'dismissed' }>;
};

type SwStatus =
  | 'probing'       // initial, haven't asked the browser yet
  | 'unsupported'   // no navigator.serviceWorker (ancient browser)
  | 'registered'    // have a registration, SW is active
  | 'installing'    // have a registration, SW is still installing/waiting
  | 'missing';      // no registration at all — something went wrong

type InstallUiState =
  | { kind: 'hidden' }
  | { kind: 'ios-help' }
  | { kind: 'prompt-ready' }
  | { kind: 'sw-missing'; sw: SwStatus }
  | { kind: 'criteria-not-met'; sw: SwStatus };

function isTauri(): boolean {
  return !!(window as unknown as { __SOLU_TAURI__?: boolean }).__SOLU_TAURI__;
}

// Firefox (desktop and Android) does not implement `beforeinstallprompt`
// — it's a Chrome/Edge-only event. So our criteria-not-met state will
// ALWAYS trigger on Firefox even when the manifest + SW are perfect,
// and the Chrome-flavored "engagement heuristic" tooltip is actively
// misleading there. Firefox's install path is deterministic: URL bar
// menu → "Install" / "Add to Home Screen". Detect and route the user
// to it explicitly. Includes Firefox-derivative UAs (Focus, Fennec).
function isFirefox(): boolean {
  if (typeof navigator === 'undefined') return false;
  return /firefox|fxios/i.test(navigator.userAgent);
}

function isStandalone(): boolean {
  if (typeof window === 'undefined') return false;
  try {
    if (window.matchMedia('(display-mode: standalone)').matches) return true;
  } catch { /* older browser */ }
  // iOS Safari reports install status via a non-standard `standalone` bool.
  return !!(window.navigator as unknown as { standalone?: boolean }).standalone;
}

function isIosWebKit(): boolean {
  if (typeof navigator === 'undefined') return false;
  const ua = navigator.userAgent;
  const platform = navigator.platform;
  const iOS = /iPad|iPhone|iPod/.test(ua) || (platform === 'MacIntel' && navigator.maxTouchPoints > 1);
  return iOS && /WebKit/.test(ua) && !/CriOS|FxiOS|EdgiOS/.test(ua);
}

export function InstallButton() {
  const isMobile = useIsMobileView();
  const [deferred, setDeferred] = useState<BeforeInstallPromptEvent | null>(null);
  const [sw, setSw] = useState<SwStatus>('probing');
  // Stable across renders so we can tell "never fired" vs. "fired and
  // consumed". Shown in the diagnostic tooltip.
  const [promptFiredAt, setPromptFiredAt] = useState<number | null>(null);

  useEffect(() => {
    if (isTauri()) return;

    // Pick up a prompt that fired BEFORE we mounted (stashed by
    // main.tsx's early window listener). Critical for Chrome — it
    // fires beforeinstallprompt exactly once per page load, and if
    // our React listener isn't attached by then, we'd miss it
    // forever and the diagnostic would falsely say "never".
    const stashed = (window as unknown as { __SOLU_INSTALL_PROMPT__?: Event }).__SOLU_INSTALL_PROMPT__;
    if (stashed) {
      setDeferred(stashed as BeforeInstallPromptEvent);
      setPromptFiredAt(Date.now());
    }

    const onBeforeInstall = (e: Event) => {
      // preventDefault stops Chrome from showing its own mini-infobar
      // on top of ours — we want the button to be the single install
      // affordance, not a competing toast from the browser chrome.
      e.preventDefault();
      setDeferred(e as BeforeInstallPromptEvent);
      setPromptFiredAt(Date.now());
    };
    const onStashedReady = () => {
      const s = (window as unknown as { __SOLU_INSTALL_PROMPT__?: Event }).__SOLU_INSTALL_PROMPT__;
      if (s) {
        setDeferred(s as BeforeInstallPromptEvent);
        setPromptFiredAt(Date.now());
      }
    };
    const onInstalled = () => {
      // User finished installing via any path (our button, the
      // browser's own menu, etc.). Clear the deferred prompt so the
      // button disappears for this session.
      setDeferred(null);
    };

    window.addEventListener('beforeinstallprompt', onBeforeInstall as EventListener);
    window.addEventListener('solu:install-prompt-ready', onStashedReady);
    window.addEventListener('appinstalled', onInstalled);

    // Probe the SW state so we can distinguish "not installable because
    // SW is missing" from "not installable for some other reason".
    // navigator.serviceWorker.ready resolves once a SW is active for
    // this page; if we've been waiting > 8 s, something failed.
    if (!('serviceWorker' in navigator)) {
      setSw('unsupported');
    } else {
      let settled = false;
      const timeout = window.setTimeout(() => {
        if (settled) return;
        settled = true;
        // Fall back to the registration API — if there's a waiting SW
        // but none active yet, `ready` never resolves.
        navigator.serviceWorker.getRegistration().then((r) => {
          if (r?.active) setSw('registered');
          else if (r) setSw('installing');
          else setSw('missing');
        }).catch(() => setSw('missing'));
      }, 8_000);
      navigator.serviceWorker.ready.then(() => {
        if (settled) return;
        settled = true;
        clearTimeout(timeout);
        setSw('registered');
      }).catch(() => {
        if (settled) return;
        settled = true;
        clearTimeout(timeout);
        setSw('missing');
      });
    }

    return () => {
      window.removeEventListener('beforeinstallprompt', onBeforeInstall as EventListener);
      window.removeEventListener('solu:install-prompt-ready', onStashedReady);
      window.removeEventListener('appinstalled', onInstalled);
    };
  }, []);

  const ui: InstallUiState = (() => {
    if (isTauri()) return { kind: 'hidden' };
    if (!isMobile) return { kind: 'hidden' };
    if (isStandalone()) return { kind: 'hidden' };
    if (isIosWebKit()) return { kind: 'ios-help' };
    if (deferred) return { kind: 'prompt-ready' };
    if (sw === 'missing' || sw === 'unsupported') return { kind: 'sw-missing', sw };
    return { kind: 'criteria-not-met', sw };
  })();

  if (ui.kind === 'hidden') return null;

  async function onClick() {
    if (!deferred) return;
    try {
      await deferred.prompt();
      await deferred.userChoice;
    } catch {
      /* prompt() throws if already invoked; swallow and clear */
    }
    setDeferred(null);
  }

  if (ui.kind === 'prompt-ready') {
    return (
      <button
        type="button"
        className="install-btn"
        onClick={onClick}
        title="Install soluterminal as an app on your device"
        aria-label="Install app"
      >
        ⤓ Install
      </button>
    );
  }

  if (ui.kind === 'ios-help') {
    return (
      <span
        className="install-btn diagnostic dim"
        title="On iPhone or iPad, use Safari's Share button, then choose Add to Home Screen."
      >
        Add to Home Screen
      </span>
    );
  }

  // Diagnostic states: show a dim pill with a detailed tooltip so the
  // user (and us, debugging over the phone) can see exactly WHERE the
  // pipeline is stuck. Clicking copies the diagnostic string to the
  // clipboard so the user can paste it back to us.
  const diag = [
    `state: ${ui.kind}`,
    `sw: ${ui.sw}`,
    `display: ${isStandalone() ? 'standalone' : 'browser'}`,
    `beforeinstallprompt: ${promptFiredAt ? 'fired@' + new Date(promptFiredAt).toLocaleTimeString() : 'never'}`,
    `ua: ${typeof navigator !== 'undefined' ? navigator.userAgent : ''}`,
  ].join(' | ');

  const firefox = isFirefox();
  const label = ui.kind === 'sw-missing'
    ? '⤓ SW not ready'
    : firefox ? '⤓ Install via menu' : '⤓ Install…';
  const tooltip = ui.kind === 'sw-missing'
    ? `Service worker not registered (${ui.sw}). PWA install requires an active SW — something failed during registration. Tap to copy diagnostic.`
    : firefox
      ? `Firefox installs PWAs from its own menu, not via an in-page prompt. Open the URL bar three-dot menu and tap "Install" (or "Add to Home Screen" on older builds). If the menu item isn't shown, clear site data for this origin and reload. Tap to copy diagnostic.`
      : `Chrome hasn't offered install yet. Common reasons: (1) engagement heuristic — tap around and revisit; (2) site was previously dismissed (~90 day cooldown); (3) already installed at the OS level. Tap to copy diagnostic.`;

  async function copyDiag() {
    try {
      await navigator.clipboard?.writeText(diag);
    } catch { /* silent */ }
  }

  return (
    <button
      type="button"
      className={`install-btn diagnostic ${ui.kind === 'sw-missing' ? 'warn' : 'dim'}`}
      onClick={copyDiag}
      title={tooltip + '\n\n' + diag}
      aria-label={label}
    >
      {label}
    </button>
  );
}
