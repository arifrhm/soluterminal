// Deterministic natural-language → schedule parser for the dialog's typed
// entry ("every weekday at 9am", "in 30 min", "tomorrow 3pm"). A dozen regex
// shapes, no AI round-trip: the result only PRE-FILLS the builder/once picker,
// so a wrong guess is always visible and correctable before Create. Returns
// null for anything it isn't sure about — silence beats a surprising fill.

import {
  HOUR_STEP_CHOICES,
  MINUTE_STEP_CHOICES,
  compileSpec,
  defaultBuilderSpec,
  type BuilderSpec,
} from './cron';

export type ParsedSchedule =
  | { kind: 'cron'; cron: string; spec: BuilderSpec }
  | { kind: 'once'; at: Date };

const WEEKDAYS: Record<string, number> = {
  sunday: 0, sun: 0,
  monday: 1, mon: 1,
  tuesday: 2, tue: 2, tues: 2,
  wednesday: 3, wed: 3,
  thursday: 4, thu: 4, thur: 4, thurs: 4,
  friday: 5, fri: 5,
  saturday: 6, sat: 6,
};

const TIME_RE = String.raw`(?:(\d{1,2})(?::(\d{2}))?\s*(am|pm)?|noon|midnight)`;

// Parse a TIME_RE match tail (h, mm, ampm, plus the raw slice for noon/midnight)
// into {hour, minute}, or null when the numbers don't form a real wall time.
function parseTime(raw: string, h?: string, mm?: string, ampm?: string): { hour: number; minute: number } | null {
  const s = raw.toLowerCase();
  if (s.includes('noon')) return { hour: 12, minute: 0 };
  if (s.includes('midnight')) return { hour: 0, minute: 0 };
  if (h === undefined) return null;
  let hour = parseInt(h, 10);
  const minute = mm ? parseInt(mm, 10) : 0;
  if (minute > 59) return null;
  if (ampm === 'pm' && hour >= 1 && hour <= 11) hour += 12;
  else if (ampm === 'am' && hour === 12) hour = 0;
  // Bare "at 9" defaults to 9am (matching the preset vocabulary), bare "at 18"
  // is 24h. Reject impossible hours.
  if (hour > 23) return null;
  return { hour, minute };
}

function cronResult(spec: BuilderSpec): ParsedSchedule {
  return { kind: 'cron', cron: compileSpec(spec), spec };
}

