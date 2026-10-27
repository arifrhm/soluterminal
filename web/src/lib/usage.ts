// Shared helpers for the Usage Phase 2 UI (the full-screen UsagePage).
// The cloud contract (docs/USAGE-PHASE2-CONTRACT.md) returns `daily` ascending
// with ONLY days that have >=1 turn; the client is responsible for zero-filling
// gaps so charts/sparklines have an even, honest x-axis.

import type { CCUsageSummary, CCUsageTeamSummary, UsageProvider } from '../api';

// A zero-filled daily point. `by_model` (Phase 2.2) is carried through so the
// daily chart can render stacked-by-model bars; it's undefined for zero-filled
// gap days (no activity → no segments) and for older responses that omit it (the
// chart then falls back to a single-color total bar).
export type DailyPoint = {
  date: string;
  cost_usd: number;
  by_model?: Record<string, number>;
};

// "YYYY-MM-DD" for a calendar Y/M/D. Date.UTC is used ONLY as a calendar
// calculator (month/day overflow normalizes, e.g. day 0 → last day of the prior
// month); UTC has no DST, so stepping days here never skips/repeats a date.
function calendarKey(y: number, m0: number, d: number): string {
  return new Date(Date.UTC(y, m0, d)).toISOString().slice(0, 10);
}

// Calendar parts (month 0-based) of `now` in the day calendar the response
// used. No `tz` (older cloud) → UTC, matching its date_trunc('day', event_ts).
// With the echoed `tz` → that zone's wall-clock date (normally the browser's
// own zone; if the cloud fell back to "UTC" the spine follows it). A zone Intl
// can't resolve falls back to the browser's local Y/M/D.
function calendarParts(now: Date, tz?: string | null): { y: number; m0: number; d: number } {
  if (!tz) return { y: now.getUTCFullYear(), m0: now.getUTCMonth(), d: now.getUTCDate() };
  try {
    const parts = new Intl.DateTimeFormat('en-US', {
      timeZone: tz,
      year: 'numeric',
      month: 'numeric',
      day: 'numeric',
    }).formatToParts(now);
    const get = (type: string) => Number(parts.find((p) => p.type === type)?.value);
    const y = get('year');
    const m = get('month');
    const d = get('day');
    if (Number.isFinite(y) && Number.isFinite(m) && Number.isFinite(d)) return { y, m0: m - 1, d };
  } catch {
    // fall through to the browser's local calendar
  }
  return { y: now.getFullYear(), m0: now.getMonth(), d: now.getDate() };
}

// Today's "YYYY-MM-DD" in the response's day calendar (see calendarParts).
export function usageDayKey(now: Date = new Date(), tz?: string | null): string {
  const { y, m0, d } = calendarParts(now, tz);
  return calendarKey(y, m0, d);
}

// Zero-fill the daily series across the full window so a chart/sparkline has an
// even x-axis. For finite windows (7d/30d/90d) we lay down a contiguous spine
// of days ending today, then UNION in any server-returned date — including
// the partial boundary day the server's `since = now - N days` can include that
// a strict N-day spine would otherwise drop (an off-by-one that silently loses
// a real day's cost from the chart and makes it disagree with hero.cost_usd).
//
// Days are UTC calendar days unless the response echoed `tz` (Local-day
// grouping): then the spine and "today" are calendar days in that zone.
//
// For window "all" (windowDays == 0) there's no fixed span to fill, so we just
// return the server's sparse days sorted ascending.
export function zeroFilledDaily(
  daily: CCUsageSummary['daily'],
  windowDays: number,
  tz?: string | null,
  now: Date = new Date(),
): DailyPoint[] {
  const byDate = new Map<string, number>();
  // Phase 2.2: keep the per-model cost map alongside the total so the chart can
  // stack. Guarded — absent on older responses, in which case `by_model` stays
  // undefined and the chart falls back to a single total bar.
  const byModelByDate = new Map<string, Record<string, number>>();
  for (const d of daily) {
    byDate.set(d.date, d.cost_usd);
    if (d.by_model) byModelByDate.set(d.date, d.by_model);
  }

  if (windowDays <= 0) {
    // "all" — sort the sparse rows ascending; no fill.
    return [...byDate.entries()]
      .map(([date, cost_usd]) => ({ date, cost_usd, by_model: byModelByDate.get(date) }))
      .sort((a, b) => a.date.localeCompare(b.date));
  }

  // Contiguous day spine ending today (UTC, or the echoed zone's calendar).
  const keys = new Set<string>();
  const today = calendarParts(now, tz);
  for (let i = windowDays - 1; i >= 0; i--) {
    keys.add(calendarKey(today.y, today.m0, today.d - i));
  }
  // Never drop a day the server actually returned (covers the partial boundary
  // day, and clock-skew dates beyond "today").
  for (const date of byDate.keys()) keys.add(date);

  return [...keys]
    .sort((a, b) => a.localeCompare(b))
    .map((date) => ({ date, cost_usd: byDate.get(date) ?? 0, by_model: byModelByDate.get(date) }));
}

// ---------------------------------------------------------------------------
// Phase 1/2 derivations (docs/USAGE-PHASE1-2-CONTRACT.md §2a). All PURE.
// ---------------------------------------------------------------------------

// Direction of a period-over-period change. 'flat' when the magnitude of the
// relative change is below EPS (or both values are ~0) so a tiny float wobble
// doesn't render as a misleading ▲/▼.
export type DeltaDir = 'up' | 'down' | 'flat';
export type Delta = { pct: number | null; dir: DeltaDir };

// Relative change of `cur` vs `prev`, as a fraction (0.25 == +25%).
//   - prev <= 0 → pct = null ("new" — there's no honest base to divide by; the
//     UI shows a "new" pill rather than a meaningless +∞%). dir is then 'up'
//     when cur > 0 (activity appeared) else 'flat'.
//   - otherwise pct = (cur - prev) / prev; dir from the signed change with a
//     small epsilon band so near-equal values read 'flat'.
const DELTA_EPS = 1e-9;
export function deltaPct(cur: number, prev: number): Delta {
  const c = Number.isFinite(cur) ? cur : 0;
  const p = Number.isFinite(prev) ? prev : 0;
  if (p <= 0) {
    return { pct: null, dir: c > 0 ? 'up' : 'flat' };
  }
  const pct = (c - p) / p;
  let dir: DeltaDir = 'flat';
  if (pct > DELTA_EPS) dir = 'up';
  else if (pct < -DELTA_EPS) dir = 'down';
  return { pct, dir };
}

// Per-metric delta computed from a current hero vs the prior-window hero. Each
// entry carries the raw cur/prev plus the derived pct/dir so a card can show
// both the headline number and its ±%. null-safe: when `previous` is absent or
// null, prev is treated as 0 → pct=null ("new"), which is the honest read for a
// freshly-tracked account.
export type HeroDelta = { cur: number; prev: number; pct: number | null; dir: DeltaDir };
export type HeroDeltas = {
  cost_usd: HeroDelta;
  turns: HeroDelta;
  sessions: HeroDelta;
  active_days: HeroDelta;
  fresh_tokens: HeroDelta;
  cached_tokens: HeroDelta;
  cache_hit_rate: HeroDelta;
};

// The hero metric keys we compute deltas for. cache_hit_rate is optional on the
// hero shape (absent on older/empty responses) → default 0.
const HERO_METRIC_KEYS = [
  'cost_usd',
  'turns',
  'sessions',
  'active_days',
  'fresh_tokens',
  'cached_tokens',
  'cache_hit_rate',
] as const;

