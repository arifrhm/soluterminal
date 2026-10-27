import { useCallback, useEffect, useRef, useState } from 'react';
import { api, ApiError } from '../../api';
import type { ReleaseTrainStatus, WorkflowSettings } from '../../api';
import { NumberInput } from './NumberInput';
import { SettingsCheck, SettingsGroup, SettingsRow } from './SettingsRow';
import type { WorkflowSettingsUpdate } from './ServicesTab';

type ReleaseTrain = NonNullable<WorkflowSettings['releaseTrain']>;

const PREVIEW_POLL_MS = 30_000;

function localTime(iso?: string): string {
  if (!iso) return 'unknown';
  const at = Date.parse(iso);
  if (!Number.isFinite(at)) return 'unknown';
  return new Date(at).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
}

export function ReleaseTrainSection({ sharedSettings, updateSharedSettings }: {
  sharedSettings?: WorkflowSettings;
  updateSharedSettings?: WorkflowSettingsUpdate;
} = {}) {
  const [localSettings, setLocalSettings] = useState<WorkflowSettings | null>(null);
  const [status, setStatus] = useState<ReleaseTrainStatus | null>(null);
  // true = this daemon predates the release train; the section hides entirely.
  const [unsupported, setUnsupported] = useState(false);
  const [error, setError] = useState('');
  const saveVersion = useRef(0);
  const settings = sharedSettings ?? localSettings;
  const shared = sharedSettings !== undefined && updateSharedSettings !== undefined;

  useEffect(() => {
    if (shared) return;
    let cancelled = false;
    api.getWorkflowSettings().then((next) => {
      if (!cancelled) setLocalSettings(next);
    }).catch(() => {});
    return () => { cancelled = true; };
  }, [shared]);

  const load = useCallback(async (refresh: boolean, alive: () => boolean) => {
    try {
      const next = await api.getReleaseTrain(refresh);
      if (alive()) setStatus(next);
    } catch (cause) {
      if (!alive()) return;
      // A daemon without the endpoint hides the whole section rather than
      // showing controls that cannot do anything.
      if (cause instanceof ApiError && (cause.status === 404 || cause.status === 503)) setUnsupported(true);
    }
  }, []);

  useEffect(() => {
    let cancelled = false;
    const alive = () => !cancelled;
    void load(true, alive);
    const timer = setInterval(() => { void load(false, alive); }, PREVIEW_POLL_MS);
    return () => { cancelled = true; clearInterval(timer); };
  }, [load]);

  if (!settings || unsupported) return null;

  const train = settings.releaseTrain ?? {};
  const enabled = train.enabled ?? false;
  const minLandings = train.minFunctionalLandings ?? 6;
  const maxAgeHours = train.maxUnreleasedAgeHours ?? 12;
  const vetoMinutes = train.notifyVetoMinutes ?? 10;

  const update = async (patch: ReleaseTrain) => {
    const version = ++saveVersion.current;
    const previous = settings;
    if (!shared) setLocalSettings({ ...settings, releaseTrain: { ...settings.releaseTrain, ...patch } });
    setError('');
    try {
      let normalized: WorkflowSettings;
      if (shared) {
        normalized = await updateSharedSettings((current) => ({
          ...current,
          releaseTrain: { ...current.releaseTrain, ...patch },
        }));
      } else {
        const current = await api.getWorkflowSettings();
        normalized = await api.setWorkflowSettings({
          ...current,
          releaseTrain: { ...current.releaseTrain, ...patch },
        });
      }
      if (saveVersion.current === version && !shared) setLocalSettings(normalized);
    } catch (cause) {
      if (saveVersion.current === version) {
        if (!shared) setLocalSettings(previous);
        setError(cause instanceof Error ? cause.message : String(cause));
      }
    }
  };

  const veto = async () => {
    setError('');
    try {
      setStatus(await api.vetoReleaseTrain());
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : String(cause));
    }
  };

  const preview = status?.preview ?? null;
  const phase = status?.state.phase ?? 'idle';

  return <SettingsGroup title="Release train">
    <SettingsCheck
      label="Auto-publish on cadence (release train)"
      checked={enabled}
      onChange={(next) => void update({ enabled: next })}
      hint="Off by default. When on, the daemon announces a train with a push notification, waits the veto window, then runs ./publish.sh from an isolated worktree of origin/main with the full test gate. Only work you already approved and merged can ship — nothing is ever approved, landed, or merged automatically."
    />
    <SettingsRow label="Functional landings before a train" hint="Counts commits that change functional paths since the last release; documentation and version-bump commits do not count. Default 6.">
      <NumberInput label="Functional landings before a train" value={minLandings} min={1} max={500} placeholder="6"
        onCommit={(next) => void update({ minFunctionalLandings: next })} />
    </SettingsRow>
    <SettingsRow label="Max unreleased age (hours)" hint="A single landing older than this departs on its own, even below the landing threshold. Default 12.">
      <NumberInput label="Max unreleased age (hours)" value={maxAgeHours} min={1} max={720} placeholder="12"
        onCommit={(next) => void update({ maxUnreleasedAgeHours: next })} />
    </SettingsRow>
    <SettingsRow label="Veto window (minutes)" hint="How long the announced train waits before it departs. Default 10.">
      <NumberInput label="Veto window (minutes)" value={vetoMinutes} min={1} max={1440} placeholder="10"
        onCommit={(next) => void update({ notifyVetoMinutes: next })} />
    </SettingsRow>

    <div className="settings-row">
      <span className="settings-row-label">Next train</span>
      <div className="settings-row-control">
        <span className={`release-scope-badge is-${phase === 'parked' || phase === 'disabled' ? 'warn' : 'ok'}`}>{phase.replace('_', ' ')}</span>
      </div>
      {preview && <p className="settings-row-hint">
        v{preview.version || 'unavailable'} · {preview.functionalLandings}/{preview.minFunctionalLandings} functional landings
        {' '}· oldest {preview.ageHours.toFixed(1)}h (max {preview.maxAgeHours}h)
      </p>}
      {preview && !preview.eligible && (preview.reasons?.length ?? 0) > 0 && <ul className="settings-row-hint release-train-reasons">
        {(preview.reasons ?? []).map((reason) => <li key={reason}>{reason}</li>)}
      </ul>}
      {(phase === 'declared' || phase === 'holding') && <p className="settings-row-hint">
        Departing at {localTime(status?.state.train?.departAt)}
        {' '}<button type="button" className="settings-btn" onClick={() => void veto()}>Veto</button>
      </p>}
      {phase === 'parked' && status?.state.parked && <p className="settings-row-hint">
        Parked: {status.state.parked.reason} — lifts when new commits land on origin/main or the policy is re-enabled.
      </p>}
      {phase === 'restart_pending' && status?.state.restartPending && <p className="settings-row-hint">
        Restart pending v{status.state.restartPending.version} — {preview?.restartNote || 'the updater retries automatically once in-flight work and Focus panes allow.'}
      </p>}
      {status?.state.train?.status === 'published' && status.state.train.supervisionLost && <p className="settings-row-hint">
        Published v{status.state.train.version} — {status.state.train.publishNote || 'the publish completed; the daemon restarted before the train could record it.'}
      </p>}
    </div>
    {status && (status.events?.length ?? 0) > 0 && <div className="settings-row">
      <span className="settings-row-label">Recent</span>
      <div className="settings-row-control" />
      <ul className="settings-row-hint release-train-events">
        {(status.events ?? []).slice(-5).reverse().map((event) => <li key={event.seq}>
          {event.type.replace('release_train.', '')} · {localTime(event.at)}
        </li>)}
      </ul>
    </div>}
    {error && <p className="settings-row-hint settings-error">Couldn't update the release train: {error}</p>}
  </SettingsGroup>;
}
