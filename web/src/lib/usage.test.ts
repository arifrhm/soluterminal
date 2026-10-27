import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest';
import {
  zeroFilledDaily,
  deltaPct,
  heroDeltas,
  cacheHitRate,
  costPerTurn,
  outputTokensPerTurn,
  tokensPerTurn,
  mean,
  median,
  lensColumns,
  sortMembers,
  primaryShares,
  pooledBaseline,
  memberDelta,
  memberModelMix,
  lensLeaderboard,
  lensLeaderboards,
  activityMatrix,
  cumulativeSeries,
  modelMixSeries,
  turnsPerSession,
  teamInsight,
  alignMemberDailies,
  projectMembers,
  periodDigest,
  accountRows,
  isCodexModel,
  modelProvider,
  modelTier,
  modelLabel,
  modelColorMap,
  CLAUDE_MODEL_RAMP,
  CODEX_MODEL_RAMP,
  normalizeProvider,
  providerLabel,
  providerRows,
  memberProviderMix,
  lastActiveDate,
  relativeDay,
  usageDayKey,
  activityOffsetMinutes,
  type MemberRow,
} from './usage';
import type { CCUsageSummary, CCUsageTeamSummary } from '../api';

type Daily = CCUsageSummary['daily'];

// Pin "now" so the UTC-day spine is deterministic. 2026-06-20T19:00:00Z means
// the server's `since = now - Nd` lands at 19:00Z N days back, so the OLDEST
// touched UTC calendar day is one further back than a strict N-day spine —
// the off-by-one this helper must not drop.
const FIXED_NOW = new Date('2026-06-20T19:00:00Z');

describe('zeroFilledDaily', () => {
  beforeEach(() => {
    vi.useFakeTimers();
    vi.setSystemTime(FIXED_NOW);
  });
  afterEach(() => {
    vi.useRealTimers();
  });

  it('returns a contiguous, ascending UTC-day spine for a finite window', () => {
    const out = zeroFilledDaily([], 7);
    // At least the 7-day spine ending today (2026-06-14 .. 2026-06-20).
    const dates = out.map((d) => d.date);
    expect(dates).toContain('2026-06-20');
    expect(dates).toContain('2026-06-14');
    // ascending
    expect([...dates].sort((a, b) => a.localeCompare(b))).toEqual(dates);
    // all zero when no server data
    expect(out.every((d) => d.cost_usd === 0)).toBe(true);
  });

  it('zero-fills gaps between sparse server days', () => {
    const daily: Daily = [
      { date: '2026-06-18', cost_usd: 5, turns: 10 },
      { date: '2026-06-20', cost_usd: 7, turns: 12 },
    ];
    const out = zeroFilledDaily(daily, 7);
    const map = new Map(out.map((d) => [d.date, d.cost_usd]));
    expect(map.get('2026-06-18')).toBe(5);
    expect(map.get('2026-06-19')).toBe(0); // the gap day, filled
    expect(map.get('2026-06-20')).toBe(7);
  });

  it('does NOT drop the partial boundary day the server can include (off-by-one)', () => {
    // since = now - 7d = 2026-06-13T19:00Z → server may return 2026-06-13.
    // A strict 7-day spine (06-14..06-20) would drop it.
    const daily: Daily = [{ date: '2026-06-13', cost_usd: 99, turns: 3 }];
    const out = zeroFilledDaily(daily, 7);
    const map = new Map(out.map((d) => [d.date, d.cost_usd]));
    expect(map.get('2026-06-13')).toBe(99);
    // and the chart total still includes it
    const total = out.reduce((s, d) => s + d.cost_usd, 0);
    expect(total).toBe(99);
  });

  it('never drops any server day even with clock skew beyond today', () => {
    const daily: Daily = [{ date: '2026-06-21', cost_usd: 4, turns: 1 }];
    const out = zeroFilledDaily(daily, 7);
    expect(out.some((d) => d.date === '2026-06-21' && d.cost_usd === 4)).toBe(true);
  });

  it('window "all" (windowDays=0) returns sparse server days sorted, no fill', () => {
    const daily: Daily = [
      { date: '2026-06-20', cost_usd: 7, turns: 1 },
      { date: '2026-01-01', cost_usd: 1, turns: 1 },
    ];
    const out = zeroFilledDaily(daily, 0);
    expect(out.map((d) => d.date)).toEqual(['2026-01-01', '2026-06-20']);
    // no zero-filled gap days
    expect(out.length).toBe(2);
  });

  it('window "all" with no data returns empty array', () => {
    expect(zeroFilledDaily([], 0)).toEqual([]);
  });

  it('produces no NaN costs', () => {
    const daily: Daily = [{ date: '2026-06-20', cost_usd: 7, turns: 1 }];
    const out = zeroFilledDaily(daily, 30);
    expect(out.every((d) => Number.isFinite(d.cost_usd))).toBe(true);
    expect(out.length).toBeGreaterThanOrEqual(30);
  });
});

// ---------------------------------------------------------------------------
// deltaPct
// ---------------------------------------------------------------------------
describe('deltaPct', () => {
  it('returns null pct ("new") when prev is 0', () => {
    expect(deltaPct(50, 0)).toEqual({ pct: null, dir: 'up' });
  });
  it('returns null pct + flat when prev is 0 and cur is 0', () => {
    expect(deltaPct(0, 0)).toEqual({ pct: null, dir: 'flat' });
  });
  it('treats negative prev as "new" (null pct), not a real base', () => {
    expect(deltaPct(10, -5)).toEqual({ pct: null, dir: 'up' });
  });
  it('reports flat for equal values', () => {
    expect(deltaPct(100, 100)).toEqual({ pct: 0, dir: 'flat' });
  });
  it('reports up for an increase', () => {
    const d = deltaPct(150, 100);
    expect(d.dir).toBe('up');
    expect(d.pct).toBeCloseTo(0.5, 12);
  });
  it('reports down for a decrease', () => {
    const d = deltaPct(75, 100);
    expect(d.dir).toBe('down');
    expect(d.pct).toBeCloseTo(-0.25, 12);
  });
  it('treats a sub-epsilon change as flat', () => {
    const d = deltaPct(100 + 1e-12, 100);
    expect(d.dir).toBe('flat');
  });
  it('coerces non-finite inputs to 0', () => {
    expect(deltaPct(NaN, 10).dir).toBe('down'); // cur→0, prev=10
    expect(deltaPct(10, NaN)).toEqual({ pct: null, dir: 'up' }); // prev→0
  });
});

// ---------------------------------------------------------------------------
// heroDeltas
// ---------------------------------------------------------------------------
describe('heroDeltas', () => {
  const hero: CCUsageSummary['hero'] = {
    cost_usd: 120,
    turns: 200,
    sessions: 30,
    active_days: 20,
    fresh_tokens: 5000,
    cached_tokens: 90000,
    models_used: 3,
    cache_hit_rate: 0.9,
  };

  it('computes per-metric cur/prev/pct/dir against a prior hero', () => {
    const prev: CCUsageSummary['previous'] = {
      window: '30d',
      hero: {
        cost_usd: 100,
        turns: 100,
        sessions: 30,
        active_days: 25,
        fresh_tokens: 4000,
        cached_tokens: 80000,
        cache_hit_rate: 0.8,
      },
    };
    const d = heroDeltas(hero, prev);
    expect(d.cost_usd).toEqual({ cur: 120, prev: 100, pct: expect.closeTo(0.2, 12), dir: 'up' });
    expect(d.turns.dir).toBe('up');
    expect(d.sessions.dir).toBe('flat'); // 30 vs 30
    expect(d.active_days.dir).toBe('down'); // 20 vs 25
    expect(d.cache_hit_rate.cur).toBe(0.9);
  });

  it('is null-safe when previous is null → every pct null ("new")', () => {
    const d = heroDeltas(hero, null);
    expect(d.cost_usd).toEqual({ cur: 120, prev: 0, pct: null, dir: 'up' });
    expect(d.turns.pct).toBeNull();
    expect(d.cache_hit_rate.prev).toBe(0);
  });

  it('is null-safe when previous is undefined (older response)', () => {
    const d = heroDeltas(hero, undefined);
    expect(d.sessions.pct).toBeNull();
  });

  it('defaults a missing cache_hit_rate to 0', () => {
    const heroNoHit = { ...hero };
    delete (heroNoHit as { cache_hit_rate?: number }).cache_hit_rate;
    const d = heroDeltas(heroNoHit, null);
    expect(d.cache_hit_rate.cur).toBe(0);
  });
});

// ---------------------------------------------------------------------------
// Efficiency helpers
// ---------------------------------------------------------------------------
describe('efficiency helpers', () => {
  it('cacheHitRate mirrors the server denom (read / (read+input+create))', () => {
    expect(cacheHitRate({ cache_read: 90, input: 5, cache_create: 5 })).toBeCloseTo(0.9, 12);
  });
  it('cacheHitRate returns 0 when the denom is 0', () => {
    expect(cacheHitRate({ cache_read: 0, input: 0, cache_create: 0 })).toBe(0);
    expect(cacheHitRate({})).toBe(0);
  });
  it('cacheHitRate excludes output (only the three input-side fields count)', () => {
    // output is not a field of TokenBundle; passing extra keys is ignored.
    expect(cacheHitRate({ cache_read: 50, input: 50 })).toBeCloseTo(0.5, 12);
  });
  it('costPerTurn guards divide-by-zero', () => {
    expect(costPerTurn(10, 4)).toBe(2.5);
    expect(costPerTurn(10, 0)).toBe(0);
  });
  it('outputTokensPerTurn guards divide-by-zero', () => {
    expect(outputTokensPerTurn(1000, 5)).toBe(200);
    expect(outputTokensPerTurn(1000, 0)).toBe(0);
  });
  it('tokensPerTurn guards divide-by-zero', () => {
    expect(tokensPerTurn(900, 3)).toBe(300);
    expect(tokensPerTurn(900, 0)).toBe(0);
  });
});

// ---------------------------------------------------------------------------
// Team baselines: mean / median
// ---------------------------------------------------------------------------
describe('mean / median', () => {
  const rows = [{ v: 4 }, { v: 1 }, { v: 7 }];
  const v = (r: { v: number }) => r.v;

  it('mean averages the metric', () => {
    expect(mean(rows, v)).toBeCloseTo((4 + 1 + 7) / 3, 12);
  });
  it('mean of an empty set is 0', () => {
    expect(mean([], v)).toBe(0);
  });
  it('median of odd length is the middle of the sorted values', () => {
    expect(median(rows, v)).toBe(4); // sorted [1,4,7]
  });
  it('median of even length is the mean of the two middle values', () => {
    expect(median([{ v: 1 }, { v: 2 }, { v: 3 }, { v: 4 }], v)).toBe(2.5);
  });
  it('median of an empty set is 0', () => {
    expect(median([], v)).toBe(0);
  });
  it('median does not mutate the input order', () => {
    const input = [{ v: 9 }, { v: 1 }, { v: 5 }];
    median(input, v);
    expect(input.map(v)).toEqual([9, 1, 5]);
  });
});