type HeroMetricKey = (typeof HERO_METRIC_KEYS)[number];

export function heroDeltas(
  hero: CCUsageSummary['hero'],
  previous: CCUsageSummary['previous'],
): HeroDeltas {
  const prevHero = previous?.hero;
  const out = {} as HeroDeltas;
  for (const key of HERO_METRIC_KEYS) {
    const cur = num((hero as Record<HeroMetricKey, number | undefined>)[key]);
    const prev = num(
      prevHero ? (prevHero as Record<HeroMetricKey, number | undefined>)[key] : 0,
    );
    const { pct, dir } = deltaPct(cur, prev);
    out[key] = { cur, prev, pct, dir };
  }
  return out;
}

// ---- Efficiency helpers -----------------------------------------------------
// All guard divide-by-zero by returning 0 (a member with no turns has no
// per-turn cost; an empty token bundle has no cache hit rate).

// Coerce maybe-undefined/NaN to a finite number (0).
function num(v: number | undefined | null): number {
  return typeof v === 'number' && Number.isFinite(v) ? v : 0;
}

// Input-side cache hit rate, mirroring the server denom (cc_summary.go):
//   cache_read / (cache_read + input + cache_create_eff)
// Output is excluded (it isn't cacheable input). 0 when the denom is 0.
// On a by_member row `cache_create_tokens` is ALREADY the effective value, so
// the caller passes it straight through as `cache_create`.
export type TokenBundle = {
  cache_read?: number;
  input?: number;
  cache_create?: number;
};
export function cacheHitRate(tokens: TokenBundle): number {
  const read = num(tokens.cache_read);
  const denom = read + num(tokens.input) + num(tokens.cache_create);
  return denom > 0 ? read / denom : 0;
}

// Display rows for the "By account" card. Returns null when there's nothing to
// show: no by_account at all, or ONLY the unattributed ("") bucket — the state
// before any daemon has attributed (e.g. right after install, or older cloud).
// The "" bucket is labeled "Unattributed" and flagged dim so it reads as the
// residual rather than a named account. The server already sorts cost-desc.
export type AccountRow = {
  account: string;
  // (Codex provider) "claude" | "codex"; by_account groups by provider+account,
  // so the same login name can appear once per provider.
  provider: ModelProvider;
  label: string;
  dim: boolean;
  cost_usd: number;
  turns: number;
  cost_share: number;
};
export function accountRows(byAccount: CCUsageSummary['by_account']): AccountRow[] | null {
  if (!byAccount || byAccount.length === 0) return null;
  // Hide when no named account exists (everything unattributed — no split worth showing).
  if (!byAccount.some((a) => a.account !== '')) return null;
  return byAccount.map((a) => ({
    account: a.account,
    provider: normalizeProvider(a.provider),
    label: a.account === '' ? 'Unattributed' : a.account,
    dim: a.account === '',
    cost_usd: a.cost_usd,
    turns: a.turns,
    cost_share: a.cost_share,
  }));
}

export function costPerTurn(cost: number, turns: number): number {
  const t = num(turns);
  return t > 0 ? num(cost) / t : 0;
}

export function outputTokensPerTurn(output: number, turns: number): number {
  const t = num(turns);
  return t > 0 ? num(output) / t : 0;
}

export function tokensPerTurn(totalTokens: number, turns: number): number {
  const t = num(turns);
  return t > 0 ? num(totalTokens) / t : 0;
}

// (Phase 3b) Turns per session — average conversation depth. A member with
// many turns spread over few sessions runs deep, multi-turn sessions; the same
// turns over many sessions means short, one-shot prompts. Higher-is-better as a
// raw engagement-depth signal. Guards sessions == 0 → 0 (a member with no
// sessions has no per-session depth).
export function turnsPerSession(row: MemberRow): number {
  const s = num(row.sessions);
  return s > 0 ? num(row.turns) / s : 0;
}

// ---- Team baselines ---------------------------------------------------------
// A by_member row (CCUsageTeamSummary['by_member'][number]). Aliased so the
// metric accessors below read cleanly and integration can reuse the name.
export type MemberRow = CCUsageTeamSummary['by_member'][number];

// A metric accessor over a row, e.g. (r) => r.cost_usd.
export type MetricFn<T> = (row: T) => number;

// Arithmetic mean of metric values over rows. 0 over an empty set.
export function mean<T>(rows: readonly T[], metric: MetricFn<T>): number {
  if (rows.length === 0) return 0;
  let sum = 0;
  for (const r of rows) sum += num(metric(r));
  return sum / rows.length;
}

// Median of metric values over rows. 0 over an empty set; the mean of the two
// middle values for even length; sorts a COPY (never mutates the input).
export function median<T>(rows: readonly T[], metric: MetricFn<T>): number {
  if (rows.length === 0) return 0;
  const vals = rows.map((r) => num(metric(r))).sort((a, b) => a - b);
  const mid = vals.length >> 1;
  return vals.length % 2 === 1 ? vals[mid] : (vals[mid - 1] + vals[mid]) / 2;
}

// (Phase 3a) The correct team baseline for ONE column over the member rows.
//   - 'mean' (default / additive columns: cost, turns, sessions, active_days,
//     output_tokens) → the arithmetic mean of the per-member metric. For an
//     additive metric this equals the pooled total / N, so the mean is right.
//   - 'pooled' (ratio/derived columns: cost_per_turn, output_per_turn,
//     cache_hit_rate) → the team RATE recomputed from the underlying totals:
//     (Σ pooled.num) / (Σ pooled.den) across members. This fixes the review bug
//     where mean-of-per-member-ratios over-weighted a 2-turn member against a
//     2000-turn member. Falls back to the mean if a 'pooled' column is somehow
//     missing its `pooled` accessors (defensive — shouldn't happen).
// 0 over an empty set (mirrors mean/median).
export function pooledBaseline(rows: readonly MemberRow[], column: ColumnSpec): number {
  if (rows.length === 0) return 0;
  if (column.baselineKind === 'pooled' && column.pooled) {
    let n = 0;
    let d = 0;
    for (const r of rows) {
      n += num(column.pooled.num(r));
      d += num(column.pooled.den(r));
    }
    return d > 0 ? n / d : 0;
  }
  return mean(rows, column.metric);
}

// ---- Per-member period-over-period delta (Phase 3a) -------------------------
// "Who's ramping" — each member's change vs their own prior equal-length window,
// carried on by_member[].previous = { cost_usd, turns } (owner-gated, optional).
//
// The lens chooses the basis so the Δ always matches the PRIMARY column it is
// drawn in: the COST lens compares cost, the OUTPUT lens compares TURNS, and the
// EFFICIENCY lens compares COST PER TURN (its primary column, lower-is-better —
// the table colours it with goodWhen 'down'). Returns null when
// `previous` is absent/null (older response / non-owner / window=="all" / a
// freshly-tracked member) so the table shows NO badge rather than a bogus delta.
export type MemberDelta = { pct: number | null; dir: DeltaDir };
export function memberDelta(row: MemberRow, lens: Lens): MemberDelta | null {
  const prev = row.previous;
  if (!prev) return null;
  if (lens === 'cost') {
    return deltaPct(num(row.cost_usd), num(prev.cost_usd));
  }
  if (lens === 'efficiency') {
    // Prior cost/turn of 0 (no prior turns) → deltaPct reports "new".
    return deltaPct(
      costPerTurn(num(row.cost_usd), num(row.turns)),
      costPerTurn(num(prev.cost_usd), num(prev.turns)),
    );
  }
  // output → turns Δ (the engagement signal).
  return deltaPct(num(row.turns), num(prev.turns));
}

