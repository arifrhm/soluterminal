import { describe, expect, it } from 'vitest';
import { scheduleUnread } from './scheduleUnread';
import type { Schedule } from '../types';

function sched(over: Partial<Schedule>): Schedule {
  return {
    id: 's1', name: 'Digest', kind: 'task', enabled: true,
    createdAt: '2026-07-01T00:00:00Z', fireCount: 1, skipCount: 0, errorCount: 0,
    runs: [{ at: '2026-07-10T09:00:00Z', status: 'fired' }],
    ...over,
  } as Schedule;
}

describe('scheduleUnread', () => {
  it('is unread when never read, read once marker catches up', () => {
    expect(scheduleUnread(sched({}))).toBe(true);
    expect(scheduleUnread(sched({ lastReadAt: '2026-07-10T09:00:00Z' }))).toBe(false);
    expect(scheduleUnread(sched({ lastReadAt: '2026-07-09T09:00:00Z' }))).toBe(true);
  });

  it('errors count, skips and non-task kinds never do', () => {
    expect(scheduleUnread(sched({ runs: [{ at: '2026-07-10T09:00:00Z', status: 'error' }] }))).toBe(true);
    expect(scheduleUnread(sched({ runs: [{ at: '2026-07-10T09:00:00Z', status: 'skipped' }] }))).toBe(false);
    expect(scheduleUnread(sched({ kind: 'prompt' }))).toBe(false);
    expect(scheduleUnread(sched({ kind: 'agent' }))).toBe(false);
    expect(scheduleUnread(sched({ runs: [] }))).toBe(false);
  });
});
