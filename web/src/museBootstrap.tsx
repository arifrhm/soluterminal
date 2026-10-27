import { useState } from 'react';
import { useStore } from './store';
import { api, type MuseInstallSpec } from './api';
import { refreshDaemonHealth } from './lib/useDaemonHealth';
import { isRouterMode, activeBackendFromUrl, type BackendId } from './dualBackend';

// Shared Muse-Code bootstrap actions — the install (guided curl|sh with a
// disclosure confirm card) and sign-in flows. Deliberate parallel of
// claudeBootstrap.tsx so the always-on MuseInstallBanner reuses the EXACT
// same, already-reviewed flow (no second copy to drift). Status reading lives
// with each caller: the checklist force-probes on open; the banner reads the
// shared daemon-health poll (useDaemonHealth) — both ultimately reflect the
// daemon's cached probe (daemon/muse_bootstrap.go).

// Titles of the spawned helper panes — also used to de-dupe (don't open a
// second installer / sign-in pane while one is live).
export const MUSE_INSTALL_PANE_TITLE = 'Install Muse Code';
export const MUSE_LOGIN_PANE_TITLE = 'Sign in to Muse';

// After a helper pane spawns, poll the daemon's probe on a backoff until the
// state it was meant to change actually flips (install → installed; login →
// authed). Same window as Claude (~8 min) — the installer typically finishes
// well inside the first 15s check. Each recheck is serialized daemon-side
// (museProbeMu), so this adds at most one login-shell probe per tick.
export const MUSE_RECHECK_BACKOFF_MS = [
  15_000, 20_000, 30_000, 45_000, 60_000, 90_000, 120_000, 120_000,
];
let museRecheckToken = 0;

export function scheduleMuseRecheck(kind: 'install' | 'login'): void {
  // Pin the backend the flow was armed for AT SCHEDULE TIME (same rationale
  // as scheduleClaudeRecheck — a mid-loop backend switch must not re-aim the
  // polls at the other daemon).
  const backend: BackendId | null = isRouterMode()
    ? (useStore.getState().activeBackend ?? activeBackendFromUrl())
    : null;
  scheduleMuseRecheckLoop(kind, backend);
}

function scheduleMuseRecheckLoop(kind: 'install' | 'login', backend: BackendId | null): void {
  const token = ++museRecheckToken; // a newer schedule supersedes any older loop
  const step = (i: number) => {
    if (token !== museRecheckToken || i >= MUSE_RECHECK_BACKOFF_MS.length) return;
    window.setTimeout(() => {
      if (token !== museRecheckToken) return;
      void api
        .museRecheckOn(backend)
        .then((st) => {
          // Push the fresh probe into the shared health poll so the banner /
          // checklist flip immediately (they read useDaemonHealth, not us).
          refreshDaemonHealth();
          const done = kind === 'install' ? st.museInstalled === true : st.museAuthed === true;
          if (!done) {
            step(i + 1);
          } else if (kind === 'install' && st.museAuthed !== true) {
            // Install landed but muse isn't signed in yet. The installer pane
            // drops to an interactive shell, and users often sign in right
            // there instead of clicking the banner's "Sign in" — keep watching
            // (same pinned backend) so the nudge clears either way.
            scheduleMuseRecheckLoop('login', backend);
          }
        })
        .catch((e: unknown) => {
          const status = (e as { status?: number } | null)?.status;
          // 403 (relay-forwarded) / 404 (older daemon): the recheck endpoint
          // isn't available on this surface at all — stop; the daemon's 5-min
          // ticker still owns those cases. Anything else is a transient daemon
          // hiccup (restart, socket blip) — keep polling.
          if (status === 403 || status === 404) return;
          step(i + 1);
        });
    }, MUSE_RECHECK_BACKOFF_MS[i]);
  };
  step(0);
}