// ---- Per-member model mix (Phase 3a) ----------------------------------------
// Normalize a member's by_model rows (cost DESC, owner-gated, optional) into a
// compact stacked-segment spec: one segment per model with its share-of-member
// (re-derived from the segment costs so segments always sum to ~1, even if the
// server's per-model cost_share drifts or is absent). Returns [] when by_model is
// absent/empty (older response / non-owner) so the table cell renders nothing.
// `limit` caps the visible segments; any overflow is merged into a trailing
// "other" segment so the bar stays compact and still sums to 1.
export type ModelMixSeg = { model: string; tier: string; share: number };
export function memberModelMix(row: MemberRow, limit = 4): ModelMixSeg[] {
  const bm = row.by_model;
  if (!bm || bm.length === 0) return [];
  let total = 0;
  for (const m of bm) total += Math.max(0, num(m.cost_usd));
  if (total <= 0) return [];
  // Rows arrive cost-DESC; keep that order. Take the top `limit`, fold the rest
  // into one "other" bucket so a many-model member still reads as a small bar.
  const head = bm.slice(0, Math.max(1, limit));
  const tail = bm.slice(Math.max(1, limit));
  const segs: ModelMixSeg[] = head.map((m) => ({
    model: m.model,
    tier: m.tier || '',
    share: Math.max(0, num(m.cost_usd)) / total,
  }));
  if (tail.length > 0) {
    let otherCost = 0;
    for (const m of tail) otherCost += Math.max(0, num(m.cost_usd));
    if (otherCost > 0) {
      segs.push({ model: 'other', tier: 'other', share: otherCost / total });
    }
  }
  return segs;
}

// ---- Lens model -------------------------------------------------------------
// A column spec drives BOTH the comparison table and the leaderboards so those
// components stay dumb. `metric` extracts the sortable/displayable number from a
// row; `format` tells the cell renderer how to print it; `primary` marks the
// lens's headline column (the default sort). `lowerIsBetter` flags metrics where
// a SMALLER value ranks better (cost-per-turn) so the table can colour/rank
// accordingly — Output/Cost metrics are higher-is-better (the default).
export type ColumnFormat = 'usd' | 'int' | 'pct' | 'tok';
// (Phase 3a) How a column's team baseline is derived. 'mean' = the arithmetic
// mean of the per-member metric values (correct for ADDITIVE metrics: cost,
// turns, sessions, active_days, output_tokens — the sum/N is the same whether
// you average the rows or pool the totals). 'pooled' = the team RATE recomputed
// from the underlying totals (correct for RATIO/derived metrics: cost_per_turn,
// cache_hit_rate, output_per_turn — a mean of per-member ratios would weight a
// 2-turn member equal to a 2000-turn member, which is wrong). A column with
// baselineKind 'pooled' MUST also declare `pooled` (see PooledRate) so the
// baseline can be rebuilt from Σnumerator / Σdenominator across members.
export type BaselineKind = 'mean' | 'pooled';
// Numerator/denominator accessors for a pooled team rate. The baseline for a
// pooled column is (Σ num(row)) / (Σ den(row)) over the members — i.e. the team
// rate as if every member's raw counts were merged into one. 0 when Σden <= 0.
export type PooledRate = { num: MetricFn<MemberRow>; den: MetricFn<MemberRow> };
export type ColumnSpec = {
  key: string;
  label: string;
  format: ColumnFormat;
  metric: MetricFn<MemberRow>;
  primary?: boolean;
  lowerIsBetter?: boolean;
  // (Phase 3a) Defaults to 'mean' when absent. 'pooled' columns carry `pooled`.
  baselineKind?: BaselineKind;
  pooled?: PooledRate;
};

export type Lens = 'output' | 'efficiency' | 'cost';

// The three lenses. Each returns an ordered list of columns; exactly one is
// marked `primary` (the lens's default sort). All metric accessors are robust
// to absent enriched fields (older responses) → fall back to 0 / cost-only,
// so a column always yields a finite number.
export function lensColumns(lens: Lens): ColumnSpec[] {
  switch (lens) {
    case 'output':
      // Raw productivity: how much work the member drove. `turns` stays the
      // primary headline; `turns/session` (Phase 3b) sits beside it as the
      // engagement-DEPTH metric (deep multi-turn sessions vs one-shots). Kept to
      // 5 columns so the table stays readable.
      return [
        {
          key: 'turns',
          label: 'Turns',
          format: 'int',
          metric: (r) => num(r.turns),
          primary: true,
        },
        {
          key: 'turns_per_session',
          label: 'Turns/session',
          format: 'int',
          metric: turnsPerSession,
          // Team turns/session = Σturns / Σsessions across members (a pooled
          // rate, not a mean of per-member ratios — a 2-session member shouldn't
          // weigh equal to a 200-session one).
          baselineKind: 'pooled',
          pooled: { num: (r) => num(r.turns), den: (r) => num(r.sessions) },
        },
        { key: 'sessions', label: 'Sessions', format: 'int', metric: (r) => num(r.sessions) },
        {
          key: 'active_days',
          label: 'Active days',
          format: 'int',
          metric: (r) => num(r.active_days),
        },
        {
          key: 'output_tokens',
          label: 'Output tokens',
          format: 'tok',
          metric: (r) => num(r.output_tokens),
        },
      ];
    case 'efficiency':
      // Quality of spend. cost_per_turn is LOWER-is-better and is the primary
      // sort (cheapest-per-turn ranks first when dir='asc'). Every column here is
      // a RATIO, so its team baseline is POOLED (Σnum/Σden), NOT a mean of the
      // per-member ratios (which would over-weight tiny-volume members).
      return [
        {
          key: 'cost_per_turn_usd',
          label: 'Cost / turn',
          format: 'usd',
          metric: (r) => costPerTurn(num(r.cost_usd), num(r.turns)),
          primary: true,
          lowerIsBetter: true,
          // Team cost/turn = Σcost / Σturns across members.
          baselineKind: 'pooled',
          pooled: { num: (r) => num(r.cost_usd), den: (r) => num(r.turns) },
        },
        {
          key: 'output_per_turn',
          label: 'Output / turn',
          format: 'tok',
          metric: (r) => outputTokensPerTurn(num(r.output_tokens), num(r.turns)),
          // Team output/turn = Σoutput / Σturns across members.
          baselineKind: 'pooled',
          pooled: { num: (r) => num(r.output_tokens), den: (r) => num(r.turns) },
        },
        {
          key: 'cache_hit_rate',
          label: 'Cache hit',
          format: 'pct',
          metric: (r) =>
            typeof r.cache_hit_rate === 'number' && Number.isFinite(r.cache_hit_rate)
              ? r.cache_hit_rate
              : cacheHitRate({
                  cache_read: r.cache_read_tokens,
                  input: r.input_tokens,
                  cache_create: r.cache_create_tokens,
                }),
          // Team cache-hit = Σcache_read / Σ(cache_read + input + cache_create)
          // — the same input-side denom the server uses, pooled over members.
          baselineKind: 'pooled',
          pooled: {
            num: (r) => num(r.cache_read_tokens),
            den: (r) =>
              num(r.cache_read_tokens) + num(r.input_tokens) + num(r.cache_create_tokens),
          },
        },
      ];
    case 'cost':
      // Dollars. cost_usd is the primary headline; cost_share contextualizes it
      // against the team total.
      return [
        {
          key: 'cost_usd',
          label: 'Cost',
          format: 'usd',
          metric: (r) => num(r.cost_usd),
          primary: true,
        },
        {
          key: 'cost_share',
          label: 'Share',
          format: 'pct',
          metric: (r) => num(r.cost_share),
        },
      ];
  }
}

