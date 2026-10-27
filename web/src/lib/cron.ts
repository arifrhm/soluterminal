// Shared cron helpers for the Schedules UI: a structured "builder spec" that
// compiles to a 5-field cron expression, a best-effort inverse parser, and a
// human describer for sidebar rows. The daemon's robfig parser stays
// authoritative — everything here is presentation/authoring sugar, and any
// expression we can't represent simply falls back to raw-cron editing
// ("advanced" mode) and a "custom" row label.

export type BuilderUnit = 'minutes' | 'hours' | 'days' | 'weeks' | 'months';

export interface BuilderSpec {
  unit: BuilderUnit;
  /** Every N units — meaningful for minutes/hours only; 1 otherwise. */
  n: number;
  /** Minute-of-hour for the hours unit; minute of the time-of-day otherwise. */
  minute: number;
  /** Hour of the time-of-day (days/weeks/months units). */
  hour: number;
  /** Days-of-week, 0=Sunday … 6=Saturday (weeks unit). */
  weekdays: number[];
  /** Day-of-month 1–31 (months unit). */
  monthday: number;
}

// Divisor-only choices keep the interval truly uniform: cron `*/7` on the
// minute field means "at :00,:07,…,:56, then :00 again" — a 4-minute gap at
// each hour boundary. Offering only divisors of 60/24 avoids shipping a
// schedule that quietly lies about its own interval.
export const MINUTE_STEP_CHOICES = [1, 2, 3, 4, 5, 6, 10, 12, 15, 20, 30];
export const HOUR_STEP_CHOICES = [1, 2, 3, 4, 6, 8, 12];

export function defaultBuilderSpec(): BuilderSpec {
  return { unit: 'days', n: 1, minute: 0, hour: 9, weekdays: [1, 2, 3, 4, 5], monthday: 1 };
}

/** Compile a builder spec to a 5-field cron expression. Weekdays must be
 * non-empty for the weeks unit — callers validate before compiling. */
export function compileSpec(s: BuilderSpec): string {
  switch (s.unit) {
    case 'minutes':
      return s.n === 1 ? '* * * * *' : `*/${s.n} * * * *`;
    case 'hours':
      return s.n === 1 ? `${s.minute} * * * *` : `${s.minute} */${s.n} * * *`;
    case 'days':
      return `${s.minute} ${s.hour} * * *`;
    case 'weeks': {
      const days = [...new Set(s.weekdays)].sort((a, b) => a - b).join(',');
      return `${s.minute} ${s.hour} * * ${days}`;
    }
    case 'months':
      return `${s.minute} ${s.hour} ${s.monthday} * *`;
  }
}

function toInt(field: string): number | null {
  if (!/^\d{1,2}$/.test(field)) return null;
  return parseInt(field, 10);
}

// Expand a day-of-week field of numbers, commas and A-B ranges into a sorted
// unique 0-6 list. Names (MON) and step syntax are left to advanced mode.
function parseDowList(field: string): number[] | null {
  const out = new Set<number>();
  for (const part of field.split(',')) {
    const m = /^(\d)-(\d)$/.exec(part);
    if (m) {
      let a = parseInt(m[1], 10);
      let b = parseInt(m[2], 10);
      if (a > 7 || b > 7 || a > b) return null;
      for (let d = a; d <= b; d++) out.add(d === 7 ? 0 : d);
      continue;
    }
    const n = toInt(part);
    if (n === null || n > 7) return null;
    out.add(n === 7 ? 0 : n);
  }
  return out.size ? [...out].sort((a, b) => a - b) : null;
}

/** Best-effort inverse of compileSpec: recognize the builder-representable
 * shapes of a 5-field cron expression. Returns null for anything else
 * (steps on dom, month restrictions, names, CRON_TZ, …). */