// useMuseInstall owns the install confirm-card lifecycle + the sign-in spawn.
// onSpawn fires after a helper pane is opened (or pointed at an existing one)
// so the caller can dismiss itself. The install is a deliberate TWO-STEP
// action: startInstall fetches the OS-correct command and stages a confirm
// card; confirmInstall spawns it only after the user has seen the exact
// command. We never pipe a remote script to a shell on a single click
// without disclosure.
export function useMuseInstall(opts?: { onSpawn?: (kind: 'install' | 'login') => void }): {
  installing: boolean;
  pendingInstall: MuseInstallSpec | null;
  startInstall: () => void;
  confirmInstall: () => void;
  cancelInstall: () => void;
  loginMuse: () => void;
} {
  const onSpawn = opts?.onSpawn;
  const [installing, setInstalling] = useState(false);
  const [pendingInstall, setPendingInstall] = useState<MuseInstallSpec | null>(null);

  const startInstall = () => {
    if (installing) return;
    setInstalling(true);
    void api
      .museInstallCommand()
      .then((spec) => {
        if (!spec.supported) {
          // supported=false only happens on a Windows host-native daemon: Muse
          // Code ships macOS/Linux binaries only, so the curl|sh installer can
          // never run there. Route the user at their WSL side (dual mode) or
          // at setting one up — never at a command that can't work.
          useStore.getState().toast(
            isRouterMode()
              ? 'Muse Code is macOS/Linux-only — switch to the WSL backend to install it there.'
              : 'Muse Code is macOS/Linux-only — install it inside WSL (curl -fsSL https://dev.meta.ai/install.sh | sh), then use the WSL backend and re-check.',
            { kind: 'info', icon: '!', ttlMs: 10000 },
          );
          return;
        }
        setPendingInstall(spec);
        useStore.getState().toast('Review the installer command, then click "Run installer in new pane".', {
          kind: 'info',
          ttlMs: 7000,
        });
      })
      .catch((e: unknown) => {
        const status = (e as { status?: number } | null)?.status;
        useStore.getState().toast(
          status === 403
            ? 'Install must run on the machine itself, not over your phone/cloud link.'
            : 'Could not reach the daemon to start the installer.',
          { kind: 'info', icon: '!', ttlMs: 8000 },
        );
      })
      .finally(() => setInstalling(false));
  };

  const confirmInstall = () => {
    const spec = pendingInstall;
    if (!spec) return;
    setPendingInstall(null);
    // De-dupe: if an installer pane is already open, point the user at it rather
    // than spawning a second concurrent curl|sh.
    const existing = Object.values(useStore.getState().panes).find((p) => p.title === MUSE_INSTALL_PANE_TITLE);
    if (existing) {
      useStore.getState().toast('An installer is already running — see the “Install Muse Code” pane.', {
        kind: 'info',
        ttlMs: 7000,
      });
      scheduleMuseRecheck('install'); // re-arm: the pane may be close to done
      onSpawn?.('install');
      return;
    }
    void useStore
      .getState()
      .createPane({ cwd: '', cmd: spec.cmd, title: MUSE_INSTALL_PANE_TITLE })
      .then(() => {
        useStore.getState().toast(
          'Installing Muse Code — watch the new pane. This updates automatically when it finishes.',
          { kind: 'info', ttlMs: 9000 },
        );
        scheduleMuseRecheck('install');
        onSpawn?.('install');
      })
      .catch(() => {
        useStore.getState().toast('Couldn’t open the installer pane — check the daemon and try again.', {
          kind: 'info',
          icon: '!',
          ttlMs: 8000,
        });
      });
  };

  const cancelInstall = () => setPendingInstall(null);

  // Sign in to Muse: open a terminal `muse` pane — first run prompts for
  // browser sign-in or an API-key paste (Meta Managed Accounts must use a
  // key). Works over the relay too (the pane is spawned on the host; the
  // phone user completes the flow there). After login the probe/health
  // detects authed → surfaces flip to Ready.
  const loginMuse = () => {
    const existing = Object.values(useStore.getState().panes).find((p) => p.title === MUSE_LOGIN_PANE_TITLE);
    if (existing) {
      useStore.getState().toast('A sign-in pane is already open.', { kind: 'info', ttlMs: 6000 });
      scheduleMuseRecheck('login'); // re-arm: the user may be mid-login
      onSpawn?.('login');
      return;
    }
    void useStore
      .getState()
      .createPane({ cwd: '', cmd: ['muse'], title: MUSE_LOGIN_PANE_TITLE })
      .then(() => {
        useStore.getState().toast(
          'Opened Muse — follow the sign-in prompt in the new pane (browser approval or API-key paste).',
          { kind: 'info', ttlMs: 10000 },
        );
        scheduleMuseRecheck('login');
        onSpawn?.('login');
      })
      .catch(() => {
        useStore.getState().toast('Couldn’t open the sign-in pane — check the daemon and try again.', {
          kind: 'info',
          icon: '!',
          ttlMs: 8000,
        });
      });
  };

  return { installing, pendingInstall, startInstall, confirmInstall, cancelInstall, loginMuse };
}

// MuseInstallConfirmCard — the disclosure card shown before running the
// installer. Renders the EXACT command (spec.display) so a remote curl|sh is
// never run on a single click without the user seeing it first. Shared by the
// checklist and the banner.
export function MuseInstallConfirmCard({
  spec,
  onConfirm,
  onCancel,
}: {
  spec: MuseInstallSpec;
  onConfirm: () => void;
  onCancel: () => void;
}) {
  return (
    <div
      style={{
        marginTop: 8,
        marginBottom: 8,
        padding: '10px 12px',
        border: '1px solid var(--border)',
        borderRadius: 6,
        background: 'var(--bg-elev, rgba(255,255,255,0.03))',
      }}
    >
      <div style={{ fontSize: 13, fontWeight: 600 }}>Run Muse Code installer?</div>
      <div style={{ fontSize: 12, color: 'var(--fg-dim)', margin: '6px 0', lineHeight: 1.45 }}>
        {spec.note} The next step opens a new pane and runs:
      </div>
      <code
        style={{
          display: 'block',
          fontSize: 11,
          fontFamily: 'var(--mono, monospace)',
          background: 'var(--bg, rgba(0,0,0,0.25))',
          padding: '6px 8px',
          borderRadius: 4,
          overflowX: 'auto',
          whiteSpace: 'pre',
        }}
      >{spec.display}</code>
      <div style={{ display: 'flex', gap: 8, marginTop: 10 }}>
        <button type="button" onClick={onConfirm} style={{ fontSize: 12, padding: '3px 10px' }}>
          Run installer in new pane
        </button>
        <button type="button" onClick={onCancel} style={{ fontSize: 12, padding: '3px 10px' }}>
          Cancel
        </button>
      </div>
    </div>
  );
}