// ---------------------------------------------------------------------------
// Lens model: lensColumns + sortMembers
// ---------------------------------------------------------------------------
function member(over: Partial<MemberRow>): MemberRow {
  return {
    user_id: over.user_id ?? 'u',
    display_name: over.display_name ?? 'name',
    email: over.email ?? 'e@x',
    is_you: over.is_you ?? false,
    roles: over.roles ?? [],
    cost_usd: over.cost_usd ?? 0,
    turns: over.turns ?? 0,
    sessions: over.sessions ?? 0,
    top_model: over.top_model ?? 'sonnet',
    cost_share: over.cost_share ?? 0,
    ...over,
  };
}

describe('lensColumns', () => {
  it('output lens: turns primary, expected columns (incl. turns/session)', () => {
    const cols = lensColumns('output');
    expect(cols.map((c) => c.key)).toEqual([
      'turns',
      'turns_per_session',
      'sessions',
      'active_days',
      'output_tokens',
    ]);
    expect(cols.length).toBeLessThanOrEqual(5); // stays readable
    expect(cols.find((c) => c.primary)?.key).toBe('turns');
    expect(cols.find((c) => c.key === 'output_tokens')?.format).toBe('tok');
  });

  it('output lens: turns/session column is int, higher-is-better, pooled baseline', () => {
    const col = lensColumns('output').find((c) => c.key === 'turns_per_session')!;
    expect(col.label).toBe('Turns/session');
    expect(col.format).toBe('int');
    expect(col.lowerIsBetter).toBeUndefined(); // higher-is-better (default)
    expect(col.metric(member({ turns: 20, sessions: 5 }))).toBe(4);
    expect(col.metric(member({ turns: 20, sessions: 0 }))).toBe(0); // guard
    // Pooled team baseline = Σturns / Σsessions, not a mean of per-member ratios.
    const rows = [
      member({ turns: 200, sessions: 10 }), // 20/session
      member({ turns: 2, sessions: 2 }), //    1/session
    ];
    expect(pooledBaseline(rows, col)).toBeCloseTo(202 / 12, 12);
  });

  it('efficiency lens: cost_per_turn primary AND ascending-is-better', () => {
    const cols = lensColumns('efficiency');
    expect(cols.map((c) => c.key)).toEqual([
      'cost_per_turn_usd',
      'output_per_turn',
      'cache_hit_rate',
    ]);
    const primary = cols.find((c) => c.primary)!;
    expect(primary.key).toBe('cost_per_turn_usd');
    expect(primary.lowerIsBetter).toBe(true);
  });

  it('cost lens: cost_usd primary, cost_share', () => {
    const cols = lensColumns('cost');
    expect(cols.map((c) => c.key)).toEqual(['cost_usd', 'cost_share']);
    expect(cols.find((c) => c.primary)?.key).toBe('cost_usd');
  });

  it('every lens has exactly one primary column', () => {
    for (const lens of ['output', 'efficiency', 'cost'] as const) {
      expect(lensColumns(lens).filter((c) => c.primary).length).toBe(1);
    }
  });

  it('efficiency metrics are robust to absent enriched fields (fall back to 0)', () => {
    const cols = lensColumns('efficiency');
    const bare = member({ cost_usd: 0, turns: 0 }); // no output_tokens/cache fields
    for (const c of cols) {
      const val = c.metric(bare);
      expect(Number.isFinite(val)).toBe(true);
      expect(val).toBe(0);
    }
  });

  it('cost_per_turn metric derives from cost/turns when the enriched field is absent', () => {
    const col = lensColumns('efficiency').find((c) => c.key === 'cost_per_turn_usd')!;
    expect(col.metric(member({ cost_usd: 20, turns: 4 }))).toBe(5);
  });

  it('cache_hit_rate metric prefers the server value, else derives from tokens', () => {
    const col = lensColumns('efficiency').find((c) => c.key === 'cache_hit_rate')!;
    expect(col.metric(member({ cache_hit_rate: 0.77 }))).toBe(0.77);
    expect(
      col.metric(
        member({ cache_read_tokens: 90, input_tokens: 5, cache_create_tokens: 5 }),
      ),
    ).toBeCloseTo(0.9, 12);
  });
});

describe('sortMembers', () => {
  const rows = [
    member({ user_id: 'a', cost_usd: 10 }),
    member({ user_id: 'b', cost_usd: 30 }),
    member({ user_id: 'c', cost_usd: 20 }),
  ];
  const costCol = lensColumns('cost').find((c) => c.key === 'cost_usd')!;

  it('sorts desc (best-first for higher-is-better)', () => {
    expect(sortMembers(rows, costCol, 'desc').map((r) => r.user_id)).toEqual(['b', 'c', 'a']);
  });
  it('sorts asc', () => {
    expect(sortMembers(rows, costCol, 'asc').map((r) => r.user_id)).toEqual(['a', 'c', 'b']);
  });
  it('does not mutate the input', () => {
    const before = rows.map((r) => r.user_id);
    sortMembers(rows, costCol, 'desc');
    expect(rows.map((r) => r.user_id)).toEqual(before);
  });
  it('is STABLE on ties (preserves input order)', () => {
    const tied = [
      member({ user_id: 'x', cost_usd: 5 }),
      member({ user_id: 'y', cost_usd: 5 }),
      member({ user_id: 'z', cost_usd: 5 }),
    ];
    expect(sortMembers(tied, costCol, 'desc').map((r) => r.user_id)).toEqual(['x', 'y', 'z']);
    expect(sortMembers(tied, costCol, 'asc').map((r) => r.user_id)).toEqual(['x', 'y', 'z']);
  });
  it('ascending-better efficiency: cheapest cost/turn ranks first under asc', () => {
    const col = lensColumns('efficiency').find((c) => c.key === 'cost_per_turn_usd')!;
    const effRows = [
      member({ user_id: 'pricey', cost_usd: 100, turns: 10 }), // 10/turn
      member({ user_id: 'cheap', cost_usd: 10, turns: 10 }), // 1/turn
      member({ user_id: 'mid', cost_usd: 50, turns: 10 }), // 5/turn
    ];
    expect(sortMembers(effRows, col, 'asc').map((r) => r.user_id)).toEqual([
      'cheap',
      'mid',
      'pricey',
    ]);
  });
});

// ---------------------------------------------------------------------------
// primaryShares (item 4) — share bar reflects the ACTIVE primary metric
// ---------------------------------------------------------------------------
describe('primaryShares', () => {
  it('normalizes a column metric to the leader (raw / max)', () => {
    // (item 4) Under the Output lens the bar must encode TURNS-share, NOT cost.
    const turnsCol = lensColumns('output').find((c) => c.key === 'turns')!;
    const rows = [
      member({ user_id: 'a', turns: 100, cost_usd: 5 }),
      member({ user_id: 'b', turns: 50, cost_usd: 80 }),
      member({ user_id: 'c', turns: 0, cost_usd: 0 }),
    ];
    const shares = primaryShares(rows, turnsCol.metric);
    expect(shares.get('a')).toBe(1); // leader
    expect(shares.get('b')).toBe(0.5); // half the leader's turns
    expect(shares.get('c')).toBe(0);
    // It tracks turns, not cost: b has the most cost but only half the bar.
  });

  it('returns an empty map when the metric is degenerate (all zero)', () => {
    const turnsCol = lensColumns('output').find((c) => c.key === 'turns')!;
    const rows = [member({ user_id: 'a', turns: 0 }), member({ user_id: 'b', turns: 0 })];
    expect(primaryShares(rows, turnsCol.metric).size).toBe(0);
  });

  it('matches cost_share semantics under the Cost lens (so the bar is unchanged)', () => {
    const costCol = lensColumns('cost').find((c) => c.key === 'cost_usd')!;
    const rows = [
      member({ user_id: 'a', cost_usd: 80 }),
      member({ user_id: 'b', cost_usd: 20 }),
    ];
    const shares = primaryShares(rows, costCol.metric);
    expect(shares.get('a')).toBe(1);
    expect(shares.get('b')).toBeCloseTo(0.25, 12);
  });
});

// ---------------------------------------------------------------------------
// pooledBaseline (Phase 3a) — ratio columns POOL totals, additive use mean
// ---------------------------------------------------------------------------
describe('pooledBaseline', () => {
  it('additive column (cost_usd) uses the mean (== pooled total / N)', () => {
    const col = lensColumns('cost').find((c) => c.key === 'cost_usd')!;
    const rows = [
      member({ cost_usd: 10 }),
      member({ cost_usd: 20 }),
      member({ cost_usd: 30 }),
    ];
    expect(pooledBaseline(rows, col)).toBeCloseTo(20, 12); // (10+20+30)/3
  });

  it('cost_per_turn baseline is POOLED Σcost/Σturns, NOT mean-of-ratios', () => {
    // The whole point of the fix: a tiny-volume member must not skew the team
    // rate. big = $0.10/turn over 2000 turns; tiny = $5/turn over 2 turns.
    const col = lensColumns('efficiency').find((c) => c.key === 'cost_per_turn_usd')!;
    const rows = [
      member({ cost_usd: 200, turns: 2000 }), // 0.10 /turn
      member({ cost_usd: 10, turns: 2 }), //     5.00 /turn
    ];
    // Pooled = (200+10) / (2000+2) = 210 / 2002 ≈ 0.1049.
    expect(pooledBaseline(rows, col)).toBeCloseTo(210 / 2002, 12);
    // The (wrong) mean of per-member ratios would be (0.10 + 5.00)/2 = 2.55 —
    // ~24× larger. Assert we are NOWHERE near it.
    expect(pooledBaseline(rows, col)).toBeLessThan(0.2);
  });

  it('cache_hit_rate baseline pools the input-side denom across members', () => {
    const col = lensColumns('efficiency').find((c) => c.key === 'cache_hit_rate')!;
    const rows = [
      // read 90 / (90+5+5) = 0.90
      member({ cache_read_tokens: 90, input_tokens: 5, cache_create_tokens: 5 }),
      // read 0 / (0+10+0) = 0.00
      member({ cache_read_tokens: 0, input_tokens: 10, cache_create_tokens: 0 }),
    ];
    // Pooled = Σread / Σ(read+input+create) = 90 / (100 + 10) = 90/110.
    expect(pooledBaseline(rows, col)).toBeCloseTo(90 / 110, 12);
  });

  it('output_per_turn baseline is Σoutput / Σturns', () => {
    const col = lensColumns('efficiency').find((c) => c.key === 'output_per_turn')!;
    const rows = [
      member({ output_tokens: 1000, turns: 10 }),
      member({ output_tokens: 500, turns: 90 }),
    ];
    expect(pooledBaseline(rows, col)).toBeCloseTo(1500 / 100, 12); // 15
  });

  it('returns 0 for an empty set and 0 when the pooled denom is 0', () => {
    const cptCol = lensColumns('efficiency').find((c) => c.key === 'cost_per_turn_usd')!;
    expect(pooledBaseline([], cptCol)).toBe(0);
    // Members with cost but zero turns → denom 0 → 0, not NaN/Infinity.
    const noTurns = [member({ cost_usd: 5, turns: 0 }), member({ cost_usd: 8, turns: 0 })];
    expect(pooledBaseline(noTurns, cptCol)).toBe(0);
  });

  it('falls back to the mean when a pooled column lacks its accessors', () => {
    // A pooled-kind column missing `pooled` (defensive path) uses mean(metric).
    const col = { ...lensColumns('cost').find((c) => c.key === 'cost_usd')!, baselineKind: 'pooled' as const };
    delete (col as { pooled?: unknown }).pooled;
    const rows = [member({ cost_usd: 4 }), member({ cost_usd: 8 })];
    expect(pooledBaseline(rows, col)).toBe(6);
  });
});