export function parseCron(expr: string): BuilderSpec | null {
  const parts = expr.trim().split(/\s+/);
  if (parts.length !== 5) return null;
  const [min, hour, dom, month, dow] = parts;
  if (month !== '*') return null;
  const base = defaultBuilderSpec();

  if (dom === '*' && dow === '*') {
    if (hour === '*') {
      if (min === '*') return { ...base, unit: 'minutes', n: 1 };
      const stepM = /^\*\/(\d{1,2})$/.exec(min);
      if (stepM) {
        const n = parseInt(stepM[1], 10);
        return n >= 1 && n <= 59 ? { ...base, unit: 'minutes', n } : null;
      }
      const m = toInt(min);
      return m !== null && m <= 59 ? { ...base, unit: 'hours', n: 1, minute: m } : null;
    }
    const stepH = /^\*\/(\d{1,2})$/.exec(hour);
    const m = toInt(min);
    if (m === null || m > 59) return null;
    if (stepH) {
      const n = parseInt(stepH[1], 10);
      return n >= 1 && n <= 23 ? { ...base, unit: 'hours', n, minute: m } : null;
    }
    const h = toInt(hour);
    return h !== null && h <= 23 ? { ...base, unit: 'days', minute: m, hour: h } : null;
  }

  const m = toInt(min);
  const h = toInt(hour);
  if (m === null || m > 59 || h === null || h > 23) return null;

  if (dom === '*' && dow !== '*') {
    const days = parseDowList(dow);
    if (!days) return null;
    return { ...base, unit: 'weeks', minute: m, hour: h, weekdays: days };
  }
  if (dow === '*' && dom !== '*') {
    const d = toInt(dom);
    if (d === null || d < 1 || d > 31) return null;
    return { ...base, unit: 'months', minute: m, hour: h, monthday: d };
  }
  return null; // both dom and dow restricted — cron ORs them; too subtle for the builder
}

const WEEKDAY_SHORT = ['Sun', 'Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat'];
const WEEKDAY_PLURAL = [
  'sundays', 'mondays', 'tuesdays', 'wednesdays', 'thursdays', 'fridays', 'saturdays',
];

// "9am" / "9:30am" / "noon" / "midnight" — matches the tone of the existing
// sidebar labels ("daily 9am", "weekdays 9am").
export function fmtTime(hour: number, minute: number): string {
  if (minute === 0) {
    if (hour === 0) return 'midnight';
    if (hour === 12) return 'noon';
    return hour < 12 ? `${hour}am` : `${hour - 12}pm`;
  }
  const mm = String(minute).padStart(2, '0');
  const h12 = hour % 12 === 0 ? 12 : hour % 12;
  return `${h12}:${mm}${hour < 12 ? 'am' : 'pm'}`;
}

function sameSet(days: number[], want: number[]): boolean {
  return days.length === want.length && want.every((d) => days.includes(d));
}

// Evenly-spaced minute lists like "2,12,22,32,42,52" read as raw noise in the
// sidebar; recognize constant-step lists and describe them as an interval
// with their offset ("every 10 min (:02)").
function describeMinuteList(min: string, rest: string[]): string | null {
  if (rest.some((f) => f !== '*')) return null;
  const nums = min.split(',').map((f) => toInt(f));
  if (nums.length < 2 || nums.some((n) => n === null || n > 59)) return null;
  const list = nums as number[];
  const step = list[1] - list[0];
  if (step <= 0) return null;
  for (let i = 1; i < list.length; i++) {
    if (list[i] - list[i - 1] !== step) return null;
  }
  // Must tile the whole hour, else it's a bespoke pattern — leave it raw.
  if (list[0] >= step || list[list.length - 1] + step !== 60 + list[0]) return null;
  const offset = list[0] === 0 ? '' : ` (:${String(list[0]).padStart(2, '0')})`;
  return `every ${step} min${offset}`;
}

/** Human label for a cron expression, or null when it has no compact honest
 * description (caller shows "custom" and keeps the raw string in a tooltip). */
