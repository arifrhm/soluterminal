import { describe, expect, it } from 'vitest';
import { parseNaturalSchedule } from './scheduleNL';

// Fixed anchor: Thursday 2026-07-09 14:00 local.
const NOW = new Date(2026, 6, 9, 14, 0, 0, 0);

function cronOf(text: string): string | null {
  const p = parseNaturalSchedule(text, NOW);
  return p?.kind === 'cron' ? p.cron : null;
}

function onceOf(text: string): Date | null {
  const p = parseNaturalSchedule(text, NOW);
  return p?.kind === 'once' ? p.at : null;
}

describe('parseNaturalSchedule — recurring', () => {
  const cases: Array<[string, string]> = [
    ['every 30 min', '*/30 * * * *'],
    ['every 30 minutes', '*/30 * * * *'],
    ['every minute', '* * * * *'],
    ['every 2 hours', '0 */2 * * *'],
    ['every hour', '0 * * * *'],
    ['hourly', '0 * * * *'],
    ['every day', '0 9 * * *'],
    ['daily at 6pm', '0 18 * * *'],
    ['daily at 6:30pm', '30 18 * * *'],
    ['every day at noon', '0 12 * * *'],
    ['every day at midnight', '0 0 * * *'],
    ['nightly', '0 21 * * *'],
    ['every weekday at 9am', '0 9 * * 1,2,3,4,5'],
    ['weekdays at 9', '0 9 * * 1,2,3,4,5'],
    ['every weekend', '0 10 * * 0,6'],
    ['every monday', '0 9 * * 1'],
    ['mondays at 9:15', '15 9 * * 1'],
    ['every mon and thu at 8:30', '30 8 * * 1,4'],
    ['every mon, wed, fri', '0 9 * * 1,3,5'],
    ['every tue at 18', '0 18 * * 2'],
  ];
  for (const [text, want] of cases) {
    it(`"${text}" → ${want}`, () => {
      expect(cronOf(text)).toBe(want);
    });
  }
});

describe('parseNaturalSchedule — one-time', () => {
  it('in 30 min', () => {
    expect(onceOf('in 30 min')?.getTime()).toBe(NOW.getTime() + 30 * 60_000);
  });
  it('in 2 hours', () => {
    expect(onceOf('in 2 hours')?.getTime()).toBe(NOW.getTime() + 2 * 3_600_000);
  });
  it('in 1 day', () => {
    expect(onceOf('in 1 day')?.getTime()).toBe(NOW.getTime() + 86_400_000);
  });
  it('tomorrow defaults to 9am', () => {
    const d = onceOf('tomorrow');
    expect(d?.getDate()).toBe(10);
    expect(d?.getHours()).toBe(9);
    expect(d?.getMinutes()).toBe(0);
  });
  it('tomorrow 3pm', () => {
    const d = onceOf('tomorrow 3pm');
    expect(d?.getDate()).toBe(10);
    expect(d?.getHours()).toBe(15);
  });
  it('at 3pm today (still ahead of 14:00)', () => {
    const d = onceOf('at 3pm');
    expect(d?.getDate()).toBe(9);
    expect(d?.getHours()).toBe(15);
  });
  it('at 9am rolls to tomorrow (already past 14:00)', () => {
    const d = onceOf('at 9am');
    expect(d?.getDate()).toBe(10);
    expect(d?.getHours()).toBe(9);
  });
});

describe('parseNaturalSchedule — rejects', () => {
  const bad = [
    '', 'x', 'min', 'hours', 'banana', 'every banana', 'every 0 min',
    'every 99 hours', 'at 25', 'daily at 9:75', 'do the thing every day maybe',
  ];
  for (const text of bad) {
    it(`"${text}" → null`, () => {
      expect(parseNaturalSchedule(text, NOW)).toBeNull();
    });
  }
});

// `*/7` is NOT "every 7 minutes": it fires :00,:07,…,:56 then :00 — a 4-minute
// gap at each hour boundary. Rather than build a schedule that lies about its
// own interval, decline to parse non-divisors (the builder refuses them too).
describe('parseNaturalSchedule — only uniform intervals', () => {
  const nonDivisorMinutes = [7, 8, 9, 11, 13, 14, 16, 25, 40, 45, 50];
  for (const n of nonDivisorMinutes) {
    it(`"every ${n} minutes" → null (${n} does not divide 60)`, () => {
      expect(parseNaturalSchedule(`every ${n} minutes`, NOW)).toBeNull();
    });
  }
  const nonDivisorHours = [5, 7, 9, 10, 11, 13, 18];
  for (const n of nonDivisorHours) {
    it(`"every ${n} hours" → null (${n} does not divide 24)`, () => {
      expect(parseNaturalSchedule(`every ${n} hours`, NOW)).toBeNull();
    });
  }
  it('still accepts the uniform ones', () => {
    expect(cronOf('every 10 minutes')).toBe('*/10 * * * *');
    expect(cronOf('every 20 minutes')).toBe('*/20 * * * *');
    expect(cronOf('every 6 hours')).toBe('0 */6 * * *');
    expect(cronOf('every 12 hours')).toBe('0 */12 * * *');
  });
});