// (item 4) Per-row share of a column's metric, normalized to the MAX across rows
// (so the leader reads as 1.0). Used to draw the primary-column share bar from
// the ACTIVE metric — turns under the Output lens, cost under the Cost lens —
// instead of always cost. Returns a userId→0..1 map. Degenerate (all-zero / no
// finite max) → an empty map, and the caller can fall back to cost_share. We key
// by user_id so the share survives the table's re-sort.
export function primaryShares(
  rows: readonly MemberRow[],
  metric: MetricFn<MemberRow>,
): Map<string, number> {
  const out = new Map<string, number>();
  let max = 0;
  for (const r of rows) {
    const v = num(metric(r));
    if (v > max) max = v;
  }
  if (max <= 0) return out;
  for (const r of rows) {
    out.set(r.user_id, Math.max(0, num(metric(r))) / max);
  }
  return out;
}

// (item 5) Build a lens leaderboard: the top `limit` ACTIVE members (turns > 0)
// ranked by a column's metric in its natural best-first direction. Returns the
// pre-shaped leaderboard rows (label/value/sub/isYou), so the LeaderboardCard
// stays metric-agnostic. `format` turns the raw metric into the value string via
// the caller-supplied formatter (keeps fmtUSD/fmtTokens/etc. in the page).
export type LeaderboardEntry = { label: string; value: string; sub?: string; isYou?: boolean };
export function lensLeaderboard(
  rows: readonly MemberRow[],
  column: ColumnSpec,
  format: (raw: number) => string,
  sub: string,
  limit = 3,
): LeaderboardEntry[] {
  const sign = column.lowerIsBetter ? 1 : -1; // asc for lower-is-better, else desc
  return [...rows]
    .filter((m) => num(m.turns) > 0)
    .map((m, i) => ({ m, i, v: num(column.metric(m)) }))
    .sort((a, b) => {
      const d = (a.v - b.v) * sign;
      return d !== 0 ? d : a.i - b.i; // stable on ties
    })
    .slice(0, limit)
    .map(({ m, v }) => ({
      label: m.display_name || m.email || '(unknown)',
      value: format(v),
      sub,
      isYou: !!m.is_you,
    }));
}

// (item 5) The two leaderboard cards for a lens. Each names the lens column to
// rank by (resolved against lensColumns) and a human title + caption. The
// caption flags any cost/turn-derived ranking as model-choice (not quality) so
// the board can't read as a performance scoreboard. Both cards reflect the
// ACTIVE lens (so the lens toggle is fully wired), and only ACTIVE members rank.
export type LeaderboardSpec = { columnKey: string; title: string; caption?: string };
const CPT_CAPTION =
  'lowest API-equivalent cost per turn — reflects model choice, not output quality';
export function lensLeaderboards(lens: Lens): [LeaderboardSpec, LeaderboardSpec] {
  switch (lens) {
    case 'output':
      return [
        { columnKey: 'turns', title: 'Most active' },
        { columnKey: 'output_tokens', title: 'Most output' },
      ];
    case 'efficiency':
      return [
        { columnKey: 'cost_per_turn_usd', title: 'Lowest cost / turn', caption: CPT_CAPTION },
        { columnKey: 'cache_hit_rate', title: 'Best cache hit rate' },
      ];
    case 'cost':
      return [
        { columnKey: 'cost_usd', title: 'Highest spend' },
        { columnKey: 'cost_share', title: 'Largest share' },
      ];
  }
}

// Stable sort of a COPY of `rows` by a column's metric. 'desc' is the natural
// "best first" for higher-is-better metrics; for a lowerIsBetter column the
// caller passes 'asc' to put the best (smallest) first. Ties preserve input
// order (stable) so an already-cost-DESC server order is respected on ties.
export function sortMembers(
  rows: readonly MemberRow[],
  column: ColumnSpec,
  dir: 'asc' | 'desc',
): MemberRow[] {
  const sign = dir === 'asc' ? 1 : -1;
  return rows
    .map((row, i) => ({ row, i }))
    .sort((a, b) => {
      const d = (num(column.metric(a.row)) - num(column.metric(b.row))) * sign;
      return d !== 0 ? d : a.i - b.i; // stable on ties
    })
    .map((x) => x.row);
}

// ---- Activity heatmap -------------------------------------------------------
// Minutes to rotate the server's activity cells by. Older clouds (no `tz` echo)
// bucket cells in UTC → rotate by the browser's offset (the negation of
// getTimezoneOffset). A cloud that echoes `tz` already bucketed them in the
// viewer's zone → 0, never rotate twice (docs/USAGE-CODEX-PROVIDER-CONTRACT.md
// §"Local-day grouping").
export function activityOffsetMinutes(tz?: string | null, now: Date = new Date()): number {
  if (tz) return 0;
  return -now.getTimezoneOffset();
}

// Rotate the SPARSE UTC (dow,hour) cells to a dense 7×24 LOCAL matrix (pass
// localOffsetMinutes = 0 for cells that are already local — see above).
//
// localOffsetMinutes is minutes to ADD to UTC to get local time (the negation
// of Date.getTimezoneOffset() — e.g. Singapore UTC+8 = +480). An hour shift can
// roll the calendar day either way, so we wrap BOTH hour (mod 24) and dow
// (mod 7), carrying the day rollover from the hour wrap. matrix[localDow][localHour].
export type ActivityMetric = 'turns' | 'output_tokens';
export function activityMatrix(
  cells: NonNullable<CCUsageSummary['activity']>['cells'],
  localOffsetMinutes: number,
  metric: ActivityMetric = 'turns',
): { matrix: number[][]; max: number } {
  const matrix: number[][] = Array.from({ length: 7 }, () => new Array<number>(24).fill(0));
  // Whole-hour part of the offset shifts the hour bucket. Buckets are hourly so
  // sub-hour precision can't be displayed; ROUND to the nearest hour — floor
  // skewed UTC+5:45 zones by up to 45 min. Whole-hour zones (the vast majority)
  // are unaffected: round and floor agree on integers.
  const offsetHours = Math.round(localOffsetMinutes / 60);
  let max = 0;
  for (const c of cells ?? []) {
    const value = metric === 'output_tokens' ? num(c.output_tokens) : num(c.turns);
    if (value === 0) continue;
    // Local hour can go negative or past 23 before wrapping; carry the day.
    const rawHour = c.hour + offsetHours;
    const localHour = ((rawHour % 24) + 24) % 24;
    const dayCarry = Math.floor(rawHour / 24); // -1, 0, or +1
    const localDow = (((c.dow + dayCarry) % 7) + 7) % 7;
    const v = matrix[localDow][localHour] + value;
    matrix[localDow][localHour] = v;
    if (v > max) max = v;
  }
  return { matrix, max };
}

// ---- Series for the trend charts -------------------------------------------

