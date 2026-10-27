import type { Schedule, ScheduleRun } from '../types';

// The cross-schedule reports feed: every retained task run, newest first,
// regardless of which schedule produced it. The sidebar's Schedules section
// answers "what will run and when"; this answers the daily question — "what
// came in since I last looked" — without making the user remember which
// schedule to tap. Skips are excluded (nothing was produced); errors are
// included (a failure you haven't seen matters MORE than a digest).

export interface ReportFeedItem {
  schedule: Schedule;
  run: ScheduleRun;
  /** Landed after the schedule's daemon-synced lastReadAt marker. */
  unread: boolean;
  /** The report asked for a human — or the run failed. */
  needsYou: boolean;
}

// Triage straight from run.summary — the daemon's reportStyleHint asks every
// task report to open with "## Needs you" when a decision is required, and
// the summary is the report's opening lines, so the contract is machine-
// checkable here without fetching a single log. Errors always need you.
export function runNeedsYou(run: ScheduleRun): boolean {
  if (run.status === 'error') return true;
  return /^##\s*needs you\b/i.test((run.summary || '').trimStart());
}

// One-line preview for feed rows. Drops a leading heading line (the badge
// already says "needs you" — repeating it as text wastes the row), strips
// list markers and emphasis, joins the rest with dots. CSS truncates.
export function runPreview(run: ScheduleRun): string {
  if (run.status === 'error') return run.detail || 'run failed';
  let s = (run.summary || '').trimStart();
  if (s.startsWith('#')) {
    const nl = s.indexOf('\n');
    s = nl === -1 ? '' : s.slice(nl + 1);
  }
  return s
    .split('\n')
    .map((l) => l.replace(/^\s*(?:[-*]|\d+\.|#{1,6}|>)\s+/, '').trim())
    .filter(Boolean)
    .join(' · ')
    .replace(/(\*\*|`|~~)/g, '');
}

export function reportsFeed(schedules: Schedule[], cap = 50): ReportFeedItem[] {
  const items: ReportFeedItem[] = [];
  for (const s of schedules) {
    if (s.kind !== 'task') continue;
    const readAt = s.lastReadAt ? new Date(s.lastReadAt).getTime() : 0;
    for (const r of s.runs || []) {
      if (r.status === 'skipped') continue;
      items.push({
        schedule: s,
        run: r,
        unread: new Date(r.at).getTime() > readAt,
        needsYou: runNeedsYou(r),
      });
    }
  }
  items.sort((a, b) => new Date(b.run.at).getTime() - new Date(a.run.at).getTime());
  return items.slice(0, cap);
}
