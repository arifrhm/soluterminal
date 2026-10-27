import { useEffect, useState } from 'react';
import { nativePaneBusy, useStore } from '../store';
import { useIsMobileDevice } from './useIsMobileView';
import { UPDATE_CHECK_FAILED } from '../lib/updateErrors';

// Blocking "update required" modal — the enforcement arm of the operator's
// min_daemon_version / min_shell_version floors in the signed control file
// (daemon/updater_control.go). Three rules keep it from destroying work:
//   1. It never appears during a typed update hold: Focus turns finish and
//      workflow invocations drain under the daemon's bounded policy first.
//   2. The daemon channel prefers the silent path: while auto-apply is being
//      admitted, applying, or retrying non-forced, stay hidden.
//      This modal is the fallback for STUCK states (nothing downloadable,
//      apply failing) — plus the installer channel, which can't self-apply.
//   3. Non-dismissable by design, but every state offers a live action.
//
// Fail-open guarantees live daemon-side: no control file → `required` never
// set; control unreachable past the stale TTL → floors lift. A CDN outage
// can't strand users behind this modal.

// The blocking state must HOLD before we block: a fresh status lands an
// instant before maybeAutoApplyUpdate flips `applying`, and flashing a
// full-screen modal for that beat would look broken.
const HOLD_MS = 1500;

export function RequiredUpdateModal() {
  const status = useStore((s) => s.updateStatus);
  const panes = useStore((s) => s.panes);
  const deferred = useStore((s) => s.deferredUpdate);
  const requestInFlight = useStore((s) => s.updateApplyRequestInFlight);
  const applyUpdate = useStore((s) => s.applyUpdate);
  const applyShellUpdate = useStore((s) => s.applyShellUpdate);
  const checkForUpdate = useStore((s) => s.checkForUpdate);
  const toast = useStore((s) => s.toast);
  const isMobile = useIsMobileDevice();
  const [held, setHeld] = useState(false);
  const [working, setWorking] = useState(false);

  const busy = nativePaneBusy(panes);
  const workflowHeld = status?.workflowDeferred === true;
  const applying = (!!status?.applying && !workflowHeld) || !!status?.shellApplying;
  // Installer floor blocks desktop only — a phone can't run the installer.
  // (The daemon floor still applies on mobile: the swap works from anywhere.)
  const shellBlocked = !!status?.shellRequired && !isMobile;
  const daemonBlocked = !!status?.required;
  const shouldBlock =
    (shellBlocked || daemonBlocked) &&
    !busy &&
    !applying &&
    !workflowHeld &&
    !deferred &&
    !requestInFlight;

  useEffect(() => {
    if (!shouldBlock) {
      setHeld(false);
      return;
    }
    const t = window.setTimeout(() => setHeld(true), HOLD_MS);
    return () => window.clearTimeout(t);
  }, [shouldBlock]);

  if (!shouldBlock || !held) return null;

  const reason = (shellBlocked ? status?.shellRequiredReason : status?.requiredReason) || '';
  // "Restart & update" runs the installer (applyShellUpdate applies a pending
  // daemon update first, so one click settles both floors). "Update now" is
  // the plain daemon swap. Neither downloadable → the stuck state: the only
  // action is re-checking, and the modal clears itself the moment the daemon
  // reports the floor met or lifted.
  const canRunInstaller = shellBlocked && !!status?.shellAvailable;
  const canSwapDaemon = !shellBlocked && daemonBlocked && !!status?.available;
  const from = shellBlocked ? status?.shellCurrent : status?.current;
  const to = shellBlocked ? status?.shellLatest : status?.latest;

  const run = (action: () => Promise<void>) => {
    setWorking(true);
    void action()
      .catch((e) => toast(`Update failed: ${(e as Error).message}`, { kind: 'error', icon: '⚠' }))
      .finally(() => setWorking(false));
  };

  return (
    <div
      className="update-overlay update-required"
      role="alertdialog"
      aria-modal="true"
      aria-labelledby="update-required-title"
    >
      <div className="update-overlay-card">
        <div className="update-required-icon" aria-hidden>
          ↑
        </div>
        <div className="update-overlay-title" id="update-required-title">
          Update required
        </div>
        <div className="update-overlay-detail">
          {reason ? <div className="update-required-reason">{reason}</div> : null}
          <div>
            {shellBlocked
              ? 'The desktop app must be updated before you can continue. Clicking below downloads the installer — follow its prompts; soluterminal will close and reopen on the new version.'
              : canSwapDaemon
                ? 'This version must update before you can continue. It takes a few seconds — open shells and Claude sessions keep running, and this window reloads itself.'
                : 'This version must update before you can continue, but no update is reachable right now. It will unblock itself as soon as the update can be fetched — check your connection, then try again.'}
          </div>
          {from && to && from !== to ? (
            <div className="update-required-versions">
              v{from} → v{to}
            </div>
          ) : null}
          {status?.lastError ? <div className="update-required-error">{UPDATE_CHECK_FAILED}</div> : null}
        </div>
        <div className="update-required-actions">
          {canRunInstaller ? (
            <button
              type="button"
              className="update-required-primary"
              disabled={working}
              onClick={() => run(applyShellUpdate)}
            >
              {working ? 'Updating…' : 'Restart & update'}
            </button>
          ) : canSwapDaemon ? (
            <button
              type="button"
              className="update-required-primary"
              disabled={working}
              onClick={() => run(() => applyUpdate())}
            >
              {working ? 'Updating…' : 'Update now'}
            </button>
          ) : (
            <button
              type="button"
              className="update-required-primary"
              disabled={working}
              onClick={() => run(checkForUpdate)}
            >
              {working ? 'Checking…' : 'Check again'}
            </button>
          )}
        </div>
      </div>
    </div>
  );
}