// ---------------------------------------------------------------------------
// memberDelta (Phase 3a) — per-member "who's ramping" direction + guard
// ---------------------------------------------------------------------------
describe('memberDelta', () => {
  it('cost lens compares cost vs previous.cost_usd', () => {
    const m = member({ cost_usd: 150, turns: 10, previous: { cost_usd: 100, turns: 10 } });
    const d = memberDelta(m, 'cost')!;
    expect(d.dir).toBe('up');
    expect(d.pct).toBeCloseTo(0.5, 12); // +50% cost
  });

  it('output lens compares TURNS vs previous.turns', () => {
    const m = member({ cost_usd: 100, turns: 80, previous: { cost_usd: 100, turns: 100 } });
    // cost is flat but turns fell 20% → the engagement lens reads a turns drop.
    expect(memberDelta(m, 'output')!.dir).toBe('down');
    expect(memberDelta(m, 'output')!.pct).toBeCloseTo(-0.2, 12);
  });

  it('efficiency lens compares COST PER TURN (the column it is drawn in)', () => {
    // Same cost over 20% fewer turns → cost/turn rose 25% ($1.25 vs $1.00).
    const m = member({ cost_usd: 100, turns: 80, previous: { cost_usd: 100, turns: 100 } });
    const d = memberDelta(m, 'efficiency')!;
    expect(d.dir).toBe('up');
    expect(d.pct).toBeCloseTo(0.25, 12);
  });

  it('efficiency lens reports "new" when there were no prior turns', () => {
    const m = member({ cost_usd: 10, turns: 5, previous: { cost_usd: 0, turns: 0 } });
    expect(memberDelta(m, 'efficiency')!.pct).toBeNull();
  });

  it('returns null when previous is absent (older response / non-owner)', () => {
    expect(memberDelta(member({ cost_usd: 50, turns: 5 }), 'cost')).toBeNull();
  });

  it('returns null when previous is explicitly null (window=="all" / no prior)', () => {
    const m = member({ cost_usd: 50, turns: 5, previous: null });
    expect(memberDelta(m, 'output')).toBeNull();
  });

  it('reports "new" (null pct) when the prior window had zero of the basis', () => {
    const m = member({ cost_usd: 40, turns: 8, previous: { cost_usd: 0, turns: 0 } });
    const d = memberDelta(m, 'cost')!;
    expect(d.pct).toBeNull();
    expect(d.dir).toBe('up'); // activity appeared
  });
});

// ---------------------------------------------------------------------------
// memberModelMix (Phase 3a) — tier-mix normalization + guard + overflow fold
// ---------------------------------------------------------------------------
describe('memberModelMix', () => {
  it('normalizes by_model costs to shares summing ~1 (cost-DESC order kept)', () => {
    const m = member({
      by_model: [
        { model: 'opus', tier: 'opus', cost_usd: 60, turns: 10, cost_share: 0.6 },
        { model: 'sonnet', tier: 'sonnet', cost_usd: 30, turns: 20, cost_share: 0.3 },
        { model: 'haiku', tier: 'haiku', cost_usd: 10, turns: 40, cost_share: 0.1 },
      ],
    });
    const mix = memberModelMix(m);
    expect(mix.map((s) => s.model)).toEqual(['opus', 'sonnet', 'haiku']);
    expect(mix.map((s) => s.tier)).toEqual(['opus', 'sonnet', 'haiku']);
    expect(mix[0].share).toBeCloseTo(0.6, 12);
    expect(mix[1].share).toBeCloseTo(0.3, 12);
    expect(mix[2].share).toBeCloseTo(0.1, 12);
    expect(mix.reduce((s, x) => s + x.share, 0)).toBeCloseTo(1, 12);
  });

  it('re-derives shares from cost (ignores a drifted server cost_share)', () => {
    const m = member({
      by_model: [
        { model: 'opus', tier: 'opus', cost_usd: 75, turns: 1, cost_share: 0.999 },
        { model: 'haiku', tier: 'haiku', cost_usd: 25, turns: 1, cost_share: 0.001 },
      ],
    });
    const mix = memberModelMix(m);
    expect(mix[0].share).toBeCloseTo(0.75, 12);
    expect(mix[1].share).toBeCloseTo(0.25, 12);
  });

  it('folds overflow models beyond the limit into a trailing "other" segment', () => {
    const m = member({
      by_model: [
        { model: 'a', tier: 'opus', cost_usd: 50, turns: 1, cost_share: 0.5 },
        { model: 'b', tier: 'sonnet', cost_usd: 20, turns: 1, cost_share: 0.2 },
        { model: 'c', tier: 'haiku', cost_usd: 15, turns: 1, cost_share: 0.15 },
        { model: 'd', tier: 'opus', cost_usd: 10, turns: 1, cost_share: 0.1 },
        { model: 'e', tier: 'haiku', cost_usd: 5, turns: 1, cost_share: 0.05 },
      ],
    });
    const mix = memberModelMix(m, 4);
    // 4 head segments + 1 folded "other".
    expect(mix.length).toBe(5);
    expect(mix[4].model).toBe('other');
    expect(mix[4].share).toBeCloseTo(0.05, 12); // just model e
    expect(mix.reduce((s, x) => s + x.share, 0)).toBeCloseTo(1, 12);
  });

  it('returns [] when by_model is absent (older response / non-owner)', () => {
    expect(memberModelMix(member({}))).toEqual([]);
  });

  it('returns [] when by_model is present but all costs are 0', () => {
    const m = member({
      by_model: [{ model: 'opus', tier: 'opus', cost_usd: 0, turns: 0, cost_share: 0 }],
    });
    expect(memberModelMix(m)).toEqual([]);
  });
});

// ---------------------------------------------------------------------------
// lensLeaderboard / lensLeaderboards (item 5) — lens-aware + honest
// ---------------------------------------------------------------------------
describe('lensLeaderboard', () => {
  const fmt = (n: number) => String(n);

  it('ranks ACTIVE members best-first for a higher-is-better column', () => {
    const turnsCol = lensColumns('output').find((c) => c.key === 'turns')!;
    const rows = [
      member({ user_id: 'a', display_name: 'A', turns: 10 }),
      member({ user_id: 'b', display_name: 'B', turns: 30 }),
      member({ user_id: 'c', display_name: 'C', turns: 20 }),
      member({ user_id: 'idle', display_name: 'Idle', turns: 0 }), // excluded
    ];
    const out = lensLeaderboard(rows, turnsCol, fmt, 'turns');
    expect(out.map((r) => r.label)).toEqual(['B', 'C', 'A']);
    expect(out.every((r) => r.sub === 'turns')).toBe(true);
  });

  it('ranks lower-is-better (cost/turn) cheapest-first and excludes idle members', () => {
    const cptCol = lensColumns('efficiency').find((c) => c.key === 'cost_per_turn_usd')!;
    const rows = [
      member({ user_id: 'pricey', display_name: 'Pricey', cost_usd: 100, turns: 10 }), // 10
      member({ user_id: 'cheap', display_name: 'Cheap', cost_usd: 10, turns: 10 }), // 1
      member({ user_id: 'idle', display_name: 'Idle', cost_usd: 0, turns: 0 }), // 0 cpt but EXCLUDED
    ];
    const out = lensLeaderboard(rows, cptCol, fmt, '/ turn');
    // Idle (0 cost/turn) must NOT win — only active members rank.
    expect(out.map((r) => r.label)).toEqual(['Cheap', 'Pricey']);
  });

  it('honors the limit and flags the caller row via isYou', () => {
    const turnsCol = lensColumns('output').find((c) => c.key === 'turns')!;
    const rows = [
      member({ user_id: 'a', display_name: 'A', turns: 5, is_you: true }),
      member({ user_id: 'b', display_name: 'B', turns: 9 }),
      member({ user_id: 'c', display_name: 'C', turns: 7 }),
    ];
    const out = lensLeaderboard(rows, turnsCol, fmt, 'turns', 2);
    expect(out.length).toBe(2);
    expect(out.map((r) => r.label)).toEqual(['B', 'C']);
    const you = lensLeaderboard(rows, turnsCol, fmt, 'turns');
    expect(you.find((r) => r.label === 'A')?.isYou).toBe(true);
  });
});

describe('lensLeaderboards', () => {
  it('returns two cards reflecting the ACTIVE lens', () => {
    expect(lensLeaderboards('output').map((s) => s.columnKey)).toEqual([
      'turns',
      'output_tokens',
    ]);
    expect(lensLeaderboards('cost').map((s) => s.columnKey)).toEqual([
      'cost_usd',
      'cost_share',
    ]);
  });

  it('flags the cost/turn efficiency card with an honesty caption (model choice)', () => {
    const [first, second] = lensLeaderboards('efficiency');
    expect(first.columnKey).toBe('cost_per_turn_usd');
    expect(first.caption).toMatch(/model choice/i);
    expect(first.caption).toMatch(/not output quality/i);
    // The non-cost card carries no caption.
    expect(second.caption).toBeUndefined();
  });
});

// ---------------------------------------------------------------------------
// activityMatrix — tz rotation incl. negative-hour & dow wraparound
// ---------------------------------------------------------------------------
type Cells = NonNullable<CCUsageSummary['activity']>['cells'];