// Running-sum of cost over the zero-filled spine. The series is monotonic
// non-decreasing (cost is never negative). Aligns to the same window as the
// daily chart so the cumulative line and the bars share an x-axis.
export function cumulativeSeries(
  daily: CCUsageSummary['daily'],
  windowDays: number,
  tz?: string | null,
  now: Date = new Date(),
): Array<{ date: string; value: number }> {
  const spine = zeroFilledDaily(daily, windowDays, tz, now);
  let acc = 0;
  return spine.map((d) => {
    acc += num(d.cost_usd);
    return { date: d.date, value: acc };
  });
}

// Per-day normalized model-cost fractions for a 100%-stacked area chart. For
// each zero-filled day, normalize that day's by_model costs to sum 1 over the
// requested `models`; a day with 0 cost (or no by_model) yields all-zero
// fractions (the stack collapses to nothing that day). Days carry every
// requested model key (0 when the model wasn't active that day) so the area
// bands are continuous.
export function modelMixSeries(
  daily: CCUsageSummary['daily'],
  models: string[],
  windowDays: number,
  tz?: string | null,
  now: Date = new Date(),
): Array<{ date: string; fractions: Record<string, number> }> {
  const spine = zeroFilledDaily(daily, windowDays, tz, now);
  return spine.map((d) => {
    const bm = d.by_model ?? {};
    // Denominator is the total over the REQUESTED models (a model not in the
    // legend shouldn't inflate the denom and shrink the visible bands).
    let total = 0;
    for (const m of models) total += num(bm[m]);
    const fractions: Record<string, number> = {};
    for (const m of models) {
      fractions[m] = total > 0 ? num(bm[m]) / total : 0;
    }
    return { date: d.date, fractions };
  });
}

// ---------------------------------------------------------------------------
// Phase 3b — owner-view narrative + multi-member trend overlay. All PURE.
// ---------------------------------------------------------------------------

// (Phase 3b) A one-line owner summary distilled from the member roster: who's
// carrying the spend, who's moving the most period-over-period, and who's the
// most cost-efficient. Each highlight is null-safe — an empty roster, a roster
// with no `previous` data, or one with no active members yields nulls so the
// caller renders nothing rather than a bogus "—" highlight.
//
//   - topContributor — highest cost_share (the headline spender). Ties break by
//     the server's incoming order (by_member is cost-DESC, tie user_id), so the
//     first row wins; we keep the strict-`>` scan, which preserves that.
//   - biggestMover — largest |Δ| (magnitude) among members WITH `previous`. The
//     basis is COST (deltaPct(cost_usd, previous.cost_usd)) to match the cost
//     lens headline. A "new" member (prev cost 0 → pct null) is NOT eligible to
//     win on magnitude (null |Δ| can't be compared); we only rank members with a
//     finite pct. null if no member has a finite pct delta. dir carries up/down.
//     Ties break by incoming order (strict `>`).
//   - mostEfficient — lowest cost-per-turn among ACTIVE members (turns > 0),
//     preferring the server's cost_per_turn_usd, else deriving cost/turns. Idle
//     (0-turn) members are ignored (their 0 cost/turn would falsely "win").
//     Ties break by incoming order (strict `<`).
export type TeamInsightHighlight = { name: string; userId: string; share: number };
export type TeamInsightMover = { name: string; userId: string; pct: number; dir: DeltaDir };
export type TeamInsightEfficient = { name: string; userId: string; costPerTurn: number };
export type TeamInsight = {
  membersTotal: number;
  membersActive: number;
  topContributor: TeamInsightHighlight | null;
  biggestMover: TeamInsightMover | null;
  mostEfficient: TeamInsightEfficient | null;
};

function memberName(row: MemberRow): string {
  return row.display_name || row.email || '(unknown)';
}

export function teamInsight(
  byMember: readonly MemberRow[],
  // The team hero is accepted for symmetry with the page's data flow and for
  // future share re-derivation; the highlights here read off the redacted member
  // rows directly (cost_share is already member/team-total), so it's unused.
  _teamHero?: CCUsageSummary['hero'],
): TeamInsight {
  const rows = byMember ?? [];
  const membersTotal = rows.length;
  let membersActive = 0;
  for (const r of rows) if (num(r.turns) > 0) membersActive++;

  // topContributor — highest cost_share. Strict `>` keeps the first (server
  // cost-DESC) row on ties.
  let topContributor: TeamInsightHighlight | null = null;
  let topShare = -Infinity;
  for (const r of rows) {
    const s = num(r.cost_share);
    if (s > topShare) {
      topShare = s;
      topContributor = { name: memberName(r), userId: r.user_id, share: s };
    }
  }

  // biggestMover — largest |pct| among members with a FINITE cost delta (has
  // `previous` AND prev cost > 0). "new" members (null pct) are not rankable on
  // magnitude. Strict `>` keeps the first row on ties.
  let biggestMover: TeamInsightMover | null = null;
  let bestMoverMag = -Infinity;
  for (const r of rows) {
    const d = memberDelta(r, 'cost');
    if (!d || d.pct === null) continue;
    const mag = Math.abs(d.pct);
    if (mag > bestMoverMag) {
      bestMoverMag = mag;
      biggestMover = { name: memberName(r), userId: r.user_id, pct: d.pct, dir: d.dir };
    }
  }

  // mostEfficient — lowest cost/turn among ACTIVE members. Strict `<` keeps the
  // first row on ties.
  let mostEfficient: TeamInsightEfficient | null = null;
  let bestCpt = Infinity;
  for (const r of rows) {
    if (num(r.turns) <= 0) continue;
    const cpt =
      typeof r.cost_per_turn_usd === 'number' && Number.isFinite(r.cost_per_turn_usd)
        ? r.cost_per_turn_usd
        : costPerTurn(num(r.cost_usd), num(r.turns));
    if (cpt < bestCpt) {
      bestCpt = cpt;
      mostEfficient = { name: memberName(r), userId: r.user_id, costPerTurn: cpt };
    }
  }

  return { membersTotal, membersActive, topContributor, biggestMover, mostEfficient };
}

// (Phase 3b) Align every member's sparse `daily[]` onto ONE shared zero-filled
// date spine for the multi-member trend overlay (TeamTrendChart). The spine is
// the SAME window spine the team daily chart uses (zeroFilledDaily logic) UNIONed
// with every member's daily dates, so all member lines share the chart's x-axis
// and no member's real day is dropped.
//
//   - metric — 'cost' reads daily[].cost_usd, 'turns' reads daily[].turns.
//   - active — turns > 0 over the window (any nonzero turn day). For the 'turns'
//     metric this equals "any nonzero value"; we compute it from turns either way
//     so a member spending $0 but taking turns still reads active.
//   - limit — when given, keep only the top `limit` members by total (of the
//     chosen metric) and DROP the rest; the caller can note truncation. Default
//     (undefined) keeps ALL members (including zero-total ones).
//   - members with NO `daily` → an all-zero series, still listed.
//   - series are sorted by total DESC so the caller's color/legend assignment is
//     stable across renders. Colors are NOT assigned here (caller owns the map);
//     we return userId + name so the caller can map.
export type MemberSeries = {
  userId: string;
  name: string;
  values: number[];
  active: boolean;
};
export type AlignedMemberDailies = { dates: string[]; series: MemberSeries[] };

