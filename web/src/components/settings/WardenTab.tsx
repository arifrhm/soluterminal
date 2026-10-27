import { useCallback, useEffect, useState } from 'react';
import { useStore } from '../../store';
import { api } from '../../api';
import { renderReportMarkdown } from '../../lib/reportMarkdown';
import { IconShield } from '../ui/icons';
import { SettingsGroup, SettingsCheck, SettingsRow } from './SettingsRow';
import type { WardenRun } from '../../types';

// The Warden settings tab: the opt-in toggle (the deferred UI from
// PLAN-warden), its tuning knobs, and an in-app viewer for the briefs it
// produces — so reading a brief no longer needs curl. Reports stay on the
// Warden's own isolated surface (GET /api/warden/status + /runs/{name});
// wiring them into the shared inbox is a separate, later step.

const RUN_ICON: Record<string, string> = { brief: '🔭', debrief: '📓', triage: '🚩' };

function whenLabel(iso: string): string {
  const t = new Date(iso).getTime();
  if (Number.isNaN(t)) return iso;
  const mins = Math.round((Date.now() - t) / 60000);
  if (mins < 1) return 'just now';
  if (mins < 60) return `${mins}m ago`;
  const hrs = Math.round(mins / 60);
  if (hrs < 24) return `${hrs}h ago`;
  return new Date(t).toLocaleDateString();
}