describe('activityMatrix', () => {
  it('UTC→local same-day shift (no day roll): UTC hour 2 +480min → local hour 10', () => {
    const cells: Cells = [{ dow: 3, hour: 2, turns: 5, output_tokens: 999 }];
    const { matrix, max } = activityMatrix(cells, 480);
    expect(matrix[3][10]).toBe(5); // same dow (Wed), hour 2→10
    expect(max).toBe(5);
    // nothing else set
    expect(matrix.flat().reduce((a, b) => a + b, 0)).toBe(5);
  });

  it('forward day roll: UTC hour 20 +480min → local hour 4 NEXT dow', () => {
    const cells: Cells = [{ dow: 6, hour: 20, turns: 3, output_tokens: 0 }]; // Sat 20:00Z
    const { matrix } = activityMatrix(cells, 480);
    // 20+8 = 28 → hour 4, day carry +1 → Sat(6)→Sun(0) (dow wrap)
    expect(matrix[0][4]).toBe(3);
  });

  it('backward day roll: UTC hour 2 -300min (UTC-5) → hour 21 PREVIOUS dow', () => {
    const cells: Cells = [{ dow: 0, hour: 2, turns: 7, output_tokens: 0 }]; // Sun 02:00Z
    const { matrix } = activityMatrix(cells, -300);
    // 2-5 = -3 → hour 21, day carry -1 → Sun(0)→Sat(6) (dow wrap backward)
    expect(matrix[6][21]).toBe(7);
  });

  it('uses turns by default and output_tokens when asked', () => {
    const cells: Cells = [{ dow: 1, hour: 0, turns: 2, output_tokens: 500 }];
    expect(activityMatrix(cells, 0).matrix[1][0]).toBe(2);
    expect(activityMatrix(cells, 0, 'output_tokens').matrix[1][0]).toBe(500);
  });

  it('accumulates cells that collapse to the same local bucket and tracks max', () => {
    const cells: Cells = [
      { dow: 2, hour: 5, turns: 4, output_tokens: 0 },
      { dow: 2, hour: 5, turns: 6, output_tokens: 0 },
    ];
    const { matrix, max } = activityMatrix(cells, 0);
    expect(matrix[2][5]).toBe(10);
    expect(max).toBe(10);
  });

  it('returns a dense 7×24 zero matrix for empty/undefined cells', () => {
    const { matrix, max } = activityMatrix([], 480);
    expect(matrix.length).toBe(7);
    expect(matrix.every((row) => row.length === 24 && row.every((v) => v === 0))).toBe(true);
    expect(max).toBe(0);
  });

  it('rounds a half-hour offset to the nearest hour (UTC+5:30 → +6, not +5)', () => {
    // India UTC+5:30 (330 min). A UTC 10:00 turn is 15:30 local. Flooring to +5
    // bucketed it at 15; rounding bucket it at 16. Either is 30 min off for :30
    // exactly, but rounding is consistent and never 45 min off for :45 zones.
    const cells: Cells = [{ dow: 1, hour: 10, turns: 4, output_tokens: 0 }];
    const { matrix } = activityMatrix(cells, 330);
    expect(matrix[1][16]).toBe(4); // round(5.5) = 6 → 10 + 6 = 16
    expect(matrix[1][15]).toBe(0); // the old floor(330/60)=5 path landed here
  });

  it('rounds a :45 offset (UTC+5:45 → +6): floor would have skewed 45 min', () => {
    const cells: Cells = [{ dow: 1, hour: 10, turns: 4, output_tokens: 0 }];
    const { matrix } = activityMatrix(cells, 345); // Nepal
    expect(matrix[1][16]).toBe(4); // round(5.75) = 6; floor(5.75)=5 would be a 45-min skew
    expect(matrix[1][15]).toBe(0);
  });
});

describe('accountRows', () => {
  // Minimal by_account element factory.
  const row = (account: string, cost = 1, turns = 1, share = 0.5) => ({
    account,
    turns,
    input_tokens: 0,
    output_tokens: 0,
    cache_read_tokens: 0,
    cache_create_tokens: 0,
    cost_usd: cost,
    cost_per_turn_usd: cost / turns,
    cost_share: share,
  });

  it('returns null when absent, empty, or only-unattributed (no named account)', () => {
    expect(accountRows(undefined)).toBeNull();
    expect(accountRows([])).toBeNull();
    expect(accountRows([row('')])).toBeNull();
    expect(accountRows([row(''), row('')])).toBeNull();
  });

  it('labels "" as Unattributed + dim, keeps named accounts non-dim', () => {
    const out = accountRows([row('alice@team', 10, 100, 0.8), row(''), row('bob@personal')]);
    expect(out).not.toBeNull();
    expect(out!.map((a) => ({ label: a.label, dim: a.dim }))).toEqual([
      { label: 'alice@team', dim: false },
      { label: 'Unattributed', dim: true },
      { label: 'bob@personal', dim: false },
    ]);
  });

  it('passes through cost / turns / share and preserves server order', () => {
    const out = accountRows([row('alice@team', 12.5, 50, 0.75), row('bob@personal', 2, 5, 0.25)]);
    expect(out).toEqual([
      expect.objectContaining({ account: 'alice@team', cost_usd: 12.5, turns: 50, cost_share: 0.75 }),
      expect.objectContaining({ account: 'bob@personal', cost_usd: 2, turns: 5, cost_share: 0.25 }),
    ]);
  });
});

// ---------------------------------------------------------------------------
// cumulativeSeries / modelMixSeries
// ---------------------------------------------------------------------------
describe('cumulativeSeries', () => {
  beforeEach(() => {
    vi.useFakeTimers();
    vi.setSystemTime(FIXED_NOW);
  });
  afterEach(() => {
    vi.useRealTimers();
  });

  it('is a monotonic non-decreasing running sum aligned to the spine', () => {
    const daily: Daily = [
      { date: '2026-06-18', cost_usd: 5, turns: 2 },
      { date: '2026-06-20', cost_usd: 7, turns: 3 },
    ];
    const out = cumulativeSeries(daily, 7);
    // monotonic
    for (let i = 1; i < out.length; i++) {
      expect(out[i].value).toBeGreaterThanOrEqual(out[i - 1].value);
    }
    // final equals the total cost
    expect(out[out.length - 1].value).toBe(12);
    // the gap day (06-19) holds flat at 5
    const m = new Map(out.map((d) => [d.date, d.value]));
    expect(m.get('2026-06-19')).toBe(5);
  });
});

describe('modelMixSeries', () => {
  beforeEach(() => {
    vi.useFakeTimers();
    vi.setSystemTime(FIXED_NOW);
  });
  afterEach(() => {
    vi.useRealTimers();
  });

  const models = ['opus', 'sonnet'];

  it('normalizes each non-zero day to sum ~1 over the requested models', () => {
    const daily: Daily = [
      { date: '2026-06-19', cost_usd: 10, turns: 4, by_model: { opus: 6, sonnet: 4 } },
      { date: '2026-06-20', cost_usd: 9, turns: 3, by_model: { opus: 3, sonnet: 6 } },
    ];
    const out = modelMixSeries(daily, models, 7);
    const byDate = new Map(out.map((d) => [d.date, d.fractions]));
    const d19 = byDate.get('2026-06-19')!;
    expect(d19.opus).toBeCloseTo(0.6, 12);
    expect(d19.sonnet).toBeCloseTo(0.4, 12);
    expect(d19.opus + d19.sonnet).toBeCloseTo(1, 12);
    const d20 = byDate.get('2026-06-20')!;
    expect(d20.opus + d20.sonnet).toBeCloseTo(1, 12);
  });

  it('zero-cost / gap days yield all-zero fractions', () => {
    const daily: Daily = [
      { date: '2026-06-20', cost_usd: 10, turns: 4, by_model: { opus: 10 } },
    ];
    const out = modelMixSeries(daily, models, 7);
    const gap = out.find((d) => d.date === '2026-06-19')!;
    expect(gap.fractions.opus).toBe(0);
    expect(gap.fractions.sonnet).toBe(0);
  });

  it('every day carries every requested model key', () => {
    const daily: Daily = [
      { date: '2026-06-20', cost_usd: 10, turns: 4, by_model: { opus: 10 } },
    ];
    const out = modelMixSeries(daily, models, 7);
    for (const d of out) {
      expect(Object.keys(d.fractions).sort()).toEqual([...models].sort());
    }
  });

  it('ignores models outside the requested set when normalizing', () => {
    const daily: Daily = [
      {
        date: '2026-06-20',
        cost_usd: 20,
        turns: 4,
        by_model: { opus: 5, sonnet: 5, haiku: 10 },
      },
    ];
    const out = modelMixSeries(daily, models, 7);
    const d = out.find((x) => x.date === '2026-06-20')!;
    // denom is opus+sonnet=10, NOT 20 → each 0.5
    expect(d.fractions.opus).toBeCloseTo(0.5, 12);
    expect(d.fractions.sonnet).toBeCloseTo(0.5, 12);
  });
});

// ---------------------------------------------------------------------------
// turnsPerSession (Phase 3b)
// ---------------------------------------------------------------------------
describe('turnsPerSession', () => {
  it('divides turns by sessions', () => {
    expect(turnsPerSession(member({ turns: 30, sessions: 6 }))).toBe(5);
  });
  it('guards sessions == 0 → 0', () => {
    expect(turnsPerSession(member({ turns: 30, sessions: 0 }))).toBe(0);
  });
  it('coerces absent counts to 0', () => {
    expect(turnsPerSession(member({}))).toBe(0);
  });
});