export function alignMemberDailies(
  byMember: readonly MemberRow[],
  windowDays: number,
  metric: 'cost' | 'turns' = 'cost',
  limit?: number,
  tz?: string | null,
  now: Date = new Date(),
): AlignedMemberDailies {
  const rows = byMember ?? [];

  // Build the shared spine: the window spine (zeroFilledDaily) UNIONed with the
  // union of every member's daily dates. We feed a synthetic `daily` of all
  // member dates (cost_usd 0) through zeroFilledDaily so it applies the same
  // window-spine + union logic the team chart uses; the costs are irrelevant
  // here (we only want the date keys).
  const unionDates = new Set<string>();
  for (const r of rows) {
    for (const d of r.daily ?? []) unionDates.add(d.date);
  }
  const spineSeed = [...unionDates].map((date) => ({ date, cost_usd: 0, turns: 0 }));
  const dates = zeroFilledDaily(spineSeed, windowDays, tz, now).map((d) => d.date);
  const dateIndex = new Map(dates.map((d, i) => [d, i]));

  type Built = MemberSeries & { total: number };
  const built: Built[] = rows.map((r) => {
    const values = new Array<number>(dates.length).fill(0);
    let total = 0;
    let active = false;
    for (const d of r.daily ?? []) {
      const idx = dateIndex.get(d.date);
      if (idx === undefined) continue; // outside spine (shouldn't happen — unioned)
      const v = metric === 'turns' ? num(d.turns) : num(d.cost_usd);
      values[idx] += v;
      total += v;
      if (num(d.turns) > 0) active = true;
    }
    return { userId: r.user_id, name: memberName(r), values, active, total };
  });

  // Stable total-DESC ordering (ties preserve incoming/server order).
  const ordered = built
    .map((b, i) => ({ b, i }))
    .sort((a, b) => b.b.total - a.b.total || a.i - b.i)
    .map((x) => x.b);

  const kept = typeof limit === 'number' ? ordered.slice(0, Math.max(0, limit)) : ordered;
  const series: MemberSeries[] = kept.map(({ userId, name, values, active }) => ({
    userId,
    name,
    values,
    active,
  }));
  return { dates, series };
}

// ---------------------------------------------------------------------------
// Phase 3c — per-project member attribution ("who works on what"). PURE.
// ---------------------------------------------------------------------------

// (Phase 3c) One member's slice of a single project, ready for ProjectMembersBar.
// `color` is resolved by the CALLER's color map (passed as `colorFor`) so the
// per-project segment colors stay consistent with the Phase 3b member-trend
// palette; `share` is the member's cost in THIS project / project total (0..1).
export type ProjectMemberSeg = {
  userId: string;
  name: string;
  color: string;
  costUsd: number;
  turns: number;
  share: number;
  isYou?: boolean;
};

// (Phase 3c) Map a top_projects entry's OWNER-ONLY `by_member` breakdown into the
// ProjectMembersBar `members` prop: one segment per contributing member with its
// caller-resolved color, raw cost/turns, and project-local cost share. Sorted by
// share DESC (the server already sends cost-DESC, but we re-sort defensively so
// the bar/legend order is stable regardless of input order). Returns [] when
// `by_member` is absent/empty (solo / member / non-owner response) so the caller
// renders nothing. PURE: the color map is the caller's (passed `colorFor`) — this
// helper never owns colors, keeping them aligned with the member-trend ramp.
export function projectMembers(
  entry: NonNullable<CCUsageSummary['top_projects']>[number],
  colorFor: (userId: string) => string,
  youId?: string,
): ProjectMemberSeg[] {
  const bm = entry.by_member;
  if (!bm || bm.length === 0) return [];
  return bm
    .map((m) => ({
      userId: m.user_id,
      name: m.display_name || '(unknown)',
      color: colorFor(m.user_id),
      costUsd: num(m.cost_usd),
      turns: num(m.turns),
      share: num(m.cost_share),
      isYou: youId != null && m.user_id === youId ? true : undefined,
    }))
    .sort((a, b) => b.share - a.share);
}

// ---------------------------------------------------------------------------
// Phase 4a — owner "period digest" (this period vs the prior equal-length one).
// A synthesized, scannable summary that SUBSUMES the one-line teamInsight with a
// richer card: a headline sentence, the key hero deltas, who's ACCELERATING vs
// COOLING, the top contributor, and the most efficient member. PURE — reuses the
// data already on the team-summary response (no new API). All null-safe.
// ---------------------------------------------------------------------------

// One member's period-over-period movement, ready for the Accelerating/Cooling
// columns. `pct` is a FRACTION (0.25 == +25%); `dir` is its sign. costUsd/turns
// are this period's raw totals (so the card can show context). isYou flags the
// caller's own row. Only members WITH a finite cost delta (has `previous` AND
// prev cost > 0) appear here — a "new" member (prev cost 0 → null pct) is not
// rankable on magnitude and is surfaced as a `newcomer` instead.
export type DigestMover = {
  userId: string;
  name: string;
  pct: number;
  dir: DeltaDir;
  costUsd: number;
  turns: number;
  isYou: boolean;
};

// A member active THIS period with no comparable prior cost (no `previous`, or
// prev cost <= 0) — i.e. someone who just started showing up. Surfaced so the
// owner can see fresh ramp-ups that the up/down movers (which need a finite
// delta) can't represent.
export type DigestNewcomer = {
  userId: string;
  name: string;
  costUsd: number;
  turns: number;
  isYou: boolean;
};

export type PeriodDigest = {
  // Human period length, e.g. "30 days" / "week" — drawn from window_days.
  windowLabel: string;
  // Team-wide hero deltas vs the prior equal-length window (reused heroDeltas).
  heroDeltas: HeroDeltas;
  // Members ramping UP (dir 'up', finite pct), pct DESC; the steepest first.
  moversUp: DigestMover[];
  // Members COOLING (dir 'down', finite pct), pct ASC; the most-negative first.
  moversDown: DigestMover[];
  // Members active this period with no comparable prior cost (fresh ramp-ups).
  newcomers: DigestNewcomer[];
  // From teamInsight — the headline spender, the most cost-efficient member, and
  // the active/total member counts. May be null (no roster / no active member).
  topContributor: TeamInsightHighlight | null;
  mostEfficient: TeamInsightEfficient | null;
  membersActive: number;
  membersTotal: number;
  // A synthesized one-liner ("Team spend up 12% vs the prior 30 days · 5 of 7
  // active · Eli accelerating, Dana cooling."). Clauses are null-safe —
  // any whose data is missing is omitted; always a single line.
  headline: string;
};

// Human label for the comparison period from window_days. "week" reads more
// naturally than "7 days" for the default short window; everything else is
// "<n> days". window_days 0 ("all") has NO prior period → callers must treat it
// as no-digest (periodDigest returns null), so the "all time" branch is only a
// defensive fallback that should never reach the card.
function windowLabel(windowDays: number): string {
  if (windowDays <= 0) return 'all time';
  if (windowDays === 7) return 'week';
  return `${windowDays} days`;
}

// Build the Accelerating/Cooling mover entry for a row from its cost delta. Only
// rows with a FINITE pct (has `previous`, prev cost > 0) qualify; callers filter
// on dir afterwards. null when the row can't be compared.
function digestMover(row: MemberRow): DigestMover | null {
  const d = memberDelta(row, 'cost');
  if (!d || d.pct === null) return null;
  return {
    userId: row.user_id,
    name: memberName(row),
    pct: d.pct,
    dir: d.dir,
    costUsd: num(row.cost_usd),
    turns: num(row.turns),
    isYou: !!row.is_you,
  };
}

// Round a fraction to a signed integer percent for the headline ("up 12%"). The
// card itself uses DeltaBadge; this is only for the synthesized sentence.
function pctWord(fraction: number): string {
  const r = Math.round(fraction * 100);
  return `${Math.abs(r)}%`;
}

