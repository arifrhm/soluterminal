import { useEffect, useState } from 'react';
import { useStore } from '../store';
import { useDaemonHealth, refreshDaemonHealth } from '../lib/useDaemonHealth';
import {
  beginBundleReloadAttempt,
  bundleReloadAttemptsExhausted,
} from '../lib/bundleParity';
import { useMachineAccess } from '../lib/useMachineAccess';

// Prominent banner that surfaces when the daemon has been unreachable
// for long enough that it's almost certainly a real outage — not a
// transient connectivity blip. For a network-dependent app that runs
// over cloud / cellular, "is the backend alive?" is a first-class
// question the user shouldn't have to hunt for in a status-bar corner.
//
// Health polling: this used to run its OWN 10s api.health() interval,
// in parallel with StatusBar's. Both now share a single module-level
// poller (lib/useDaemonHealth.ts) — one request, one source of truth, so
// the banner and the status-bar pill can never disagree about offline
// state. The shared poller starts on the first subscriber (whichever of
// the two mounts first) and uses the same 10s cadence as before.
//
// Thresholds:
//   SHOW_AFTER_MS = 12_000 — two full poll intervals before we show the
//                            banner. Lets one-off failures (daemon
//                            restart, brief network hiccup) self-heal
//                            without flashing chrome at the user.

const POLL_MS = 10_000;
const SHOW_AFTER_MS = 12_000;