/** Parse free text into a schedule suggestion. `now` is injectable for tests. */
export function parseNaturalSchedule(text: string, now: Date = new Date()): ParsedSchedule | null {
  const t = text.trim().toLowerCase().replace(/\s+/g, ' ');
  if (t.length < 2) return null;
  const base = defaultBuilderSpec();

  // --- one-time -------------------------------------------------------------
  // "in 30 min", "in 2 hours", "in 1 day"
  let m = /^in (\d{1,3}) ?(min(?:ute)?s?|m|h(?:ou)?rs?|h|days?|d)$/.exec(t);
  if (m) {
    const n = parseInt(m[1], 10);
    if (n < 1) return null;
    const unit = m[2][0]; // m | h | d
    const ms = unit === 'm' ? n * 60_000 : unit === 'h' ? n * 3_600_000 : n * 86_400_000;
    return { kind: 'once', at: new Date(now.getTime() + ms) };
  }

  // "tomorrow", "tomorrow 9am", "tomorrow at 9:30"
  m = new RegExp(`^tomorrow(?: at)?(?: (${TIME_RE}))?$`).exec(t);
  if (m) {
    const time = m[1] ? parseTime(m[1], m[2], m[3], m[4]) : { hour: 9, minute: 0 };
    if (!time) return null;
    const d = new Date(now);
    d.setDate(d.getDate() + 1);
    d.setHours(time.hour, time.minute, 0, 0);
    return { kind: 'once', at: d };
  }

  // "at 3pm", "today at 15:30" — today if still ahead, else tomorrow
  m = new RegExp(`^(?:today )?at (${TIME_RE})$`).exec(t);
  if (m) {
    const time = parseTime(m[1], m[2], m[3], m[4]);
    if (!time) return null;
    const d = new Date(now);
    d.setHours(time.hour, time.minute, 0, 0);
    if (d.getTime() <= now.getTime()) d.setDate(d.getDate() + 1);
    return { kind: 'once', at: d };
  }

  // --- recurring ------------------------------------------------------------
  // "every 30 min", "every minute", "every 2 hours", "hourly".
  // Only divisors of 60/24 are accepted — `*/7` is NOT "every 7 minutes"
  // (it fires :00,:07,…,:56 then :00, a 4-minute gap each hour). Rather than
  // create a schedule that lies about its own interval we decline to parse,
  // and the caller shows "not recognized". Same guard the builder applies.
  m = /^every (\d{1,2}) ?(min(?:ute)?s?|h(?:ou)?rs?)$/.exec(t.replace(/^every (min|hour)/, 'every 1 $1'));
  if (m) {
    const n = parseInt(m[1], 10);
    if (m[2].startsWith('m')) {
      return MINUTE_STEP_CHOICES.includes(n) ? cronResult({ ...base, unit: 'minutes', n }) : null;
    }
    return HOUR_STEP_CHOICES.includes(n) ? cronResult({ ...base, unit: 'hours', n, minute: 0 }) : null;
  }
  if (t === 'hourly') return cronResult({ ...base, unit: 'hours', n: 1, minute: 0 });

  // "every day", "daily at 6pm", "every morning", "nightly"
  m = new RegExp(`^(?:every day|daily|every morning|every night|nightly)(?: at (${TIME_RE}))?$`).exec(t);
  if (m) {
    const isNight = t.startsWith('every night') || t.startsWith('nightly');
    const fallback = isNight ? { hour: 21, minute: 0 } : { hour: 9, minute: 0 };
    const time = m[1] ? parseTime(m[1], m[2], m[3], m[4]) : fallback;
    if (!time) return null;
    return cronResult({ ...base, unit: 'days', hour: time.hour, minute: time.minute });
  }

  // "every weekday at 9am", "weekdays at 9", "every weekend"
  m = new RegExp(`^(?:every weekday|weekdays)(?: at (${TIME_RE}))?$`).exec(t);
  if (m) {
    const time = m[1] ? parseTime(m[1], m[2], m[3], m[4]) : { hour: 9, minute: 0 };
    if (!time) return null;
    return cronResult({ ...base, unit: 'weeks', weekdays: [1, 2, 3, 4, 5], hour: time.hour, minute: time.minute });
  }
  m = new RegExp(`^(?:every weekend|weekends)(?: at (${TIME_RE}))?$`).exec(t);
  if (m) {
    const time = m[1] ? parseTime(m[1], m[2], m[3], m[4]) : { hour: 10, minute: 0 };
    if (!time) return null;
    return cronResult({ ...base, unit: 'weeks', weekdays: [0, 6], hour: time.hour, minute: time.minute });
  }

  // "every monday", "mondays at 9", "every mon and thu at 8:30",
  // "every mon, wed, fri" — day list with and/commas
  m = new RegExp(
    `^(?:every |on )?([a-z]+(?:s)?(?:(?:, ?| and | & |, and )[a-z]+(?:s)?)*)(?: at (${TIME_RE}))?$`,
  ).exec(t);
  if (m) {
    const names = m[1].split(/,| and | & /).map((x) => x.trim().replace(/s$/, '')).filter(Boolean);
    const days: number[] = [];
    for (const name of names) {
      const d = WEEKDAYS[name];
      if (d === undefined) { return null; }
      if (!days.includes(d)) days.push(d);
    }
    if (days.length === 0) return null;
    const time = m[2] ? parseTime(m[2], m[3], m[4], m[5]) : { hour: 9, minute: 0 };
    if (!time) return null;
    return cronResult({ ...base, unit: 'weeks', weekdays: days, hour: time.hour, minute: time.minute });
  }

  return null;
}

/** Short confirmation line shown under the input ("→ every 15 min"). */
export function describeParsed(p: ParsedSchedule): string {
  if (p.kind === 'once') {
    return `once · ${p.at.toLocaleString(undefined, {
      weekday: 'short', month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit',
    })}`;
  }
  return p.cron;
}