export function periodDigest(summary: CCUsageTeamSummary): PeriodDigest | null {
  const byMember = summary.by_member ?? [];
  if (byMember.length === 0) return null;

  // Comparison basis: either a team-wide prior hero, or at least one member with
  // a non-null `previous`. Without EITHER there's nothing to compare against —
  // solo/non-owner/older/"all"-window data degrades to "no digest".
  const hasTeamPrev = !!summary.previous;
  const hasMemberPrev = byMember.some((m) => !!m.previous);
  if (!hasTeamPrev && !hasMemberPrev) return null;

  const days = num(summary.window_days);
  // window=="all" (0) has no prior period → no honest comparison; no digest.
  if (days <= 0) return null;

  const label = windowLabel(days);
  const deltas = heroDeltas(summary.hero, summary.previous);

  // Per-member movers, split by direction. moversUp: dir 'up', pct DESC (steepest
  // ramp first). moversDown: dir 'down', pct ASC (most-negative / biggest cooldown
  // first). Members with no `previous` (null mover) are excluded from both.
  const movers: DigestMover[] = [];
  const newcomers: DigestNewcomer[] = [];
  for (const row of byMember) {
    const m = digestMover(row);
    if (m) {
      movers.push(m);
      continue;
    }
    // No comparable prior cost. Surface as a newcomer only when they're ACTIVE
    // this period (turns > 0) — an idle no-previous member isn't a ramp-up.
    if (num(row.turns) > 0) {
      newcomers.push({
        userId: row.user_id,
        name: memberName(row),
        costUsd: num(row.cost_usd),
        turns: num(row.turns),
        isYou: !!row.is_you,
      });
    }
  }

  const moversUp = movers
    .filter((m) => m.dir === 'up')
    .sort((a, b) => b.pct - a.pct)
    .slice(0, DIGEST_MOVER_N);
  const moversDown = movers
    .filter((m) => m.dir === 'down')
    .sort((a, b) => a.pct - b.pct)
    .slice(0, DIGEST_MOVER_N);

  const insight = teamInsight(byMember, summary.hero);

  // Synthesized headline. Each clause is appended only when its data exists, so a
  // sparse response still yields a sensible (possibly short) one-liner.
  const clauses: string[] = [];
  const spend = deltas.cost_usd;
  if (spend.dir === 'flat') {
    clauses.push(`Team spend flat vs the prior ${label}`);
  } else if (spend.pct === null) {
    // No prior team spend to divide by → "new" rather than a bogus ∞%.
    clauses.push(`Team spend new this ${label}`);
  } else {
    clauses.push(`Team spend ${spend.dir} ${pctWord(spend.pct)} vs the prior ${label}`);
  }
  clauses.push(`${insight.membersActive} of ${insight.membersTotal} active`);
  // "X accelerating, Y cooling" — name only the single steepest in each direction
  // so the headline stays one line; the card lists the rest.
  const accel = moversUp[0];
  const cool = moversDown[0];
  if (accel || cool) {
    const parts: string[] = [];
    if (accel) parts.push(`${accel.name} accelerating`);
    if (cool) parts.push(`${cool.name} cooling`);
    clauses.push(parts.join(', '));
  }
  const headline = clauses.join(' · ');

  return {
    windowLabel: label,
    heroDeltas: deltas,
    moversUp,
    moversDown,
    newcomers,
    topContributor: insight.topContributor,
    mostEfficient: insight.mostEfficient,
    membersActive: insight.membersActive,
    membersTotal: insight.membersTotal,
    headline,
  };
}

// Default number of movers surfaced per direction (Accelerating / Cooling). 3
// keeps each column scannable; the headline names only the steepest of each.
const DIGEST_MOVER_N = 3;

// ---------------------------------------------------------------------------
// Codex provider (docs/USAGE-CODEX-PROVIDER-CONTRACT.md). All PURE.
// ---------------------------------------------------------------------------

// A concrete provider (the filter adds "all"). Rows from older clouds carry no
// provider field and are Claude Code by definition.
export type ModelProvider = 'claude' | 'codex';

// Normalize a row's provider field (or a pricing tier's "anthropic"/"openai")
// to the two providers the UI knows. Empty/unknown → claude, matching the
// ingest rule ("" → claude).
export function normalizeProvider(provider?: string | null): ModelProvider {
  const p = (provider || '').toLowerCase();
  return p === 'codex' || p === 'openai' ? 'codex' : 'claude';
}

// Human name for a provider filter / row. "all" is the unfiltered view.
export function providerLabel(provider: UsageProvider | string | undefined): string {
  if (provider === 'all') return 'All';
  return normalizeProvider(provider) === 'codex' ? 'Codex' : 'Claude Code';
}

// OpenAI tier words the pricing table and member mix may carry as a bare tier.
const OPENAI_TIER_WORDS = new Set(['astra', 'sol', 'terra', 'luna', 'codex', 'gpt', 'openai']);

// True when a model id (or a bare tier key) is an OpenAI/Codex model:
// gpt-*, *codex*, o-series ids, or one of the OpenAI tier words. Lowercased
// substring match like the server; Claude-Code-proxied ids such as
// "claude-gpt-6-astra" still count, since they are priced as GPT.
export function isCodexModel(modelOrTier: string | undefined | null): boolean {
  const m = (modelOrTier || '').toLowerCase();
  if (!m) return false;
  if (OPENAI_TIER_WORDS.has(m)) return true;
  return m.includes('gpt-') || m.startsWith('gpt') || m.includes('codex') || /^o\d/.test(m);
}

// Which provider a model row belongs to: the row's own `provider` field when
// present (authoritative — it records the CLI that produced the turn), else
// sniffed from the model id.
export function modelProvider(model: string, provider?: string | null): ModelProvider {
  if (provider) return normalizeProvider(provider);
  return isCodexModel(model) ? 'codex' : 'claude';
}

// Coarse tier word for a model id, used for the small tier badges where the
// response carries only the raw model (busiest sessions, a member's top model).
// Claude → opus|sonnet|haiku|fable; OpenAI → the family word (astra|sol|terra|
// luna|codex) or "gpt" when the id has none. "?" when nothing matches.
export function modelTier(model: string | undefined | null): string {
  const m = (model || '').toLowerCase();
  if (isCodexModel(m)) {
    if (OPENAI_TIER_WORDS.has(m)) return m === 'openai' ? 'gpt' : m;
    for (const word of ['astra', 'terra', 'luna', 'sol', 'codex']) {
      if (new RegExp(`(^|[-_.])${word}($|[-_.])`).test(m)) return word;
    }
    return 'gpt';
  }
  if (m.includes('opus')) return 'opus';
  if (m.includes('sonnet')) return 'sonnet';
  if (m.includes('haiku')) return 'haiku';
  if (m.includes('fable')) return 'fable';
  return '?';
}

