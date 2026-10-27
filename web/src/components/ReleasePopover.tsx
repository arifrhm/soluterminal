import { StatusChip } from "./shared/StatusChip";
import { useEffect, useRef } from 'react';
import { createPortal } from 'react-dom';
import type { ReleaseTrainStatus } from '../api';
import type { ReleaseCheck, ReleaseReadiness } from '../types';

function trainTime(iso?: string): string {
  if (!iso) return 'soon';
  const at = Date.parse(iso);
  if (!Number.isFinite(at)) return 'soon';
  return new Date(at).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
}

function forecastTime(iso: string, now = Date.now()): string {
  const at = Date.parse(iso);
  if (!Number.isFinite(at)) return 'soon';
  const target = new Date(at);
  const current = new Date(now);
  const sameDay = target.getFullYear() === current.getFullYear()
    && target.getMonth() === current.getMonth()
    && target.getDate() === current.getDate();
  if (sameDay) return target.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
  return target.toLocaleString([], { weekday: 'short', hour: '2-digit', minute: '2-digit' });
}

function trainStatusOf(train: unknown): ReleaseTrainStatus | null {
  if (!train || typeof train !== 'object' || Array.isArray(train)) return null;
  const state = (train as { state?: unknown }).state;
  if (!state || typeof state !== 'object' || Array.isArray(state)) return null;
  return train as ReleaseTrainStatus;
}

const FAILURE_LABELS: Record<string, string> = {
  test_assertion: 'test failure',
  package_timeout: 'package timeout',
  outer_timeout: 'timeout',
  resource: 'resource pressure',
  network_transient: 'network',
  source_drift: 'source drift',
  unknown: 'unknown',
};

function failureLabel(value?: string): string {
  return FAILURE_LABELS[value || 'unknown'] || 'unknown';
}

// An idle or disabled train has no departure row; every other phase gets
// exactly one line.
function hasTrainRow(status: ReleaseTrainStatus | null): status is ReleaseTrainStatus {
  const publishState = status?.publishState;
  if (publishState === 'queued' || publishState === 'testing' || publishState === 'failed'
    || publishState === 'retrying' || publishState === 'published') return true;
  switch (status?.state.phase) {
    case 'declared': case 'holding': case 'publishing': case 'parked': case 'restart_pending':
      return true;
    default:
      return false;
  }
}

// One line about the opt-in publish cadence, rendered only when a train
// actually exists. The chip's existing content is untouched otherwise.
function ReleaseTrainRow({ train, onVetoTrain }: { train: ReleaseTrainStatus; onVetoTrain?: () => void }) {
  const phase = train.state.phase;
  const publishState = train.publishState;
  const legacyPublishState = !publishState || publishState === 'idle';
  const version = train.state.train?.version || train.preview?.version || '';
  if (publishState === 'queued' || (legacyPublishState && (phase === 'declared' || phase === 'holding'))) {
    return <div className="vp-summary release-train-row">
      <StatusChip status="pending" label="queued" />
      <span>
        Release train v{version} departs {trainTime(train.state.train?.departAt)}
        {' '}· veto window {train.policy.notifyVetoMinutes} min
      </span>
      {onVetoTrain && <button type="button" className="settings-btn" onClick={onVetoTrain}>Veto</button>}
    </div>;
  }
  if (legacyPublishState && phase === 'parked' && train.state.parked) {
    return <div className="vp-summary release-train-row">Release train parked: {train.state.parked.reason}</div>;
  }
  if (publishState === 'failed' && train.state.parked) {
    const park = train.state.parked;
    const action = park.retryEligible
      ? `auto-retry at ${trainTime(park.nextRetryAt)}`
      : park.failureClass === 'test_assertion'
        ? 'parked until a fixing commit lands'
        : 'needs manual publish decision';
    const attempts = `${park.attempt ?? 0}/${park.maxAttempts ?? 1}`;
    return <div className="vp-summary release-train-row release-train-failure">
      <div className="release-train-state-line">
        <StatusChip status="failed" label="failed" />
        <StatusChip status="failed" label={failureLabel(park.failureClass)} muted />
      </div>
      <div>last attempt {trainTime(park.at)} · retry {attempts} · {park.retryEligible ? 'eligible' : 'not eligible'}</div>
      {park.evidence && <div className="release-train-evidence">{park.evidence}</div>}
      <div>{action}</div>
    </div>;
  }
  if (phase === 'restart_pending' && train.state.restartPending) {
    return <div className="vp-summary release-train-row">Release train: restart pending v{train.state.restartPending.version}</div>;
  }
  if (publishState === 'retrying') {
    const attempt = train.state.parked?.attempt ?? 1;
    const max = train.state.parked?.maxAttempts ?? 1;
    return <div className="vp-summary release-train-row">
      <StatusChip status="running" label="retrying" />
      <span>v{version} · retry {attempt}/{max} · full test gate running</span>
    </div>;
  }
  if (publishState === 'testing' || (legacyPublishState && phase === 'publishing')) {
    return <div className="vp-summary release-train-row">
      <StatusChip status="running" label="testing" />
      <span>v{version} · full test gate running</span>
    </div>;
  }
  if (publishState === 'published') {
    return <div className="vp-summary release-train-row">
      <div className="release-train-state-line">
        <StatusChip status="completed" label="published" />
        {train.state.train?.supervisionLost && <StatusChip status="completed" label="supervision lost at restart" muted />}
      </div>
      <span>v{version} · source {train.state.train?.sourceSha?.slice(0, 7) || 'captured'}</span>
      {train.state.train?.supervisionLost && train.state.train.publishNote
        && <div className="release-train-evidence">{train.state.train.publishNote}</div>}
    </div>;
  }
  return null;
}