// ---------------------------------------------------------------------------
// teamInsight (Phase 3b) — owner one-line narrative
// ---------------------------------------------------------------------------
describe('teamInsight', () => {
  it('is fully null-safe on an empty roster', () => {
    const t = teamInsight([]);
    expect(t).toEqual({
      membersTotal: 0,
      membersActive: 0,
      topContributor: null,
      biggestMover: null,
      mostEfficient: null,
    });
  });

  it('counts total and active (turns > 0) members', () => {
    const t = teamInsight([
      member({ user_id: 'a', turns: 10 }),
      member({ user_id: 'b', turns: 0 }),
      member({ user_id: 'c', turns: 3 }),
    ]);
    expect(t.membersTotal).toBe(3);
    expect(t.membersActive).toBe(2);
  });

  it('topContributor is the highest cost_share', () => {
    const t = teamInsight([
      member({ user_id: 'a', display_name: 'A', cost_share: 0.2, turns: 5 }),
      member({ user_id: 'b', display_name: 'B', cost_share: 0.7, turns: 5 }),
      member({ user_id: 'c', display_name: 'C', cost_share: 0.1, turns: 5 }),
    ]);
    expect(t.topContributor).toEqual({ name: 'B', userId: 'b', share: 0.7 });
  });

  it('topContributor falls back to email then "(unknown)" for the name', () => {
    const t = teamInsight([
      member({ user_id: 'a', display_name: '', email: 'a@x', cost_share: 0.9, turns: 1 }),
    ]);
    expect(t.topContributor?.name).toBe('a@x');
  });

  it('biggestMover is the largest |Δ cost| among members with a finite delta', () => {
    const t = teamInsight([
      // +50% cost
      member({ user_id: 'a', display_name: 'A', cost_usd: 150, turns: 5, previous: { cost_usd: 100, turns: 5 } }),
      // -80% cost (bigger magnitude)
      member({ user_id: 'b', display_name: 'B', cost_usd: 20, turns: 5, previous: { cost_usd: 100, turns: 5 } }),
      // no previous — ineligible
      member({ user_id: 'c', display_name: 'C', cost_usd: 999, turns: 5 }),
    ]);
    expect(t.biggestMover?.userId).toBe('b');
    expect(t.biggestMover?.dir).toBe('down');
    expect(t.biggestMover?.pct).toBeCloseTo(-0.8, 12);
  });

  it('biggestMover is null when NO member has a finite delta (none have previous)', () => {
    const t = teamInsight([
      member({ user_id: 'a', cost_usd: 50, turns: 5 }),
      member({ user_id: 'b', cost_usd: 80, turns: 5 }),
    ]);
    expect(t.biggestMover).toBeNull();
  });

  it('biggestMover excludes "new" members (prev cost 0 → null pct) from the ranking', () => {
    const t = teamInsight([
      // "new" — prev cost 0 → pct null, not rankable on magnitude
      member({ user_id: 'new', cost_usd: 500, turns: 5, previous: { cost_usd: 0, turns: 0 } }),
      // a real +10% mover
      member({ user_id: 'real', display_name: 'Real', cost_usd: 110, turns: 5, previous: { cost_usd: 100, turns: 5 } }),
    ]);
    expect(t.biggestMover?.userId).toBe('real');
    expect(t.biggestMover?.pct).toBeCloseTo(0.1, 12);
  });

  it('mostEfficient is the lowest cost/turn among ACTIVE members (idle ignored)', () => {
    const t = teamInsight([
      member({ user_id: 'pricey', display_name: 'Pricey', cost_usd: 100, turns: 10 }), // 10/turn
      member({ user_id: 'cheap', display_name: 'Cheap', cost_usd: 10, turns: 10 }), //   1/turn
      member({ user_id: 'idle', display_name: 'Idle', cost_usd: 0, turns: 0 }), //       0 but IDLE
    ]);
    expect(t.mostEfficient?.userId).toBe('cheap');
    expect(t.mostEfficient?.costPerTurn).toBe(1);
  });

  it('mostEfficient prefers the server cost_per_turn_usd over the derived value', () => {
    const t = teamInsight([
      member({ user_id: 'a', cost_usd: 100, turns: 10, cost_per_turn_usd: 0.5 }), // server says 0.5
      member({ user_id: 'b', cost_usd: 10, turns: 10 }), // derived 1.0
    ]);
    // a wins on the server value (0.5 < 1.0) even though derived would be 10.
    expect(t.mostEfficient?.userId).toBe('a');
    expect(t.mostEfficient?.costPerTurn).toBe(0.5);
  });

  it('mostEfficient is null when no member is active', () => {
    const t = teamInsight([
      member({ user_id: 'a', cost_usd: 5, turns: 0 }),
      member({ user_id: 'b', cost_usd: 8, turns: 0 }),
    ]);
    expect(t.mostEfficient).toBeNull();
  });
});

// ---------------------------------------------------------------------------
// alignMemberDailies (Phase 3b) — shared spine + per-member overlay series
// ---------------------------------------------------------------------------
describe('alignMemberDailies', () => {
  beforeEach(() => {
    vi.useFakeTimers();
    vi.setSystemTime(FIXED_NOW);
  });
  afterEach(() => {
    vi.useRealTimers();
  });

  it('maps each member onto a shared spine with gap days = 0', () => {
    const rows = [
      member({
        user_id: 'a',
        display_name: 'A',
        turns: 5,
        daily: [
          { date: '2026-06-18', cost_usd: 5, turns: 2 },
          { date: '2026-06-20', cost_usd: 7, turns: 3 },
        ],
      }),
    ];
    const { dates, series } = alignMemberDailies(rows, 7, 'cost');
    // Spine is the 7-day window ending today (matches the daily chart's x-axis).
    expect(dates).toContain('2026-06-18');
    expect(dates).toContain('2026-06-19');
    expect(dates).toContain('2026-06-20');
    expect([...dates].sort((x, y) => x.localeCompare(y))).toEqual(dates); // ascending
    const a = series[0];
    const at = (d: string) => a.values[dates.indexOf(d)];
    expect(at('2026-06-18')).toBe(5);
    expect(at('2026-06-19')).toBe(0); // gap day filled with 0
    expect(at('2026-06-20')).toBe(7);
    expect(a.active).toBe(true);
  });

  it('unions member dates into the spine even outside the strict window', () => {
    // 2026-06-01 is well before the 7-day window; it must still appear.
    const rows = [
      member({
        user_id: 'a',
        turns: 1,
        daily: [{ date: '2026-06-01', cost_usd: 9, turns: 1 }],
      }),
    ];
    const { dates, series } = alignMemberDailies(rows, 7, 'cost');
    expect(dates).toContain('2026-06-01');
    expect(series[0].values[dates.indexOf('2026-06-01')]).toBe(9);
  });

  it('switches metric between cost and turns', () => {
    const rows = [
      member({
        user_id: 'a',
        turns: 5,
        daily: [{ date: '2026-06-20', cost_usd: 7, turns: 3 }],
      }),
    ];
    const cost = alignMemberDailies(rows, 7, 'cost');
    const turns = alignMemberDailies(rows, 7, 'turns');
    expect(cost.series[0].values[cost.dates.indexOf('2026-06-20')]).toBe(7);
    expect(turns.series[0].values[turns.dates.indexOf('2026-06-20')]).toBe(3);
  });

  it('defaults to the cost metric', () => {
    const rows = [
      member({ user_id: 'a', turns: 5, daily: [{ date: '2026-06-20', cost_usd: 7, turns: 3 }] }),
    ];
    const def = alignMemberDailies(rows, 7);
    expect(def.series[0].values[def.dates.indexOf('2026-06-20')]).toBe(7); // cost
  });

  it('a member with no daily → an all-zero series, still listed and not active', () => {
    const rows = [
      member({ user_id: 'a', display_name: 'A', turns: 0 }), // no daily
      member({
        user_id: 'b',
        display_name: 'B',
        turns: 5,
        daily: [{ date: '2026-06-20', cost_usd: 7, turns: 3 }],
      }),
    ];
    const { series } = alignMemberDailies(rows, 7, 'cost');
    expect(series.map((s) => s.userId).sort()).toEqual(['a', 'b']);
    const a = series.find((s) => s.userId === 'a')!;
    expect(a.values.every((v) => v === 0)).toBe(true);
    expect(a.active).toBe(false);
  });

  it('active flag derives from turns even for the cost metric', () => {
    // A member with $0 cost but real turns is still ACTIVE.
    const rows = [
      member({
        user_id: 'a',
        turns: 3,
        daily: [{ date: '2026-06-20', cost_usd: 0, turns: 3 }],
      }),
    ];
    const { series } = alignMemberDailies(rows, 7, 'cost');
    expect(series[0].active).toBe(true);
  });

  it('orders series by total DESC (stable on ties) for stable colors', () => {
    const rows = [
      member({ user_id: 'lo', turns: 1, daily: [{ date: '2026-06-20', cost_usd: 1, turns: 1 }] }),
      member({ user_id: 'hi', turns: 1, daily: [{ date: '2026-06-20', cost_usd: 9, turns: 1 }] }),
      member({ user_id: 'mid', turns: 1, daily: [{ date: '2026-06-20', cost_usd: 5, turns: 1 }] }),
    ];
    expect(alignMemberDailies(rows, 7, 'cost').series.map((s) => s.userId)).toEqual([
      'hi',
      'mid',
      'lo',
    ]);
  });

  it('honors `limit` by keeping the top-N by total and dropping the rest', () => {
    const rows = [
      member({ user_id: 'lo', turns: 1, daily: [{ date: '2026-06-20', cost_usd: 1, turns: 1 }] }),
      member({ user_id: 'hi', turns: 1, daily: [{ date: '2026-06-20', cost_usd: 9, turns: 1 }] }),
      member({ user_id: 'mid', turns: 1, daily: [{ date: '2026-06-20', cost_usd: 5, turns: 1 }] }),
    ];
    const { series } = alignMemberDailies(rows, 7, 'cost', 2);
    expect(series.map((s) => s.userId)).toEqual(['hi', 'mid']);
  });

  it('includes ALL members (even zero-total) by default', () => {
    const rows = [
      member({ user_id: 'z', turns: 0 }), // zero total, no daily
      member({ user_id: 'a', turns: 1, daily: [{ date: '2026-06-20', cost_usd: 5, turns: 1 }] }),
    ];
    expect(alignMemberDailies(rows, 7, 'cost').series.map((s) => s.userId).sort()).toEqual([
      'a',
      'z',
    ]);
  });

  it('returns an empty series list for an empty roster (spine still built)', () => {
    const { dates, series } = alignMemberDailies([], 7, 'cost');
    expect(series).toEqual([]);
    expect(dates.length).toBeGreaterThanOrEqual(7);
  });
});

// ---------------------------------------------------------------------------
// projectMembers (Phase 3c) — per-project member attribution mapping
// ---------------------------------------------------------------------------
type ProjectEntry = NonNullable<CCUsageSummary['top_projects']>[number];
function project(over: Partial<ProjectEntry>): ProjectEntry {
  return {
    project: over.project ?? 'webapp',
    cost_usd: over.cost_usd ?? 0,
    turns: over.turns ?? 0,
    cost_share: over.cost_share ?? 0,
    ...over,
  };
}