// Readable model name for labels/tooltips: "gpt-6.1-sol-fast" → "GPT-6.1 Sol
// (fast)", "gpt-5.3-codex" → "GPT-5.3 Codex"; Claude ids → their tier
// capitalized ("Opus"). Unrecognized ids pass through unchanged.
export function modelLabel(model: string | undefined | null, provider?: string | null): string {
  const raw = model || '';
  const m = raw.toLowerCase();
  if (modelProvider(raw, provider) === 'codex' || isCodexModel(m)) {
    const fast = /-fast($|[-_.])/.test(m);
    const match = m.match(/gpt-(\d+(?:\.\d+)?)(?:-([a-z]+))?/);
    if (!match) return raw || 'Codex';
    const variant = match[2] && match[2] !== 'fast' ? ' ' + match[2][0].toUpperCase() + match[2].slice(1) : '';
    return `GPT-${match[1]}${variant}${fast ? ' (fast)' : ''}`;
  }
  const tier = modelTier(m);
  return tier === '?' ? raw : tier[0].toUpperCase() + tier.slice(1);
}

// Stable categorical colors keyed by model NAME (not by cost rank), so a model
// keeps its hue when the window or filter changes. Claude models draw from a
// cool/warm "Claude ramp", Codex models from a separate "Codex ramp", so the two
// providers never share a hue. Each model's preferred slot comes from its tier
// (opus → blue, astra → rose, …) or a name hash; on a collision (two opus
// versions) we probe forward through its own ramp, then the other ramp, in
// model-name order so the assignment is deterministic for a given model set.
export const CLAUDE_MODEL_RAMP = [
  'var(--usage-blue)',
  'var(--usage-amber)',
  'var(--usage-green)',
  'var(--usage-violet)',
];
export const CODEX_MODEL_RAMP = [
  'var(--usage-rose)',
  'var(--usage-orange)',
  'var(--usage-teal)',
  'var(--usage-cyan)',
];
export const MODEL_COLOR_FALLBACK = 'color-mix(in srgb, var(--fg) 30%, transparent)';
const TIER_SLOT: Record<string, number> = {
  opus: 0, sonnet: 1, haiku: 2, fable: 3,
  astra: 0, sol: 1, terra: 2, luna: 3, codex: 1,
};

function nameHash(s: string): number {
  let h = 0;
  for (let i = 0; i < s.length; i++) h = (h * 31 + s.charCodeAt(i)) >>> 0;
  return h;
}

export function modelColorMap(
  models: ReadonlyArray<{ model: string; provider?: string | null }>,
): Map<string, string> {
  const out = new Map<string, string>();
  const used = new Set<string>();
  const unique = new Map<string, ModelProvider>();
  for (const m of models) {
    if (!unique.has(m.model)) unique.set(m.model, modelProvider(m.model, m.provider));
  }
  const names = [...unique.keys()].sort((a, b) => a.localeCompare(b));
  // Pass 1: every model claims a slot in its OWN provider's ramp. Pass 2: only
  // models whose ramp is full spill into the other ramp. Doing the spill last
  // keeps a crowded Claude set from taking a Codex model's hue (and vice
  // versa), so filtering to one provider doesn't recolor its models.
  const spill: string[] = [];
  for (const model of names) {
    const own = unique.get(model) === 'codex' ? CODEX_MODEL_RAMP : CLAUDE_MODEL_RAMP;
    const start = TIER_SLOT[modelTier(model)] ?? nameHash(model) % own.length;
    const color = own.map((_, i) => own[(start + i) % own.length]).find((c) => !used.has(c));
    if (color) {
      used.add(color);
      out.set(model, color);
    } else {
      spill.push(model);
    }
  }
  for (const model of spill) {
    const other = unique.get(model) === 'codex' ? CLAUDE_MODEL_RAMP : CODEX_MODEL_RAMP;
    const color = other.find((c) => !used.has(c)) ?? MODEL_COLOR_FALLBACK;
    if (color !== MODEL_COLOR_FALLBACK) used.add(color);
    out.set(model, color);
  }
  return out;
}

// Display rows for the By-provider card: one per provider with its cost share
// (the server's cost_share when finite, else re-derived from the costs so the
// bars still read when an older/partial row omits it), fresh input (input +
// cache write), cached input, output and the reasoning subset of output.
// null when there are no rows (older cloud / empty window) → card hidden.
export type ProviderRow = {
  provider: ModelProvider;
  label: string;
  cost_usd: number;
  share: number;
  turns: number;
  sessions: number;
  fresh_tokens: number;
  cached_tokens: number;
  output_tokens: number;
  reasoning_tokens: number;
};
export function providerRows(byProvider: CCUsageSummary['by_provider']): ProviderRow[] | null {
  if (!byProvider || byProvider.length === 0) return null;
  let total = 0;
  for (const p of byProvider) total += Math.max(0, num(p.cost_usd));
  return byProvider.map((p) => {
    const provider = normalizeProvider(p.provider);
    const share =
      typeof p.cost_share === 'number' && Number.isFinite(p.cost_share)
        ? Math.max(0, Math.min(1, p.cost_share))
        : total > 0
          ? Math.max(0, num(p.cost_usd)) / total
          : 0;
    return {
      provider,
      label: providerLabel(provider),
      cost_usd: num(p.cost_usd),
      share,
      turns: num(p.turns),
      sessions: num(p.sessions),
      fresh_tokens: num(p.input_tokens) + num(p.cache_create_tokens),
      cached_tokens: num(p.cache_read_tokens),
      output_tokens: num(p.output_tokens),
      reasoning_tokens: num(p.reasoning_output_tokens),
    };
  });
}

// Owner comparison: a member's non-Claude share, e.g. "Codex 32%". Uses cost
// share, falling back to turns when the member has no cost (all-zero). null when
// by_provider is absent, the member used only Claude Code, or there's nothing to
// split — Claude-only is the unremarkable default, so it carries no chip.
export function memberProviderMix(row: MemberRow): string | null {
  const bp = row.by_provider;
  if (!bp || bp.length === 0) return null;
  let cost = 0;
  let codexCost = 0;
  let turns = 0;
  let codexTurns = 0;
  for (const p of bp) {
    const isCodex = normalizeProvider(p.provider) === 'codex';
    cost += Math.max(0, num(p.cost_usd));
    turns += Math.max(0, num(p.turns));
    if (isCodex) {
      codexCost += Math.max(0, num(p.cost_usd));
      codexTurns += Math.max(0, num(p.turns));
    }
  }
  const share = cost > 0 ? codexCost / cost : turns > 0 ? codexTurns / turns : 0;
  if (share <= 0) return null;
  return `Codex ${Math.max(1, Math.round(share * 100))}%`;
}

// The latest day a member had activity (cost or turns > 0) in their daily
// series. undefined when the row carries no `daily` (older response → no
// column value at all); null when the series has no active day.
export function lastActiveDate(row: MemberRow): string | null | undefined {
  if (!row.daily) return undefined;
  let latest: string | null = null;
  for (const d of row.daily) {
    if (num(d.turns) <= 0 && num(d.cost_usd) <= 0) continue;
    if (latest === null || d.date > latest) latest = d.date;
  }
  return latest;
}

// Relative label for a "YYYY-MM-DD" day vs `now`: "today", "1d ago",
// "12d ago". Future dates (clock skew) read "today". "—" for an unparseable date.
// `tz` is the response's echoed zone: with it, "today" is that zone's calendar
// date (the dates are local days); without it, UTC (older clouds). Both sides
// are compared as pure calendar dates, so DST never shifts the count.
export function relativeDay(date: string, now: Date = new Date(), tz?: string | null): string {
  const t = Date.parse(date + 'T00:00:00Z');
  if (Number.isNaN(t)) return '—';
  const { y, m0, d } = calendarParts(now, tz);
  const today = Date.UTC(y, m0, d);
  const days = Math.floor((today - t) / 86_400_000);
  if (days <= 0) return 'today';
  return `${days}d ago`;
}