function ReleaseForecast({
  status,
  releaseCheck,
}: {
  status: ReleaseTrainStatus | null;
  releaseCheck?: ReleaseCheck | null;
}) {
  if (status === null) {
    return <div className="vp-section release-forecast" data-state="unavailable">
      <div className="vp-section-title">Next auto release</div>
      <div className="release-forecast-eta">Release-train status unavailable.</div>
    </div>;
  }
  const phase = status.state.phase;
  if (phase !== 'idle' && phase !== 'disabled') return null;
  const publishState = status.publishState;
  if (publishState && publishState !== 'idle' && publishState !== 'published') return null;
  const preview = status.preview;
  if (phase === 'disabled') {
    return <div className="vp-section release-forecast" data-state="off">
      <div className="vp-section-title">Next auto release</div>
      <div className="release-forecast-eta">
        Off — {preview?.reasons?.[0] ?? 'release train policy is disabled'}
      </div>
      <div className="release-forecast-meta">Enable the release train under Settings → Services.</div>
    </div>;
  }
  if (!preview) {
    return <div className="vp-section release-forecast" data-state="unavailable">
      <div className="vp-section-title">Next auto release</div>
      <div className="release-forecast-eta">Not evaluated yet — the daemon refreshes this within a minute.</div>
    </div>;
  }

  const checkSummary = releaseCheck === null
    ? ' · release check: unavailable (fails closed)'
    : releaseCheck === undefined
      ? ''
      : ` · release check: ${releaseCheck.blockers.length ? `${releaseCheck.blockers.length} blocker(s)` : 'no blockers'}, ${releaseCheck.warnings.length} ${releaseCheck.warnings.length === 1 ? 'advisory' : 'advisories'}`;
  const meta = `${status.policy.notifyVetoMinutes}-min veto notice before departure${checkSummary}`;

  if (preview.eligible) {
    return <div className="vp-section release-forecast" data-state="eligible">
      <div className="vp-section-title">Next auto release</div>
      <div className="release-forecast-eta">
        Eligible now — departs at <time {...(preview.wouldDepartAt ? { dateTime: preview.wouldDepartAt } : {})}>
          {preview.wouldDepartAt ? forecastTime(preview.wouldDepartAt) : 'soon'}
        </time> after a {status.policy.notifyVetoMinutes}-min veto notice.
      </div>
      <div className="release-forecast-meta">{meta}</div>
    </div>;
  }

  // The evaluator snapshot owns trigger state. If either daemon-derived OR
  // trigger was already satisfied when it evaluated this preview, the train
  // is waiting on another reason and an ETA would be misleading.
  const evaluatedAt = Date.parse(preview.evaluatedAt);
  const ageTriggerAt = preview.ageTriggerAt ? Date.parse(preview.ageTriggerAt) : Number.NaN;
  const triggerAlreadySatisfied = preview.landingsRemaining === 0
    || (Number.isFinite(evaluatedAt) && Number.isFinite(ageTriggerAt) && ageTriggerAt <= evaluatedAt);

  if (triggerAlreadySatisfied) {
    return <div className="vp-section release-forecast" data-state="waiting">
      <div className="vp-section-title">Next auto release</div>
      <div className="release-forecast-eta">
        Held — {preview.reasons?.[0] ?? 'release train is waiting on another condition'}
      </div>
      <ul className="release-forecast-reasons" aria-label="Waiting on">
        {(preview.reasons ?? []).map((reason) => <li key={reason}>{reason}</li>)}
      </ul>
      <div className="release-forecast-meta">{meta}</div>
    </div>;
  }

  const remaining = preview.landingsRemaining;
  const landingsClause = remaining === undefined
    ? `Auto release when ${preview.minFunctionalLandings} functional landings have landed`
    : remaining > 0
      ? `After ${remaining} more functional landing${remaining === 1 ? '' : 's'} (${preview.functionalLandings} of ${preview.minFunctionalLandings})`
      : '';
  const ageClause = preview.ageTriggerAt
    ? <>{landingsClause ? 'or at ' : 'At '}<time dateTime={preview.ageTriggerAt}>{forecastTime(preview.ageTriggerAt)}</time> ({preview.ageHours.toFixed(1)}h of {preview.maxAgeHours}h)</>
    : <>{landingsClause ? 'or when' : 'When'} the oldest functional landing reaches {preview.maxAgeHours}h (age unknown)</>;

  return <div className="vp-section release-forecast" data-state="waiting">
    <div className="vp-section-title">Next auto release</div>
    <div className="release-forecast-eta">
      {landingsClause}{landingsClause ? ', ' : ''}{ageClause}{landingsClause ? ' — whichever first.' : '.'}
    </div>
    <ul className="release-forecast-reasons" aria-label="Waiting on">
      {(preview.reasons ?? []).map((reason) => <li key={reason}>{reason}</li>)}
    </ul>
    <div className="release-forecast-meta">{meta}</div>
  </div>;
}