export function describeCron(expr: string): string | null {
  const parts = expr.trim().split(/\s+/);
  if (parts.length !== 5) return null;
  const listLabel = describeMinuteList(parts[0], parts.slice(1));
  if (listLabel) return listLabel;

  const spec = parseCron(expr);
  if (!spec) return null;
  switch (spec.unit) {
    case 'minutes':
      return spec.n === 1 ? 'every min' : `every ${spec.n} min`;
    case 'hours': {
      const at = spec.minute === 0 ? '' : ` at :${String(spec.minute).padStart(2, '0')}`;
      return spec.n === 1 ? `hourly${at}` : `every ${spec.n}h${at}`;
    }
    case 'days':
      return `daily ${fmtTime(spec.hour, spec.minute)}`;
    case 'weeks': {
      const t = fmtTime(spec.hour, spec.minute);
      const d = spec.weekdays;
      if (sameSet(d, [0, 1, 2, 3, 4, 5, 6])) return `daily ${t}`;
      if (sameSet(d, [1, 2, 3, 4, 5])) return `weekdays ${t}`;
      if (sameSet(d, [0, 6])) return `weekends ${t}`;
      if (d.length === 1) return `${WEEKDAY_PLURAL[d[0]]} ${t}`;
      return `${d.map((x) => WEEKDAY_SHORT[x]).join('/')} ${t}`;
    }
    case 'months': {
      const suffix =
        spec.monthday % 10 === 1 && spec.monthday !== 11 ? 'st'
        : spec.monthday % 10 === 2 && spec.monthday !== 12 ? 'nd'
        : spec.monthday % 10 === 3 && spec.monthday !== 13 ? 'rd'
        : 'th';
      return `monthly (${spec.monthday}${suffix}) ${fmtTime(spec.hour, spec.minute)}`;
    }
  }
}

/** Rewrite an expression into the one form compileSpec would emit, so two
 * spellings of the same schedule compare equal. `0 9 * * 1-5` and
 * `0 9 * * 1,2,3,4,5` are the same weekday-9am job; without this the preset
 * dropdown fails to recognize a builder- or NL-produced equivalent and
 * needlessly falls back to "Custom…". Unparseable input is returned trimmed. */
export function canonicalizeCron(expr: string): string {
  const spec = parseCron(expr);
  return spec ? compileSpec(spec) : expr.trim();
}

/** Whether two cron expressions describe the same schedule. */
export function cronsEquivalent(a: string, b: string): boolean {
  return canonicalizeCron(a) === canonicalizeCron(b);
}

/** Client-side sanity check before round-tripping to the server parser.
 * Accepts the optional CRON_TZ=… prefix robfig supports — without this the
 * Create button stays disabled on expressions the daemon would accept. */
export function looksLikeCron(expr: string): boolean {
  const parts = expr.trim().split(/\s+/);
  if (parts.length === 6 && parts[0].startsWith('CRON_TZ=')) return true;
  return parts.length === 5;
}

/** formatNextFire — a schedule's server-computed nextFire as a short "coming up"
 * label ("imminent" / "in 45s" / "in 12m" / "at 14:30" / "in 3h" / "in 2d").
 * Shared by the Schedules sidebar rows and the pane header's next-run chip so
 * the two speak the same vocabulary. "—" for absent/unparseable (a disabled
 * schedule has no nextFire).
 *
 * NOTE: nextFire is the raw cron occurrence — the daemon's NextFire does NOT
 * subtract a snooze (snoozeUntil suppresses exactly that occurrence). Callers
 * must check snoozeUntil themselves before presenting this as a promise. */
export function formatNextFire(iso: string | undefined): string {
  if (!iso) return '—';
  const t = new Date(iso).getTime();
  if (isNaN(t)) return '—';
  const now = Date.now();
  const diff = t - now;
  if (diff <= 0) return 'imminent';
  const sec = Math.round(diff / 1000);
  if (sec < 60) return `in ${sec}s`;
  if (sec < 3600) return `in ${Math.round(sec / 60)}m`;
  if (sec < 86400) {
    const d = new Date(t);
    const sameDay = new Date(now).toDateString() === d.toDateString();
    if (sameDay) return `at ${String(d.getHours()).padStart(2, '0')}:${String(d.getMinutes()).padStart(2, '0')}`;
    return `in ${Math.round(sec / 3600)}h`;
  }
  return `in ${Math.round(sec / 86400)}d`;
}
