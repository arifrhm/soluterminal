import { describe, expect, it } from 'vitest';
import {
  canonicalizeCron,
  compileSpec,
  cronsEquivalent,
  defaultBuilderSpec,
  describeCron,
  fmtTime,
  looksLikeCron,
  parseCron,
  type BuilderSpec,
} from './cron';

function spec(over: Partial<BuilderSpec>): BuilderSpec {
  return { ...defaultBuilderSpec(), ...over };
}

describe('compileSpec', () => {
  const cases: Array<[Partial<BuilderSpec>, string]> = [
    [{ unit: 'minutes', n: 1 }, '* * * * *'],
    [{ unit: 'minutes', n: 15 }, '*/15 * * * *'],
    [{ unit: 'hours', n: 1, minute: 0 }, '0 * * * *'],
    [{ unit: 'hours', n: 1, minute: 30 }, '30 * * * *'],
    [{ unit: 'hours', n: 4, minute: 5 }, '5 */4 * * *'],
    [{ unit: 'days', hour: 9, minute: 0 }, '0 9 * * *'],
    [{ unit: 'days', hour: 18, minute: 45 }, '45 18 * * *'],
    [{ unit: 'weeks', hour: 9, minute: 0, weekdays: [1, 2, 3, 4, 5] }, '0 9 * * 1,2,3,4,5'],
    [{ unit: 'weeks', hour: 9, minute: 0, weekdays: [5, 1, 3] }, '0 9 * * 1,3,5'],
    [{ unit: 'months', hour: 9, minute: 0, monthday: 1 }, '0 9 1 * *'],
  ];
  for (const [s, want] of cases) {
    it(`${JSON.stringify(s)} → ${want}`, () => {
      expect(compileSpec(spec(s))).toBe(want);
    });
  }
});

describe('parseCron round-trips compileSpec', () => {
  const specs: Array<Partial<BuilderSpec>> = [
    { unit: 'minutes', n: 1 },
    { unit: 'minutes', n: 30 },
    { unit: 'hours', n: 1, minute: 0 },
    { unit: 'hours', n: 2, minute: 15 },
    { unit: 'days', hour: 0, minute: 0 },
    { unit: 'days', hour: 23, minute: 59 },
    { unit: 'weeks', hour: 9, minute: 30, weekdays: [0, 6] },
    { unit: 'months', hour: 12, minute: 0, monthday: 31 },
  ];
  for (const s of specs) {
    it(JSON.stringify(s), () => {
      const full = spec(s);
      const parsed = parseCron(compileSpec(full));
      expect(parsed).not.toBeNull();
      expect(compileSpec(parsed!)).toBe(compileSpec(full));
    });
  }
});

describe('parseCron shapes', () => {
  it('expands dow ranges', () => {
    expect(parseCron('0 9 * * 1-5')).toMatchObject({ unit: 'weeks', weekdays: [1, 2, 3, 4, 5] });
    expect(parseCron('0 9 * * 5-7')).toMatchObject({ unit: 'weeks', weekdays: [0, 5, 6] });
  });
  it('maps dow 7 to Sunday', () => {
    expect(parseCron('0 9 * * 7')).toMatchObject({ unit: 'weeks', weekdays: [0] });
  });
  it('rejects non-builder shapes', () => {
    expect(parseCron('0 9 */2 * *')).toBeNull(); // dom step
    expect(parseCron('0 9 * 6 *')).toBeNull(); // month restriction
    expect(parseCron('0 9 1 * 1')).toBeNull(); // dom AND dow
    expect(parseCron('0 9 * * MON')).toBeNull(); // names → advanced
    expect(parseCron('*/5 9 * * *')).toBeNull(); // step minutes within an hour window
    expect(parseCron('0 9 * *')).toBeNull(); // 4 fields
    expect(parseCron('60 9 * * *')).toBeNull(); // minute out of range
    expect(parseCron('0 24 * * *')).toBeNull(); // hour out of range
    expect(parseCron('0 9 0 * *')).toBeNull(); // monthday 0
  });
});

describe('describeCron', () => {
  const cases: Array<[string, string]> = [
    ['*/5 * * * *', 'every 5 min'],
    ['* * * * *', 'every min'],
    ['0 * * * *', 'hourly'],
    ['30 * * * *', 'hourly at :30'],
    ['0 */2 * * *', 'every 2h'],
    ['5 */4 * * *', 'every 4h at :05'],
    ['0 9 * * *', 'daily 9am'],
    ['0 12 * * *', 'daily noon'],
    ['0 0 * * *', 'daily midnight'],
    ['45 18 * * *', 'daily 6:45pm'],
    ['0 9 * * 1-5', 'weekdays 9am'],
    ['0 9 * * 1', 'mondays 9am'],
    ['0 10 * * 0,6', 'weekends 10am'],
    ['0 9 * * 1,3,5', 'Mon/Wed/Fri 9am'],
    ['0 9 * * 0-6', 'daily 9am'],
    ['0 9 1 * *', 'monthly (1st) 9am'],
    ['30 14 22 * *', 'monthly (22nd) 2:30pm'],
    ['2,12,22,32,42,52 * * * *', 'every 10 min (:02)'],
    ['0,30 * * * *', 'every 30 min'],
  ];
  for (const [expr, want] of cases) {
    it(`${expr} → ${want}`, () => {
      expect(describeCron(expr)).toBe(want);
    });
  }
  it('returns null for patterns without an honest compact label', () => {
    expect(describeCron('0 9 */2 * *')).toBeNull();
    expect(describeCron('1,2,50 * * * *')).toBeNull(); // uneven list
    expect(describeCron('5,15 * * * *')).toBeNull(); // does not tile the hour
    expect(describeCron('not a cron')).toBeNull();
  });
});

describe('fmtTime', () => {
  it('formats edges', () => {
    expect(fmtTime(0, 0)).toBe('midnight');
    expect(fmtTime(12, 0)).toBe('noon');
    expect(fmtTime(0, 30)).toBe('12:30am');
    expect(fmtTime(12, 30)).toBe('12:30pm');
    expect(fmtTime(23, 5)).toBe('11:05pm');
  });
});

describe('canonicalizeCron / cronsEquivalent', () => {
  it('treats range and list spellings of the same weekdays as equal', () => {
    expect(cronsEquivalent('0 9 * * 1-5', '0 9 * * 1,2,3,4,5')).toBe(true);
    expect(cronsEquivalent('0 9 * * 5-7', '0 9 * * 0,5,6')).toBe(true);
    expect(cronsEquivalent('0 9 * * 7', '0 9 * * 0')).toBe(true);
  });
  it('does not equate genuinely different schedules', () => {
    expect(cronsEquivalent('0 9 * * 1-5', '0 9 * * 1-4')).toBe(false);
    expect(cronsEquivalent('0 9 * * *', '0 10 * * *')).toBe(false);
  });
  it('leaves unparseable expressions alone but still self-equal', () => {
    expect(canonicalizeCron(' weird ')).toBe('weird');
    expect(cronsEquivalent('0 9 */2 * *', '0 9 */2 * *')).toBe(true);
  });
});

describe('looksLikeCron', () => {
  it('accepts 5 fields and CRON_TZ prefix', () => {
    expect(looksLikeCron('0 9 * * 1-5')).toBe(true);
    expect(looksLikeCron('CRON_TZ=Asia/Tokyo 0 9 * * *')).toBe(true);
    expect(looksLikeCron('0 9 * *')).toBe(false);
    expect(looksLikeCron('a b c d e f')).toBe(false);
  });
});