function releaseAge(iso: string, computedAt: string): string {
  const at = Date.parse(iso);
  const snapshotAt = Date.parse(computedAt);
  if (!Number.isFinite(at) || !Number.isFinite(snapshotAt)) return 'unknown';
  const ms = Math.max(0, snapshotAt - at);
  const hours = Math.floor(ms / 3_600_000);
  if (hours < 24) return `${hours}h`;
  return `${Math.floor(hours / 24)}d`;
}

export function ReleasePopover({
  anchor,
  readiness,
  releaseCheck,
  train,
  onVetoTrain,
  onClose,
}: {
  anchor: HTMLElement;
  readiness: ReleaseReadiness;
  releaseCheck?: ReleaseCheck | null;
  train?: ReleaseTrainStatus | null;
  onVetoTrain?: () => void;
  onClose: () => void;
}) {
  const popRef = useRef<HTMLDivElement | null>(null);

  useEffect(() => {
    function onKey(event: KeyboardEvent) {
      if (event.key === 'Escape') {
        event.stopPropagation();
        onClose();
      }
    }
    function onClick(event: MouseEvent) {
      const target = event.target as Node | null;
      if (!target || popRef.current?.contains(target) || anchor.contains(target)) return;
      onClose();
    }
    window.addEventListener('keydown', onKey, true);
    // Deliberately click, not mousedown: unmounting between down/up can swallow
    // clicks on content inside a portaled popover.
    window.addEventListener('click', onClick, true);
    return () => {
      window.removeEventListener('keydown', onKey, true);
      window.removeEventListener('click', onClick, true);
    };
  }, [anchor, onClose]);

  const rect = anchor.getBoundingClientRect();
  const margin = 8;
  const width = Math.min(380, Math.max(280, window.innerWidth - margin * 2));
  let left = rect.right - width;
  if (left < margin) left = margin;
  if (left + width > window.innerWidth - margin) left = window.innerWidth - width - margin;
  const version = readiness.suggestedNextVersion;
  const status = trainStatusOf(train);
  const landings = status?.preview?.functionalLandings ?? readiness.functionalCommitCount;
  const cut = releaseCheck?.cut;
  const sourceSha = cut?.sourceSha || '';
  let scopeVerdict = 'Release required to activate merged work';
  if (releaseCheck === null) {
    scopeVerdict = 'BLOCKED — release services unavailable — releases fail closed';
  } else if (releaseCheck?.allowed) {
    scopeVerdict = `Scope safe · v${cut?.version || version || 'unknown'} · source ${sourceSha ? sourceSha.slice(0, 7) : 'not captured'}`;
  } else if (releaseCheck) {
    scopeVerdict = releaseCheck.blockers.length > 0
      ? `BLOCKED — ${releaseCheck.blockers.length} task(s) in scope`
      : `BLOCKED — ${releaseCheck.warnings[0] || 'release cut is not safe to publish'}`;
  }
  const publishState = hasTrainRow(status) ? status.publishState : undefined;
  const trainVersion = hasTrainRow(status) ? (status.state.train?.version || status.preview?.version || version) : version;
  const failedClass = hasTrainRow(status) ? failureLabel(status.state.parked?.failureClass) : 'unknown';
  let verdict = scopeVerdict;
  if (publishState === 'failed') verdict = `Publish failed · ${failedClass}`;
  else if (publishState === 'retrying') verdict = `Release retrying · v${trainVersion}`;
  else if (publishState === 'testing') verdict = `Release testing · v${trainVersion}`;
  else if (publishState === 'queued') verdict = `Release queued · v${trainVersion}`;
  else if (publishState === 'published') {
    verdict = `Published · v${trainVersion}${status?.state.train?.supervisionLost ? ' · supervision lost at restart' : ''}`;
  }
  const publishOverridesScope = !!publishState && publishState !== 'idle';
  const headlineSafe = publishState === 'published' || (!publishOverridesScope && releaseCheck?.allowed);

  return createPortal(
    <div
      ref={popRef}
      className="version-popover release-popover"
      style={{ left, bottom: window.innerHeight - rect.top + 6, width }}
      role="dialog"
      aria-label="Release readiness"
    >
      <div className={`vp-verdict ${headlineSafe ? 'release-verdict-safe' : 'vp-verdict-warn'}`}>{verdict}</div>
      {publishOverridesScope && <div className="vp-summary release-scope-verdict">{scopeVerdict}</div>}
      <div className="vp-summary release-delta">
        {readiness.unreleasedTotal} unreleased commits since v{readiness.lastReleaseVersion}
        {landings !== undefined && <>{' '}· {landings} functional landing{landings === 1 ? '' : 's'} count toward auto release</>}
        {' '}· {readiness.functionalCount} functional files + {readiness.docsOnlyCount} documentation files changed
        {' '}· oldest {releaseAge(readiness.oldestUnreleasedAt, readiness.computedAt)} ago · daemon running v{readiness.runningVersion}
      </div>
      {train !== undefined && (train === null || status !== null) && (
        <ReleaseForecast status={status} releaseCheck={releaseCheck} />
      )}
      {hasTrainRow(status) && <ReleaseTrainRow train={status} onVetoTrain={onVetoTrain} />}
      {releaseCheck !== undefined && (
        <details className="release-scope" open={releaseCheck === null || releaseCheck?.allowed === false}>
          <summary>
            Release scope
            {releaseCheck === null && <span className="release-scope-badge is-warn">unavailable</span>}
            {releaseCheck?.allowed === false && <span className="release-scope-badge is-warn">
              {releaseCheck.blockers.length > 0 ? `${releaseCheck.blockers.length} blocker(s)` : 'blocked'}
            </span>}
            {releaseCheck?.allowed && <span className="release-scope-badge is-ok">safe</span>}
          </summary>
          {releaseCheck === null ? (
            <div className="release-scope-unavailable">Release services unavailable — releases fail closed.</div>
          ) : releaseCheck && (
            <div className="release-scope-body">
              <div className="release-scope-meta">
                <span>cut <code>{cut?.id || 'implicit / not declared'}</code></span>
                <span>version <code>{cut?.version || version || 'unknown'}</code></span>
                <span>source <code>{sourceSha || 'not captured'}</code></span>
                <span className={`release-scope-badge is-${readiness.anchorProvenance === 'exact' ? 'ok' : 'inferred'}`}>
                  {readiness.anchorProvenance || 'inferred'} provenance
                </span>
              </div>
              {/* Why the anchor is what it is. Without this a degraded anchor
                  (manifest fetch failing, diverged checkout) reads exactly like
                  a healthy one, with a silently wrong delta beside it. */}
              {readiness.anchorNote && <div className="release-scope-meta">{readiness.anchorNote}</div>}
              {releaseCheck.blockers.length > 0 && <div className="release-scope-group">
                <strong>Blocking publication</strong>
                {releaseCheck.blockers.map((blocker) => <div className="release-scope-row" key={blocker.taskId}>
                  <div><code>{blocker.taskId}</code> · {blocker.title}</div>
                  <div className="release-scope-row-meta">
                    <StatusChip status={blocker.landOutcome === 'needs_merge' ? 'needs_merge' : blocker.status} />
                    {blocker.queueState && <span>{blocker.queueState.replaceAll('_', ' ')}</span>}
                    <span>{blocker.why}</span>
                  </div>
                  {blocker.infraCode && <div className="release-scope-row-meta">
                    infrastructure <code>{blocker.infraCode}</code>{blocker.infraReason ? ` · ${blocker.infraReason}` : ''}
                  </div>}
                  {!!blocker.conflictPaths?.length && <div className="release-scope-row-meta">
                    conflict files: {blocker.conflictPaths.map((path, index) => <span key={path}>{index > 0 && ', '}<code>{path}</code></span>)}
                  </div>}
                  {!!blocker.blockedByTaskIds?.length && <div className="release-scope-row-meta">
                    waiting on: {blocker.blockedByTaskIds.map((taskId, index) => <span key={taskId}>{index > 0 && ', '}<code>{taskId}</code></span>)}
                  </div>}
                  {blocker.clearAction && <div className="release-scope-row-meta">Clear by: {blocker.clearAction}</div>}
                </div>)}
              </div>}
              {!!releaseCheck.deadlocks?.length && <div className="release-scope-group release-scope-warning" role="alert">
                <strong>Release/merge cycle detected</strong>
                {releaseCheck.deadlocks.map((deadlock) => <div className="release-scope-row" key={`${deadlock.code}:${deadlock.blockerTaskId}`}>
                  <div><code>{deadlock.code}</code> · blocker <code>{deadlock.blockerTaskId}</code></div>
                  <div className="release-scope-row-meta">Activation tasks: {deadlock.activationTaskIds.map((taskId, index) => <span key={taskId}>{index > 0 && ', '}<code>{taskId}</code></span>)}</div>
                  <div className="release-scope-row-meta">{deadlock.cause}</div>
                  <div className="release-scope-row-meta">Break the cycle: {deadlock.breakAction}</div>
                </div>)}
              </div>}
              {releaseCheck.included.length > 0 && <div className="release-scope-group">
                <strong>Included dependencies</strong>
                {releaseCheck.included.map((included) => <div className="release-scope-row" key={included.taskId}>
                  <div><code>{included.taskId}</code> · {included.title}</div>
                  <div className="release-scope-row-meta">
                    <StatusChip status={included.status} />
                    <span>ancestor {included.ancestor || 'pending'}</span>
                  </div>
                </div>)}
              </div>}
              {releaseCheck.waived.length > 0 && <div className="release-scope-group">
                <strong>Explicit exclusions</strong>
                {releaseCheck.waived.map((waiver) => <div className="release-scope-row" key={waiver.taskId}>
                  <div><code>{waiver.taskId}</code> · waived</div>
                  <div className="release-scope-row-meta">{waiver.reason}</div>
                </div>)}
              </div>}
              {releaseCheck.warnings.map((warning) => <div className="release-scope-warning" key={warning}>{warning}</div>)}
              <div className="release-scope-delta">
                Landed but unreleased: {readiness.unreleasedTotal} commits
                {landings !== undefined && <>{' '}· {landings} functional landings</>}
                {' '}· {readiness.functionalCount} functional files.
              </div>
            </div>
          )}
        </details>
      )}
      <div className="vp-section">
        <div className="vp-section-title">Suggested next version</div>
        <div className="release-next">v{version || 'unavailable'}</div>
      </div>
      <div className="vp-section">
        <div className="vp-section-title">Prepare, publish, activate</div>
        <ol className="release-sequence">
          <li>Declare the release cut and included workflow dependencies.</li>
          <li>Wait for human approval and land every included dependency.</li>
          <li>Verify the typed release blockers are clear.</li>
          <li>Bump v{version}, commit, and push <code>origin main</code>.</li>
          <li>Run <code>./publish.sh</code>; it captures and rechecks one source commit.</li>
          <li>Verify public manifest source SHA, cloud status <code>live</code>, and fleet activation.</li>
        </ol>
      </div>
      <div className="release-info">
        {hasTrainRow(status)
          ? 'Nothing here approves or merges — a veto only parks the announced train.'
          : 'Informational only — nothing here publishes or approves a release.'}
      </div>
    </div>,
    document.body,
  );
}