export function WardenTab() {
  const warden = useStore((s) => s.wardenSettings);
  const save = useStore((s) => s.saveWardenSettings);
  const toast = useStore((s) => s.toast);

  const [runs, setRuns] = useState<WardenRun[]>([]);
  const [open, setOpen] = useState<{ file: string; text: string } | null>(null);
  const [running, setRunning] = useState(false);
  const [loadingLog, setLoadingLog] = useState(false);

  const refresh = useCallback(async () => {
    try {
      const s = await api.getWardenStatus();
      setRuns(s.runs ?? []);
    } catch {
      // Older daemon without the warden endpoints — leave the list empty.
    }
  }, []);

  useEffect(() => {
    void refresh();
  }, [refresh]);

  const enabled = warden?.enabled ?? false;

  const openRun = async (run: WardenRun) => {
    if (!run.logFile) return;
    if (open?.file === run.logFile) {
      setOpen(null);
      return;
    }
    setLoadingLog(true);
    try {
      setOpen({ file: run.logFile, text: await api.wardenRunLog(run.logFile) });
    } catch (e) {
      toast(`Couldn't load brief: ${(e as Error).message}`, { kind: 'error', icon: '!', ttlMs: 5000 });
    } finally {
      setLoadingLog(false);
    }
  };

  const runNow = async () => {
    setRunning(true);
    try {
      const r = await api.wardenRunNow();
      if (!r.started) {
        toast(r.reason ?? 'A Warden run is already in flight.', { kind: 'info', ttlMs: 5000 });
        return;
      }
      const before = runs.length;
      // A brief is a real headless run (~1–2 min). Poll for the new record.
      for (let i = 0; i < 45; i++) {
        await new Promise((res) => setTimeout(res, 4000));
        let s;
        try {
          s = await api.getWardenStatus();
        } catch (e) {
          toast(`Couldn't check the brief status: ${(e as Error).message}`, { kind: 'error', icon: '!', ttlMs: 5000 });
          return;
        }
        if ((s.runs?.length ?? 0) > before) {
          setRuns(s.runs);
          if (s.runs[0]?.logFile) void openRun(s.runs[0]).catch(() => {});
          return;
        }
      }
      toast('Brief is taking a while — refresh in a moment.', { kind: 'info', ttlMs: 5000 });
      void refresh().catch(() => {});
    } catch (e) {
      toast(`Couldn't start a brief: ${(e as Error).message}`, { kind: 'error', icon: '!', ttlMs: 5000 });
    } finally {
      setRunning(false);
    }
  };

  return (
    <>
      <SettingsGroup title="Fleet caretaker">
        <SettingsCheck
          label="Enable the Warden"
          checked={enabled}
          onChange={(v) => void save({ enabled: v }).catch(() => {})}
          hint="Off by default. When on, the daemon prepares triage briefs and debriefs without typing into your sessions. Automatic pane flags have a separate option."
        />
        <SettingsCheck
          label="Automatic pane flags"
          checked={warden?.autoFlags ?? false}
          onChange={(v) => void save({ autoFlags: v }).catch(() => {})}
          hint="Off by default. When enabled, a Warden triage may flag its needs-user pane. Its flag clears when that episode ends; your own flags stay yours. Briefs still run with this off."
        />
        <SettingsRow
          label="Model"
          hint="Which model the Warden's runs use. A cheaper model is usually right for read-and-summarize work."
        >
          <select
            className="settings-select"
            value={warden?.model ?? ''}
            onChange={(e) => void save({ model: e.target.value }).catch(() => {})}
          >
            <option value="">Account default</option>
            <option value="claude-haiku-4-5-20251001">Haiku (fast, cheap)</option>
            <option value="claude-sonnet-5">Sonnet</option>
          </select>
        </SettingsRow>
        <SettingsRow
          label="Triage after"
          hint="How long a pane must keep needing you before the Warden prepares a triage brief. The real-time alert fires earlier; this is the follow-up."
        >
          <span className="warden-num">
            <input
              type="number"
              className="settings-input"
              min={2}
              max={120}
              value={warden?.triageAfterMin ?? 10}
              onChange={(e) => void save({ triageAfterMin: clamp(e.target.value, 2, 120) }).catch(() => {})}
            />
            <span className="settings-unit">min</span>
          </span>
        </SettingsRow>
        <SettingsRow
          label="Max runs / hour"
          hint="A ceiling on total Warden runs per hour — the spend cap and the fork-bomb guard."
        >
          <input
            type="number"
            className="settings-input"
            min={1}
            max={30}
            value={warden?.maxRunsPerHour ?? 6}
            onChange={(e) => void save({ maxRunsPerHour: clamp(e.target.value, 1, 30) }).catch(() => {})}
          />
        </SettingsRow>
      </SettingsGroup>

      <SettingsGroup title="Briefs">
        <div className="warden-briefs">
          <div className="warden-briefs-head">
            <button
              type="button"
              className="settings-btn warden-run-btn"
              onClick={() => void runNow().catch(() => {})}
              disabled={running}
            >
              <IconShield size={13} />
              {running ? 'Running a brief…' : 'Run a fleet brief now'}
            </button>
            <span className="settings-row-hint" style={{ margin: 0 }}>
              A brief walks every pane and reports what needs you. Works even while the Warden is off.
            </span>
          </div>

          {runs.length === 0 ? (
            <p className="warden-empty">No briefs yet. Run one above, or let the Warden fire them on its own.</p>
          ) : (
            <ul className="warden-run-list">
              {runs.map((run, i) => (
                <li key={run.logFile ?? i}>
                  <button
                    type="button"
                    className={`warden-run-row${open?.file === run.logFile ? ' active' : ''}`}
                    onClick={() => void openRun(run)}
                    disabled={!run.logFile}
                  >
                    <span className="warden-run-kind">{RUN_ICON[run.kind] ?? '•'} {run.kind}</span>
                    {run.paneTitle && <span className="warden-run-pane">{run.paneTitle}</span>}
                    <span className="warden-run-when">{whenLabel(run.at)}</span>
                    {run.status !== 'ok' && <span className="warden-run-bad">{run.status}</span>}
                  </button>
                </li>
              ))}
            </ul>
          )}

          {loadingLog && <p className="warden-empty">Loading brief…</p>}
          {open && !loadingLog && (
            <div className="warden-brief-reader report-md report-md-reader">
              {renderReportMarkdown(open.text)}
            </div>
          )}
        </div>
      </SettingsGroup>
    </>
  );
}

function clamp(raw: string, lo: number, hi: number): number {
  const n = Math.round(Number(raw));
  if (Number.isNaN(n)) return lo;
  return Math.min(hi, Math.max(lo, n));
}