describe('projectMembers', () => {
  // A deterministic caller color map so the test asserts colors are passed
  // through from `colorFor` (the helper never owns the palette).
  const colorFor = (userId: string) => `color:${userId}`;

  it('maps by_member to color-resolved segments and sorts by share DESC', () => {
    const entry = project({
      by_member: [
        { user_id: 'b', display_name: 'Bob', cost_usd: 20, turns: 8, cost_share: 0.2 },
        { user_id: 'a', display_name: 'Alice', cost_usd: 70, turns: 30, cost_share: 0.7 },
        { user_id: 'c', display_name: 'Carol', cost_usd: 10, turns: 4, cost_share: 0.1 },
      ],
    });
    const segs = projectMembers(entry, colorFor);
    // Sorted by share DESC regardless of incoming order.
    expect(segs.map((s) => s.userId)).toEqual(['a', 'b', 'c']);
    expect(segs.map((s) => s.name)).toEqual(['Alice', 'Bob', 'Carol']);
    // Color comes from the caller's map (not owned by the helper).
    expect(segs.map((s) => s.color)).toEqual(['color:a', 'color:b', 'color:c']);
    // Raw cost/turns + project-local share pass through.
    expect(segs[0]).toMatchObject({ costUsd: 70, turns: 30, share: 0.7 });
    expect(segs[1].share).toBeCloseTo(0.2, 12);
  });

  it('passes the share field through untouched (no re-derivation)', () => {
    // Even a drifted/odd cost_share is reflected as-is — the server owns it.
    const entry = project({
      by_member: [
        { user_id: 'a', display_name: 'A', cost_usd: 5, turns: 1, cost_share: 0.42 },
      ],
    });
    expect(projectMembers(entry, colorFor)[0].share).toBe(0.42);
  });

  it('flags the caller via isYou when youId matches (else undefined)', () => {
    const entry = project({
      by_member: [
        { user_id: 'a', display_name: 'Alice', cost_usd: 60, turns: 10, cost_share: 0.6 },
        { user_id: 'me', display_name: 'Me', cost_usd: 40, turns: 5, cost_share: 0.4 },
      ],
    });
    const segs = projectMembers(entry, colorFor, 'me');
    expect(segs.find((s) => s.userId === 'me')?.isYou).toBe(true);
    // Non-matching members carry no isYou flag.
    expect(segs.find((s) => s.userId === 'a')?.isYou).toBeUndefined();
  });

  it('does not flag isYou when no youId is given', () => {
    const entry = project({
      by_member: [{ user_id: 'a', display_name: 'A', cost_usd: 1, turns: 1, cost_share: 1 }],
    });
    expect(projectMembers(entry, colorFor)[0].isYou).toBeUndefined();
  });

  it('falls back to "(unknown)" when display_name is empty', () => {
    const entry = project({
      by_member: [{ user_id: 'a', display_name: '', cost_usd: 1, turns: 1, cost_share: 1 }],
    });
    expect(projectMembers(entry, colorFor)[0].name).toBe('(unknown)');
  });

  it('returns [] when by_member is absent (solo / member / non-owner response)', () => {
    expect(projectMembers(project({}), colorFor)).toEqual([]);
  });

  it('returns [] when by_member is present but empty', () => {
    expect(projectMembers(project({ by_member: [] }), colorFor)).toEqual([]);
  });

  it('coerces absent/NaN cost & turns to 0', () => {
    const entry = project({
      by_member: [
        // cost_usd/turns deliberately non-finite to exercise the num() guard.
        { user_id: 'a', display_name: 'A', cost_usd: NaN, turns: NaN, cost_share: 0.5 },
      ],
    });
    const seg = projectMembers(entry, colorFor)[0];
    expect(seg.costUsd).toBe(0);
    expect(seg.turns).toBe(0);
  });
});

// ---------------------------------------------------------------------------
// periodDigest (Phase 4a) — owner "this period vs prior" synthesized digest
// ---------------------------------------------------------------------------

// Minimal CCUsageTeamSummary carrying only the fields periodDigest reads (hero,
// previous, by_member, window_days). The rest of the contract is filled with
// inert defaults so the cast is honest.
function teamSummary(over: {
  windowDays?: number;
  hero?: Partial<CCUsageSummary['hero']>;
  previous?: CCUsageSummary['previous'];
  byMember?: MemberRow[];
}): CCUsageTeamSummary {
  const hero = {
    cost_usd: 0,
    turns: 0,
    sessions: 0,
    active_days: 0,
    fresh_tokens: 0,
    cached_tokens: 0,
    models_used: 0,
    cache_hit_rate: 0,
    ...over.hero,
  };
  return {
    window: `${over.windowDays ?? 30}d`,
    window_days: over.windowDays ?? 30,
    generated_at: '2026-06-20T00:00:00Z',
    currency: 'USD',
    notional: true,
    hero,
    by_model: [],
    top_projects: [],
    daily: [],
    in_out: { in_app: { turns: 0, cost_usd: 0 }, out_app: { turns: 0, cost_usd: 0 } },
    subagent_share: { cost_usd: 0, cost_share: 0 },
    previous: over.previous,
    team: null,
    teams: [],
    by_member: (over.byMember ?? []) as CCUsageTeamSummary['by_member'],
  } as CCUsageTeamSummary;
}

// A team-wide prior hero wrapper ({window, hero}) with the keys heroDeltas reads.
function prevHero(
  over: Partial<CCUsageSummary['hero']>,
): NonNullable<CCUsageSummary['previous']> {
  return {
    window: '30d',
    hero: {
      cost_usd: 0,
      turns: 0,
      sessions: 0,
      active_days: 0,
      fresh_tokens: 0,
      cached_tokens: 0,
      cache_hit_rate: 0,
      ...over,
    },
  };
}

describe('periodDigest', () => {
  it('returns null when there is no by_member roster', () => {
    expect(periodDigest(teamSummary({ byMember: [], previous: prevHero({ cost_usd: 1 }) }))).toBeNull();
  });

  it('returns null when there is NO comparison basis (no team prev AND no member prev)', () => {
    const d = periodDigest(
      teamSummary({
        previous: null,
        byMember: [member({ user_id: 'a', cost_usd: 50, turns: 5 })], // no member.previous
      }),
    );
    expect(d).toBeNull();
  });

  it('returns a digest when there is at least a per-member previous (no team prev)', () => {
    const d = periodDigest(
      teamSummary({
        previous: null,
        byMember: [
          member({ user_id: 'a', cost_usd: 110, turns: 5, previous: { cost_usd: 100, turns: 5 } }),
        ],
      }),
    );
    expect(d).not.toBeNull();
  });

  it('returns null for the "all" window even with prior data (no prior period)', () => {
    const d = periodDigest(
      teamSummary({
        windowDays: 0,
        previous: prevHero({ cost_usd: 100 }),
        byMember: [member({ user_id: 'a', cost_usd: 50, turns: 5, previous: { cost_usd: 40, turns: 4 } })],
      }),
    );
    expect(d).toBeNull();
  });

  it('windowLabel reads "week" for 7, "<n> days" otherwise', () => {
    const base = {
      previous: prevHero({ cost_usd: 100 }),
      byMember: [member({ user_id: 'a', cost_usd: 110, turns: 5, previous: { cost_usd: 100, turns: 5 } })],
    };
    expect(periodDigest(teamSummary({ ...base, windowDays: 7 }))?.windowLabel).toBe('week');
    expect(periodDigest(teamSummary({ ...base, windowDays: 30 }))?.windowLabel).toBe('30 days');
    expect(periodDigest(teamSummary({ ...base, windowDays: 90 }))?.windowLabel).toBe('90 days');
  });

  it('reuses heroDeltas for cost/turns/active_days/cache_hit_rate (sign + magnitude)', () => {
    const d = periodDigest(
      teamSummary({
        windowDays: 30,
        hero: { cost_usd: 112, turns: 200, active_days: 20, cache_hit_rate: 0.8 },
        previous: prevHero({ cost_usd: 100, turns: 250, active_days: 20, cache_hit_rate: 0.5 }),
        byMember: [member({ user_id: 'a', cost_usd: 112, turns: 200, previous: { cost_usd: 100, turns: 250 } })],
      }),
    )!;
    expect(d.heroDeltas.cost_usd.dir).toBe('up');
    expect(d.heroDeltas.cost_usd.pct).toBeCloseTo(0.12, 12);
    expect(d.heroDeltas.turns.dir).toBe('down');
    expect(d.heroDeltas.active_days.dir).toBe('flat');
    expect(d.heroDeltas.cache_hit_rate.pct).toBeCloseTo(0.6, 12);
  });

  it('ranks moversUp by pct DESC (steepest ramp first) and only dir up', () => {
    const d = periodDigest(
      teamSummary({
        previous: prevHero({ cost_usd: 100 }),
        byMember: [
          member({ user_id: 'small', display_name: 'Small', cost_usd: 110, turns: 5, previous: { cost_usd: 100, turns: 5 } }), // +10%
          member({ user_id: 'big', display_name: 'Big', cost_usd: 200, turns: 5, previous: { cost_usd: 100, turns: 5 } }),    // +100%
          member({ user_id: 'cool', display_name: 'Cool', cost_usd: 80, turns: 5, previous: { cost_usd: 100, turns: 5 } }),   // -20% (down)
        ],
      }),
    )!;
    expect(d.moversUp.map((m) => m.userId)).toEqual(['big', 'small']);
    expect(d.moversUp.every((m) => m.dir === 'up')).toBe(true);
  });

  it('ranks moversDown by pct ASC (most-negative first) and only dir down', () => {
    const d = periodDigest(
      teamSummary({
        previous: prevHero({ cost_usd: 100 }),
        byMember: [
          member({ user_id: 'mild', display_name: 'Mild', cost_usd: 90, turns: 5, previous: { cost_usd: 100, turns: 5 } }),  // -10%
          member({ user_id: 'steep', display_name: 'Steep', cost_usd: 20, turns: 5, previous: { cost_usd: 100, turns: 5 } }), // -80%
          member({ user_id: 'up', display_name: 'Up', cost_usd: 150, turns: 5, previous: { cost_usd: 100, turns: 5 } }),      // +50% (up)
        ],
      }),
    )!;
    expect(d.moversDown.map((m) => m.userId)).toEqual(['steep', 'mild']);
    expect(d.moversDown.every((m) => m.dir === 'down')).toBe(true);
  });

  it('caps each mover list at N=3', () => {
    const rows = ['a', 'b', 'c', 'd', 'e'].map((id, i) =>
      member({ user_id: id, cost_usd: 100 + (i + 1) * 10, turns: 5, previous: { cost_usd: 100, turns: 5 } }),
    );
    const d = periodDigest(teamSummary({ previous: prevHero({ cost_usd: 100 }), byMember: rows }))!;
    expect(d.moversUp.length).toBe(3);
  });

  it('excludes members with NO previous from both mover lists', () => {
    const d = periodDigest(
      teamSummary({
        previous: prevHero({ cost_usd: 100 }),
        byMember: [
          member({ user_id: 'has', cost_usd: 150, turns: 5, previous: { cost_usd: 100, turns: 5 } }),
          member({ user_id: 'none', cost_usd: 999, turns: 5 }), // no previous → excluded
        ],
      }),
    )!;
    const ids = [...d.moversUp, ...d.moversDown].map((m) => m.userId);
    expect(ids).not.toContain('none');
    expect(ids).toContain('has');
  });

  it('surfaces active no-previous (or prev cost 0) members as newcomers, not movers', () => {
    const d = periodDigest(
      teamSummary({
        previous: prevHero({ cost_usd: 100 }),
        byMember: [
          // active, prev cost 0 → null pct → newcomer (not a mover)
          member({ user_id: 'fresh', display_name: 'Fresh', cost_usd: 500, turns: 8, previous: { cost_usd: 0, turns: 0 } }),
          // active, no previous at all → newcomer
          member({ user_id: 'noprev', display_name: 'NoPrev', cost_usd: 30, turns: 2 }),
          // idle no-previous → NOT a newcomer (turns 0)
          member({ user_id: 'idle', cost_usd: 0, turns: 0 }),
          // a real mover so the digest still has a basis
          member({ user_id: 'real', cost_usd: 110, turns: 5, previous: { cost_usd: 100, turns: 5 } }),
        ],
      }),
    )!;
    expect(d.newcomers.map((n) => n.userId).sort()).toEqual(['fresh', 'noprev']);
    const moverIds = [...d.moversUp, ...d.moversDown].map((m) => m.userId);
    expect(moverIds).not.toContain('fresh');
    expect(moverIds).not.toContain('noprev');
  });

  it('reuses teamInsight for topContributor / mostEfficient / member counts', () => {
    const d = periodDigest(
      teamSummary({
        previous: prevHero({ cost_usd: 100 }),
        byMember: [
          member({ user_id: 'top', display_name: 'Top', cost_usd: 200, cost_share: 0.7, turns: 10, previous: { cost_usd: 100, turns: 5 } }),
          member({ user_id: 'eff', display_name: 'Eff', cost_usd: 10, cost_share: 0.05, turns: 100, previous: { cost_usd: 8, turns: 80 } }),
          member({ user_id: 'idle', display_name: 'Idle', cost_usd: 0, turns: 0 }),
        ],
      }),
    )!;
    expect(d.topContributor?.userId).toBe('top');
    expect(d.mostEfficient?.userId).toBe('eff');
    expect(d.membersTotal).toBe(3);
    expect(d.membersActive).toBe(2);
  });

  it('marks the caller own row with isYou on movers', () => {
    const d = periodDigest(
      teamSummary({
        previous: prevHero({ cost_usd: 100 }),
        byMember: [
          member({ user_id: 'me', is_you: true, cost_usd: 150, turns: 5, previous: { cost_usd: 100, turns: 5 } }),
        ],
      }),
    )!;
    expect(d.moversUp[0].isYou).toBe(true);
  });

  it('assembles a headline with spend Δ, active count, and accelerating/cooling names', () => {
    const d = periodDigest(
      teamSummary({
        windowDays: 30,
        hero: { cost_usd: 112, turns: 200 },
        previous: prevHero({ cost_usd: 100 }),
        byMember: [
          member({ user_id: 'a', display_name: 'Eli', cost_usd: 200, turns: 50, previous: { cost_usd: 100, turns: 30 } }), // up
          member({ user_id: 'b', display_name: 'Dana', cost_usd: 40, turns: 20, previous: { cost_usd: 100, turns: 30 } }), // down
          member({ user_id: 'c', display_name: 'Idle', cost_usd: 0, turns: 0 }),
        ],
      }),
    )!;
    expect(d.headline).toBe('Team spend up 12% vs the prior 30 days · 2 of 3 active · Eli accelerating, Dana cooling');
  });

  it('headline reads "flat" when team spend is unchanged and omits absent mover clauses', () => {
    const d = periodDigest(
      teamSummary({
        windowDays: 7,
        hero: { cost_usd: 100 },
        previous: prevHero({ cost_usd: 100 }), // flat
        byMember: [
          // a flat member → not up, not down → no accelerating/cooling clause
          member({ user_id: 'a', display_name: 'A', cost_usd: 100, turns: 5, previous: { cost_usd: 100, turns: 5 } }),
        ],
      }),
    )!;
    expect(d.headline).toBe('Team spend flat vs the prior week · 1 of 1 active');
  });

  it('headline says spend is "new" when there is no prior team spend to divide by', () => {
    const d = periodDigest(
      teamSummary({
        windowDays: 30,
        hero: { cost_usd: 200 },
        previous: prevHero({ cost_usd: 0 }), // no prior team spend → null pct
        byMember: [
          member({ user_id: 'a', display_name: 'A', cost_usd: 150, turns: 5, previous: { cost_usd: 100, turns: 5 } }),
        ],
      }),
    )!;
    expect(d.headline).toContain('Team spend new this 30 days');
  });
});

