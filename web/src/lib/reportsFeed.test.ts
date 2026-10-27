import { describe, expect, it } from 'vitest';
import { reportsFeed, runNeedsYou, runPreview } from './reportsFeed';
import type { Schedule, ScheduleRun } from '../types';

function sched(over: Partial<Schedule> = {}): Schedule {
  return {
    id: 's1',
    name: 'Morning digest',
    kind: 'task',
    enabled: true,
    createdAt: '2026-07-01T00:00:00Z',
    fireCount: 0,
    skipCount: 0,
    errorCount: 0,
    ...over,
  } as Schedule;
}

function run(over: Partial<ScheduleRun> = {}): ScheduleRun {
  return { at: '2026-07-10T09:00:00Z', status: 'fired', ...over };
}

describe('reportsFeed', () => {
  it('merges task runs across schedules newest-first, excluding skips and non-task kinds', () => {
    const items = reportsFeed([
      sched({
        id: 'a',
        runs: [
          run({ at: '2026-07-10T09:00:00Z' }),
          run({ at: '2026-07-09T09:00:00Z', status: 'skipped' }),
          run({ at: '2026-07-08T09:00:00Z' }),
        ],
      }),
      sched({ id: 'b', runs: [run({ at: '2026-07-09T21:00:00Z' })] }),
      sched({ id: 'c', kind: 'agent', runs: [run({ at: '2026-07-10T23:00:00Z' })] }),
      sched({ id: 'd', kind: 'prompt', runs: [run({ at: '2026-07-10T22:00:00Z' })] }),
    ]);
    expect(items.map((i) => `${i.schedule.id}:${i.run.at}`)).toEqual([
      'a:2026-07-10T09:00:00Z',
      'b:2026-07-09T21:00:00Z',
      'a:2026-07-08T09:00:00Z',
    ]);
  });

  it('computes per-run unread against the schedule lastReadAt marker', () => {
    const items = reportsFeed([
      sched({
        lastReadAt: '2026-07-09T09:00:00Z',
        runs: [
          run({ at: '2026-07-10T09:00:00Z' }),
          run({ at: '2026-07-09T09:00:00Z' }),
          run({ at: '2026-07-08T09:00:00Z' }),
        ],
      }),
    ]);
    expect(items.map((i) => i.unread)).toEqual([true, false, false]);
    // No marker at all → everything unread.
    expect(reportsFeed([sched({ runs: [run()] })])[0].unread).toBe(true);
  });

  it('caps the feed', () => {
    const runs = Array.from({ length: 20 }, (_, i) =>
      run({ at: `2026-07-${String(i + 1).padStart(2, '0')}T09:00:00Z` }));
    const items = reportsFeed([sched({ runs })], 5);
    expect(items).toHaveLength(5);
    expect(items[0].run.at).toBe('2026-07-20T09:00:00Z');
  });
});

describe('runNeedsYou', () => {
  it('detects the contract heading, case-insensitively, and error runs', () => {
    expect(runNeedsYou(run({ summary: '## Needs you\n- approve PR #7' }))).toBe(true);
    expect(runNeedsYou(run({ summary: '  ## needs YOU — two things' }))).toBe(true);
    expect(runNeedsYou(run({ summary: 'Nothing needs you today.\n\n## CI' }))).toBe(false);
    expect(runNeedsYou(run({ summary: '## Needs your attention later' }))).toBe(false);
    expect(runNeedsYou(run({ status: 'error', summary: undefined }))).toBe(true);
    expect(runNeedsYou(run({ summary: undefined }))).toBe(false);
  });
});

describe('runPreview', () => {
  it('drops a leading heading, strips markers, joins lines', () => {
    expect(runPreview(run({ summary: '## Needs you\n- approve **PR #7**\n- rotate `token`' })))
      .toBe('approve PR #7 · rotate token');
    expect(runPreview(run({ summary: 'Nothing needs you today.' })))
      .toBe('Nothing needs you today.');
  });

  it('uses the detail line for error runs', () => {
    expect(runPreview(run({ status: 'error', detail: 'plan · exit 1 in 9s' })))
      .toBe('plan · exit 1 in 9s');
  });
});
