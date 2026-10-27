import { useState } from 'react';
import { api } from '../api';

interface Props {
  error: string;
  onRetry: () => void | Promise<void>;
}

type HealthCheck =
  | { status: 'idle'; message: string }
  | { status: 'checking'; message: string }
  | { status: 'ok'; message: string }
  | { status: 'error'; message: string };

function errorMessage(err: unknown): string {
  return err instanceof Error ? err.message : String(err || 'Unknown error');
}

// Cause-ranked checklist of WHY bootstrap most likely failed, most-common
// first. Each line is a thing the user can act on — not a raw stack trace.
// This is the "the app can't talk to its daemon" triage screen; the
// DaemonHealthBanner remains the single source of truth for the live
// reachable/unreachable state once the grid IS loaded, so this screen
// deliberately mirrors its language ("daemon unreachable") and never shows a
// competing elapsed-time clock.
const CAUSES: { title: string; detail: string }[] = [
  {
    title: 'The daemon (solud) isn’t running',
    detail:
      'Most common. Start it from the tray, or use the tray “Repair setup” item to relaunch it. On a fresh machine the first launch can take a few seconds.',
  },
  {
    title: 'WSL has no Linux distribution installed',
    detail:
      'The WSL backend needs a distro. Run “wsl --install” in PowerShell (admin), reboot if prompted, then use the tray “Repair setup” item.',
  },
  {
    title: 'A stale process is holding port 8787',
    detail:
      'Something already answers on :8787 but it isn’t the matching daemon version. Quit the app fully, end any old solud / solud.exe process, then relaunch.',
  },
  {
    title: 'The bundled daemon binary is missing',
    detail:
      'The desktop install may be incomplete or quarantined by antivirus. Reinstall soluterminal, allow it through Defender, then reopen.',
  },
];

export function BootstrapErrorState({ error, onRetry }: Props) {
  const [retrying, setRetrying] = useState(false);
  const [health, setHealth] = useState<HealthCheck>({
    status: 'idle',
    message: 'Daemon health has not been checked from this screen yet.',
  });
  const [showRaw, setShowRaw] = useState(false);

  const retry = async () => {
    if (retrying) return;
    setRetrying(true);
    try {
      await onRetry();
    } finally {
      setRetrying(false);
    }
  };

  const checkHealth = async () => {
    setHealth({ status: 'checking', message: 'Checking daemon health...' });
    try {
      const h = await api.health();
      const label = h.version ? `Daemon reachable (${h.version})` : 'Daemon reachable';
      setHealth({ status: 'ok', message: label });
    } catch (err) {
      setHealth({ status: 'error', message: `Daemon health check failed: ${errorMessage(err)}` });
    }
  };

  return (
    <div className="bootstrap-error-state" role="alert" aria-live="assertive">
      <div className="bootstrap-error-card">
        <div className="bootstrap-error-kicker">Daemon unreachable</div>
        <h2>Could not load sessions</h2>
        <p>
          The app couldn’t reach its daemon, so the session grid didn’t load. The
          likely causes, most common first:
        </p>
        <ol className="bootstrap-cause-list">
          {CAUSES.map((c) => (
            <li key={c.title} className="bootstrap-cause">
              <span className="bootstrap-cause-title">{c.title}</span>
              <span className="bootstrap-cause-detail">{c.detail}</span>
            </li>
          ))}
        </ol>
        <div className="bootstrap-error-actions">
          <button type="button" className="primary" onClick={() => void retry()} disabled={retrying}>
            {retrying ? 'Retrying...' : 'Retry bootstrap'}
          </button>
          <button type="button" className="secondary" onClick={() => void checkHealth()} disabled={health.status === 'checking'}>
            {health.status === 'checking' ? 'Checking...' : 'Check daemon health'}
          </button>
          <button type="button" className="secondary" onClick={() => setShowRaw((v) => !v)}>
            {showRaw ? 'Hide details' : 'Show details'}
          </button>
        </div>
        <div className={`bootstrap-health bootstrap-health-${health.status}`}>
          {health.message}
        </div>
        {showRaw && <pre className="bootstrap-error-detail">{error}</pre>}
      </div>
    </div>
  );
}