// ---------------------------------------------------------------------------
// Codex provider helpers
// ---------------------------------------------------------------------------
describe('provider + model helpers', () => {
  it('normalizes provider fields (empty/unknown → claude; openai → codex)', () => {
    expect(normalizeProvider(undefined)).toBe('claude');
    expect(normalizeProvider('')).toBe('claude');
    expect(normalizeProvider('claude')).toBe('claude');
    expect(normalizeProvider('anthropic')).toBe('claude');
    expect(normalizeProvider('codex')).toBe('codex');
    expect(normalizeProvider('OpenAI')).toBe('codex');
    expect(providerLabel('all')).toBe('All');
    expect(providerLabel('claude')).toBe('Claude Code');
    expect(providerLabel('codex')).toBe('Codex');
  });

  it('detects OpenAI/Codex model ids and tier keys', () => {
    for (const m of ['gpt-6-astra', 'gpt-6.1-sol', 'gpt-5.6-luna', 'gpt-5.3-codex', 'claude-gpt-6-astra', 'GPT-6-Luna-fast', 'astra', 'o4-mini']) {
      expect(isCodexModel(m)).toBe(true);
    }
    for (const m of ['claude-opus-4-6', 'claude-sonnet-4-5', 'haiku', 'fable', 'opus', '', undefined]) {
      expect(isCodexModel(m)).toBe(false);
    }
  });

  it('prefers the row provider field over model sniffing', () => {
    expect(modelProvider('gpt-6-astra')).toBe('codex');
    expect(modelProvider('claude-opus-4-6')).toBe('claude');
    // A Claude-Code-proxied GPT model was produced by Claude Code.
    expect(modelProvider('claude-gpt-6-astra', 'claude')).toBe('claude');
    expect(modelProvider('mystery', 'codex')).toBe('codex');
  });

  it('modelTier understands Claude and OpenAI families', () => {
    expect(modelTier('claude-opus-4-6')).toBe('opus');
    expect(modelTier('claude-sonnet-4-5')).toBe('sonnet');
    expect(modelTier('claude-haiku-4')).toBe('haiku');
    expect(modelTier('fable-1')).toBe('fable');
    expect(modelTier('gpt-6-astra')).toBe('astra');
    expect(modelTier('gpt-6.1-sol-fast')).toBe('sol');
    expect(modelTier('gpt-5.6-terra')).toBe('terra');
    expect(modelTier('gpt-5.6-luna')).toBe('luna');
    expect(modelTier('gpt-5.3-codex')).toBe('codex');
    expect(modelTier('gpt-7')).toBe('gpt');
    expect(modelTier('mystery-model')).toBe('?');
    expect(modelTier(undefined)).toBe('?');
  });

  it('modelLabel renders readable names', () => {
    expect(modelLabel('gpt-6-astra')).toBe('GPT-6 Astra');
    expect(modelLabel('gpt-6.1-sol')).toBe('GPT-6.1 Sol');
    expect(modelLabel('gpt-5.6-luna-fast')).toBe('GPT-5.6 Luna (fast)');
    expect(modelLabel('gpt-5.3-codex')).toBe('GPT-5.3 Codex');
    expect(modelLabel('claude-gpt-6-astra', 'claude')).toBe('GPT-6 Astra');
    expect(modelLabel('claude-opus-4-6')).toBe('Opus');
    expect(modelLabel('mystery')).toBe('mystery');
  });

  it('modelColorMap keys colors by model name, Claude vs Codex ramps, no collisions', () => {
    const a = modelColorMap([
      { model: 'claude-opus-4-6' },
      { model: 'gpt-6-astra', provider: 'codex' },
      { model: 'claude-sonnet-4-5' },
    ]);
    // Order-independent: the same set in a different (cost) order → same colors.
    const b = modelColorMap([
      { model: 'claude-sonnet-4-5' },
      { model: 'gpt-6-astra', provider: 'codex' },
      { model: 'claude-opus-4-6' },
    ]);
    expect([...a.entries()].sort()).toEqual([...b.entries()].sort());
    expect(CLAUDE_MODEL_RAMP).toContain(a.get('claude-opus-4-6'));
    expect(CLAUDE_MODEL_RAMP).toContain(a.get('claude-sonnet-4-5'));
    expect(CODEX_MODEL_RAMP).toContain(a.get('gpt-6-astra'));
    // Two opus versions don't share a hue.
    const c = modelColorMap([{ model: 'claude-opus-4-5' }, { model: 'claude-opus-4-6' }]);
    expect(c.get('claude-opus-4-5')).not.toBe(c.get('claude-opus-4-6'));
  });
});

describe('providerRows (by_provider share math)', () => {
  const row = (over: Partial<NonNullable<CCUsageSummary['by_provider']>[number]>) => ({
    provider: 'claude',
    cost_usd: 0,
    cost_share: 0,
    turns: 0,
    sessions: 0,
    input_tokens: 0,
    output_tokens: 0,
    cache_read_tokens: 0,
    cache_create_tokens: 0,
    ...over,
  });

  it('returns null for absent/empty by_provider', () => {
    expect(providerRows(undefined)).toBeNull();
    expect(providerRows([])).toBeNull();
  });

  it('uses the server cost_share and splits fresh/cached/output/reasoning', () => {
    const out = providerRows([
      row({ provider: 'claude', cost_usd: 75, cost_share: 0.75, input_tokens: 10, cache_create_tokens: 5, cache_read_tokens: 100, output_tokens: 20 }),
      row({ provider: 'codex', cost_usd: 25, cost_share: 0.25, output_tokens: 40, reasoning_output_tokens: 12 }),
    ])!;
    expect(out.map((r) => r.label)).toEqual(['Claude Code', 'Codex']);
    expect(out[0].share).toBe(0.75);
    expect(out[0].fresh_tokens).toBe(15);
    expect(out[0].cached_tokens).toBe(100);
    expect(out[1].output_tokens).toBe(40);
    expect(out[1].reasoning_tokens).toBe(12);
  });

  it('re-derives the share from costs when cost_share is missing/NaN', () => {
    const out = providerRows([
      row({ provider: 'claude', cost_usd: 30, cost_share: Number.NaN }),
      row({ provider: 'codex', cost_usd: 10, cost_share: undefined as unknown as number }),
    ])!;
    expect(out[0].share).toBeCloseTo(0.75, 12);
    expect(out[1].share).toBeCloseTo(0.25, 12);
  });

  it('shares are 0 (not NaN) when every cost is 0 and no share is sent', () => {
    const out = providerRows([row({ cost_share: Number.NaN }), row({ provider: 'codex', cost_share: Number.NaN })])!;
    expect(out.every((r) => r.share === 0)).toBe(true);
  });
});