export function DaemonHealthBanner() {
  const updating = useStore((s) => s.updateStatus?.applying === true);
  const machineAccess = useMachineAccess();
  // Shared daemon-health state. `lastOkMs` is the timestamp of the last
  // SUCCESSFUL poll (initialized to load time in the singleton) so the
  // banner doesn't flash during the first ~12s of app load while the
  // daemon is still warming up.
  const { health: lastHealth, lastOkMs, polling, bundleState } = useDaemonHealth();
  const reloadApp = useStore((s) => s.reloadApp);
  const [now, setNow] = useState<number>(() => Date.now());
  // A banner the user cannot get rid of is worse than a stale label; let them
  // dismiss it for this session (a NEW version re-arms it).
  const [dismissedFor, setDismissedFor] = useState<string | null>(null);
  const bundleBannerKey = bundleState.kind === 'ok'
    ? null
    : [bundleState.kind, bundleState.shellDaemonVersion, bundleState.shellWebVersion, bundleState.targetVersion].join(':');
  const dismissed = bundleBannerKey !== null && dismissedFor === bundleBannerKey;
  const setDismissed = (on: boolean) => setDismissedFor(on ? bundleBannerKey : null);
  // Separate 1 Hz tick to re-render the "X seconds ago" label without
  // being on the poll cadence. Only ticks while the banner is visible
  // (see the gate below) — no background CPU otherwise.
  const unreachable = !updating && !lastHealth && now - lastOkMs >= SHOW_AFTER_MS;
  useEffect(() => {
    if (!unreachable) return;
    const t = setInterval(() => setNow(Date.now()), 1000);
    return () => clearInterval(t);
  }, [unreachable]);

  // Keep `now` correct even when the banner is hidden, so the moment
  // we DO cross SHOW_AFTER_MS we re-render with the right elapsed
  // time. Otherwise a stale `now` from the last hidden tick could make
  // the banner briefly read "1s" right as it appears.
  useEffect(() => {
    const t = setInterval(() => setNow(Date.now()), POLL_MS);
    return () => clearInterval(t);
  }, []);

  const elapsedSec = Math.floor((now - lastOkMs) / 1000);
  const elapsedLabel = elapsedSec < 60
    ? `${elapsedSec}s`
    : elapsedSec < 3600
      ? `${Math.floor(elapsedSec / 60)}m`
      : `${Math.floor(elapsedSec / 3600)}h ${Math.floor((elapsedSec % 3600) / 60)}m`;

  if (machineAccess.blocked) return null;

  if (!updating && bundleState.kind === 'packaged-mismatch' && !dismissed) {
    return (
      <div className="daemon-banner-stack">
        <div className="daemon-banner is-package-warning" role="status">
        <span className="daemon-banner-dot" aria-hidden />
        <span className="daemon-banner-text">
          <strong>The installed SoluTerminal package contains an older interface</strong>
          <span className="daemon-banner-sub">
            {` · web ${bundleState.shellWebVersion}, daemon ${bundleState.shellDaemonVersion}. Reloading or reopening will not fix this — reinstall SoluTerminal from the latest installer (a corrected release).`}
          </span>
          {unreachable && (
            <span className="daemon-banner-sub">{` · Daemon unreachable · last healthy ${elapsedLabel} ago`}</span>
          )}
        </span>
        {unreachable && (
          <button
            type="button"
            className="daemon-banner-retry"
            onClick={() => refreshDaemonHealth()}
            disabled={polling}
            title="Re-check the daemon now"
          >
            {polling ? 'Checking…' : 'Retry now'}
          </button>
        )}
        <button type="button" className="daemon-banner-dismiss" onClick={() => setDismissed(true)} title="Dismiss until the next update" aria-label="Dismiss">
          ×
        </button>
        </div>
      </div>
    );
  }

  if (!updating && bundleState.kind === 'activation-gap' && !dismissed) {
    const target = bundleState.targetVersion || bundleState.shellDaemonVersion;
    return (
      <div className="daemon-banner-stack">
        <div className="daemon-banner is-info" role="status">
        <span className="daemon-banner-dot" aria-hidden />
        <span className="daemon-banner-text">
          <strong>SoluTerminal updated to {target}</strong>
          <span className="daemon-banner-sub"> · finishing the update on the shell backend — this window will reload automatically</span>
          {unreachable && (
            <span className="daemon-banner-sub">{` · Daemon unreachable · last healthy ${elapsedLabel} ago`}</span>
          )}
        </span>
        {unreachable && (
          <button
            type="button"
            className="daemon-banner-retry"
            onClick={() => refreshDaemonHealth()}
            disabled={polling}
            title="Re-check the daemon now"
          >
            {polling ? 'Checking…' : 'Retry now'}
          </button>
        )}
        <button type="button" className="daemon-banner-dismiss" onClick={() => setDismissed(true)} title="Dismiss until the next update" aria-label="Dismiss">
          ×
        </button>
        </div>
      </div>
    );
  }

  const legacyTarget = bundleState.kind === 'unknown' ? bundleState.legacyStaleVersion : '';
  const transientTarget = bundleState.kind === 'transient-stale' ? bundleState.shellWebVersion : legacyTarget;
  if (!updating && !unreachable && transientTarget && !dismissed) {
    const reloadFailed = bundleReloadAttemptsExhausted(transientTarget);
    const reload = () => {
      if (!beginBundleReloadAttempt(transientTarget)) return;
      reloadApp(transientTarget);
    };
    return (
      <div className="daemon-banner-stack">
        <div className="daemon-banner is-info" role="status">
        <span className="daemon-banner-dot" aria-hidden />
        <span className="daemon-banner-text">
          <strong>SoluTerminal updated to {transientTarget}</strong>
          <span className="daemon-banner-sub">
            {reloadFailed
              ? ' · reloading did not pick it up — quit and reopen SoluTerminal to finish the update'
              : ' · this window is still running the previous interface'}
          </span>
        </span>
        {!reloadFailed && (
          <button type="button" className="daemon-banner-retry" onClick={reload} title="Reload to pick up the new interface">
            Reload
          </button>
        )}
        <button type="button" className="daemon-banner-dismiss" onClick={() => setDismissed(true)} title="Dismiss until the next update" aria-label="Dismiss">
          ×
        </button>
        </div>
      </div>
    );
  }

  if (updating || !unreachable) return null;

  return (
    <div className="daemon-banner-stack">
      <div className="daemon-banner" role="alert">
        <span className="daemon-banner-dot" aria-hidden />
        <span className="daemon-banner-text">
          <strong>Daemon unreachable</strong>
          <span className="daemon-banner-sub">
            {` · last healthy ${elapsedLabel} ago`}
          </span>
        </span>
        <button
          type="button"
          className="daemon-banner-retry"
          onClick={() => refreshDaemonHealth()}
          disabled={polling}
          title="Re-check the daemon now"
        >
          {polling ? 'Checking…' : 'Retry now'}
        </button>
      </div>
    </div>
  );
}