describe('memberProviderMix', () => {
  it('reports the Codex cost share', () => {
    const m = member({
      by_provider: [
        { provider: 'claude', cost_usd: 68, turns: 10 },
        { provider: 'codex', cost_usd: 32, turns: 90 },
      ],
    });
    expect(memberProviderMix(m)).toBe('Codex 32%');
  });

  it('falls back to turns when there is no cost, and never rounds a real share to 0%', () => {
    expect(
      memberProviderMix(member({ by_provider: [{ provider: 'codex', cost_usd: 0, turns: 4 }, { provider: 'claude', cost_usd: 0, turns: 4 }] })),
    ).toBe('Codex 50%');
    expect(
      memberProviderMix(member({ by_provider: [{ provider: 'claude', cost_usd: 999, turns: 1 }, { provider: 'codex', cost_usd: 1, turns: 1 }] })),
    ).toBe('Codex 1%');
  });

  it('is null for Claude-only members and absent data', () => {
    expect(memberProviderMix(member({}))).toBeNull();
    expect(memberProviderMix(member({ by_provider: [{ provider: 'claude', cost_usd: 5, turns: 1 }] }))).toBeNull();
  });
});

describe('lastActiveDate / relativeDay', () => {
  it('picks the latest day with turns or cost', () => {
    const m = member({
      daily: [
        { date: '2026-06-10', cost_usd: 1, turns: 1 },
        { date: '2026-06-15', cost_usd: 0, turns: 0 },
        { date: '2026-06-12', cost_usd: 0, turns: 2 },
      ],
    });
    expect(lastActiveDate(m)).toBe('2026-06-12');
  });

  it('distinguishes no daily (undefined) from no active day (null)', () => {
    expect(lastActiveDate(member({}))).toBeUndefined();
    expect(lastActiveDate(member({ daily: [] }))).toBeNull();
    expect(lastActiveDate(member({ daily: [{ date: '2026-06-10', cost_usd: 0, turns: 0 }] }))).toBeNull();
  });

  it('renders today / Nd ago in UTC days', () => {
    const now = new Date('2026-06-20T19:00:00Z');
    expect(relativeDay('2026-06-20', now)).toBe('today');
    expect(relativeDay('2026-06-21', now)).toBe('today'); // clock skew
    expect(relativeDay('2026-06-19', now)).toBe('1d ago');
    expect(relativeDay('2026-06-17', now)).toBe('3d ago');
    expect(relativeDay('not-a-date', now)).toBe('—');
  });
});

describe('accountRows provider', () => {
  it('carries the normalized provider (absent → claude)', () => {
    const rows = accountRows([
      { account: 'me@x', turns: 1, input_tokens: 0, output_tokens: 0, cache_read_tokens: 0, cache_create_tokens: 0, cost_usd: 1, cost_per_turn_usd: 1, cost_share: 0.5 },
      { account: 'me@x', provider: 'codex', turns: 1, input_tokens: 0, output_tokens: 0, cache_read_tokens: 0, cache_create_tokens: 0, cost_usd: 1, cost_per_turn_usd: 1, cost_share: 0.5 },
    ])!;
    expect(rows.map((r) => r.provider)).toEqual(['claude', 'codex']);
  });
});

describe('modelColorMap provider isolation', () => {
  it('keeps a Codex model hue when a crowded Claude set is added', () => {
    const codexOnly = modelColorMap([{ model: 'gpt-6-astra', provider: 'codex' }]);
    const mixed = modelColorMap([
      { model: 'claude-opus-4-8', provider: 'claude' },
      { model: 'claude-opus-5-5', provider: 'claude' },
      { model: 'claude-opus-5', provider: 'claude' },
      { model: 'claude-sonnet-5', provider: 'claude' },
      { model: 'claude-haiku-4-5', provider: 'claude' },
      { model: 'gpt-6-astra', provider: 'codex' },
    ]);
    expect(mixed.get('gpt-6-astra')).toBe(codexOnly.get('gpt-6-astra'));
  });
});

// ---------------------------------------------------------------------------
// Local-day grouping: a response that echoes `tz` carries calendar days in that
// zone (docs/USAGE-CODEX-PROVIDER-CONTRACT.md §"Local-day grouping"). Explicit
// zones + an injected `now` keep these independent of the runner's own TZ.
// ---------------------------------------------------------------------------
describe('Local-day grouping (tz echo)', () => {
  // 2026-06-20T19:00Z = 2026-06-21 03:00 in Singapore, 12:00 in Los Angeles.
  const NOW = new Date('2026-06-20T19:00:00Z');

  it('usageDayKey: UTC without tz, the zone calendar with tz', () => {
    expect(usageDayKey(NOW)).toBe('2026-06-20');
    expect(usageDayKey(NOW, 'Asia/Singapore')).toBe('2026-06-21');
    expect(usageDayKey(new Date('2026-06-21T03:00:00Z'), 'America/Los_Angeles')).toBe('2026-06-20');
    expect(usageDayKey(new Date('2026-06-21T03:00:00Z'))).toBe('2026-06-21');
  });

  it('an unresolvable zone falls back to the browser-local calendar', () => {
    const local = `${NOW.getFullYear()}-${String(NOW.getMonth() + 1).padStart(2, '0')}-${String(NOW.getDate()).padStart(2, '0')}`;
    expect(usageDayKey(NOW, 'Not/AZone')).toBe(local);
  });

  it('zeroFilledDaily ends the spine on the LOCAL today when tz is echoed', () => {
    const local = zeroFilledDaily([], 7, 'Asia/Singapore', NOW).map((d) => d.date);
    expect(local).toEqual([
      '2026-06-15', '2026-06-16', '2026-06-17', '2026-06-18', '2026-06-19', '2026-06-20', '2026-06-21',
    ]);
    // Without the echo the spine is exactly the old UTC one.
    const utc = zeroFilledDaily([], 7, undefined, NOW).map((d) => d.date);
    expect(utc).toEqual([
      '2026-06-14', '2026-06-15', '2026-06-16', '2026-06-17', '2026-06-18', '2026-06-19', '2026-06-20',
    ]);
  });

  it('still unions every server-returned date into a local spine', () => {
    const daily: Daily = [
      { date: '2026-06-14', cost_usd: 3, turns: 1 }, // partial boundary day
      { date: '2026-06-21', cost_usd: 5, turns: 2 }, // local today
    ];
    const out = zeroFilledDaily(daily, 7, 'Asia/Singapore', NOW);
    expect(out[0]).toMatchObject({ date: '2026-06-14', cost_usd: 3 });
    expect(out[out.length - 1]).toMatchObject({ date: '2026-06-21', cost_usd: 5 });
    expect(out.length).toBe(8);
    expect(out.reduce((s, d) => s + d.cost_usd, 0)).toBe(8);
  });

  it('is DST-safe: contiguous, no skipped or repeated day across spring-forward', () => {
    // US DST began 2026-03-08 02:00 local. A 25h/23h day must not collapse or
    // duplicate a date in the spine.
    const now = new Date('2026-03-10T14:00:00Z'); // 10:00 EDT on 03-10
    const dates = zeroFilledDaily([], 5, 'America/New_York', now).map((d) => d.date);
    expect(dates).toEqual(['2026-03-06', '2026-03-07', '2026-03-08', '2026-03-09', '2026-03-10']);
  });

  it('is DST-safe across fall-back late in the local evening', () => {
    // US DST ended 2026-11-01. 23:30 EST on 11-02 is already 11-03 in UTC.
    const now = new Date('2026-11-03T04:30:00Z');
    const dates = zeroFilledDaily([], 4, 'America/New_York', now).map((d) => d.date);
    expect(dates).toEqual(['2026-10-30', '2026-10-31', '2026-11-01', '2026-11-02']);
  });

  it('cumulative / model-mix / member alignment share the local spine', () => {
    const daily: Daily = [{ date: '2026-06-21', cost_usd: 2, turns: 1 }];
    const cum = cumulativeSeries(daily, 7, 'Asia/Singapore', NOW);
    expect(cum[cum.length - 1]).toEqual({ date: '2026-06-21', value: 2 });
    expect(cum.length).toBe(7);
    const mix = modelMixSeries(daily, ['m'], 7, 'Asia/Singapore', NOW);
    expect(mix[mix.length - 1]!.date).toBe('2026-06-21');
    const aligned = alignMemberDailies(
      [member({ user_id: 'a', daily: [{ date: '2026-06-21', cost_usd: 4, turns: 1 }] })],
      7,
      'cost',
      undefined,
      'Asia/Singapore',
      NOW,
    );
    expect(aligned.dates[aligned.dates.length - 1]).toBe('2026-06-21');
    expect(aligned.dates.length).toBe(7);
    expect(aligned.series[0]!.values[6]).toBe(4);
  });

  it('relativeDay compares against the echoed zone\'s today', () => {
    // Local (SGT) today is 06-21, so the member's last local day 06-21 is today
    // and 06-20 is yesterday — under UTC 06-20 would read "today".
    expect(relativeDay('2026-06-21', NOW, 'Asia/Singapore')).toBe('today');
    expect(relativeDay('2026-06-20', NOW, 'Asia/Singapore')).toBe('1d ago');
    expect(relativeDay('2026-06-18', NOW, 'Asia/Singapore')).toBe('3d ago');
    expect(relativeDay('2026-06-20', NOW)).toBe('today');
    // DST week: still whole calendar days.
    const dst = new Date('2026-03-10T14:00:00Z');
    expect(relativeDay('2026-03-07', dst, 'America/New_York')).toBe('3d ago');
  });

  it('heatmap: tz echo → no rotation; no echo → browser offset', () => {
    expect(activityOffsetMinutes('Asia/Singapore', NOW)).toBe(0);
    expect(activityOffsetMinutes(undefined, NOW)).toBe(-NOW.getTimezoneOffset());
    // Cells already local stay exactly where the server put them.
    const { matrix, max } = activityMatrix(
      [{ dow: 0, hour: 23, turns: 5, output_tokens: 0 }, { dow: 6, hour: 0, turns: 2, output_tokens: 0 }],
      activityOffsetMinutes('Asia/Singapore', NOW),
    );
    expect(matrix[0]![23]).toBe(5);
    expect(matrix[6]![0]).toBe(2);
    expect(max).toBe(5);
  });
});
