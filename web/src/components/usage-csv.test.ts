import { describe, it, expect } from 'vitest';
import { buildUsageCsv, buildTeamCsv, csvCell } from './UsagePage';
import type { CCUsageSummary, CCUsageTeamSummary } from '../api';

// Minimal valid CCUsageSummary — only the fields the CSV builders read are
// meaningful; the rest are filled to satisfy the type. `over` lets each test
// override the few relevant slices (by_model / daily / by_member).
function summary(over: Partial<CCUsageSummary> = {}): CCUsageSummary {
  return {
    window: '30d',
    window_days: 30,
    generated_at: '2026-06-24T00:00:00Z',
    currency: 'USD',
    notional: true,
    hero: {
      cost_usd: 0,
      turns: 0,
      sessions: 0,
      active_days: 0,
      fresh_tokens: 0,
      cached_tokens: 0,
      models_used: 0,
    },
    by_model: [],
    top_projects: [],
    daily: [],
    in_out: { in_app: { turns: 0, cost_usd: 0 }, out_app: { turns: 0, cost_usd: 0 } },
    subagent_share: { cost_usd: 0, cost_share: 0 },
    ...over,
  };
}

// Split a CSV blob into labelled sheets keyed by their "# Header" line.
function sheets(csv: string): Record<string, string[]> {
  const out: Record<string, string[]> = {};
  let cur: string | null = null;
  for (const line of csv.split('\r\n')) {
    if (line.startsWith('# ')) {
      cur = line.slice(2);
      out[cur] = [];
    } else if (cur && line !== '') {
      out[cur].push(line);
    }
  }
  return out;
}

describe('buildUsageCsv — Daily by model sheet (Phase 3a)', () => {
  it('emits a "Daily by model" sheet from daily[].by_model, (date,model,cost) rows', () => {
    const data = summary({
      daily: [
        { date: '2026-06-20', cost_usd: 9, turns: 3, by_model: { opus: 6, sonnet: 3 } },
        { date: '2026-06-21', cost_usd: 4, turns: 1, by_model: { haiku: 4 } },
      ],
    });
    const sh = sheets(buildUsageCsv(data));
    expect(sh['Daily by model']).toBeDefined();
    // Header + one row per (date, model). Models emitted cost-DESC within a day.
    expect(sh['Daily by model'][0]).toBe('date,model,cost_usd');
    expect(sh['Daily by model'].slice(1)).toEqual([
      '2026-06-20,opus,6',
      '2026-06-20,sonnet,3',
      '2026-06-21,haiku,4',
    ]);
  });

  it('orders a day\'s models cost-DESC (stable, readable)', () => {
    const data = summary({
      daily: [{ date: '2026-06-20', cost_usd: 10, turns: 5, by_model: { a: 1, b: 7, c: 2 } }],
    });
    const sh = sheets(buildUsageCsv(data));
    expect(sh['Daily by model'].slice(1)).toEqual([
      '2026-06-20,b,7',
      '2026-06-20,c,2',
      '2026-06-20,a,1',
    ]);
  });

  it('OMITS the sheet entirely when no day carries by_model (older response — byte-stable)', () => {
    const data = summary({
      daily: [
        { date: '2026-06-20', cost_usd: 9, turns: 3 },
        { date: '2026-06-21', cost_usd: 4, turns: 1 },
      ],
    });
    const csv = buildUsageCsv(data);
    expect(csv).not.toContain('# Daily by model');
    // The original Daily sheet is unchanged.
    const sh = sheets(csv);
    expect(sh['Daily']).toEqual(['date,cost_usd,turns', '2026-06-20,9,3', '2026-06-21,4,1']);
  });

  it('keeps the existing By model + Daily sheets byte-stable (new sheet only appended)', () => {
    const noModel = summary({
      by_model: [
        {
          model: 'claude-opus', tier: 'opus', turns: 5,
          input_tokens: 10, output_tokens: 20, cache_read_tokens: 30, cache_create_tokens: 40,
          cost_usd: 1.5, cost_per_turn_usd: 0.3, cost_share: 1,
        },
      ],
      daily: [{ date: '2026-06-20', cost_usd: 1.5, turns: 5 }],
    });
    const withModel = summary({
      ...noModel,
      daily: [{ date: '2026-06-20', cost_usd: 1.5, turns: 5, by_model: { 'claude-opus': 1.5 } }],
    });
    const a = buildUsageCsv(noModel);
    const b = buildUsageCsv(withModel);
    // b is exactly a PLUS the appended Daily-by-model sheet (a is a prefix of b).
    expect(b.startsWith(a)).toBe(true);
    expect(b.slice(a.length)).toContain('# Daily by model');
  });
});

describe('buildTeamCsv — By member sheet still present after the new sheet', () => {
  function team(over: Partial<CCUsageTeamSummary> = {}): CCUsageTeamSummary {
    return {
      ...summary(),
      team: null,
      teams: [],
      by_member: [],
      ...over,
    };
  }

  it('emits By model, Daily, Daily by model, AND By member sheets', () => {
    const data = team({
      daily: [{ date: '2026-06-20', cost_usd: 5, turns: 2, by_model: { opus: 5 } }],
      by_member: [
        {
          user_id: 'u1', display_name: 'Alice', email: 'a@x', is_you: false, roles: ['owner'],
          cost_usd: 5, turns: 2, sessions: 1, top_model: 'opus', cost_share: 1,
        },
      ],
    });
    const sh = sheets(buildTeamCsv(data));
    expect(sh['Daily by model']).toBeDefined();
    expect(sh['By member']).toBeDefined();
    expect(sh['By member'][0]).toBe('display_name,email,cost_usd,turns,sessions,top_model,cost_share');
    expect(sh['By member'][1]).toBe('Alice,a@x,5,2,1,opus,1');
  });

  it('keeps the By member sheet present even when no day has by_model', () => {
    const data = team({
      daily: [{ date: '2026-06-20', cost_usd: 5, turns: 2 }],
      by_member: [
        {
          user_id: 'u1', display_name: 'Bob', email: 'b@x', is_you: true, roles: [],
          cost_usd: 5, turns: 2, sessions: 1, top_model: 'sonnet', cost_share: 1,
        },
      ],
    });
    const csv = buildTeamCsv(data);
    expect(csv).not.toContain('# Daily by model');
    expect(csv).toContain('# By member');
  });
});

describe('csvCell — quoting + formula-injection neutralisation', () => {
  it('neutralises string cells that start with a formula trigger', () => {
    expect(csvCell('=HYPERLINK("x")')).toBe(`"'=HYPERLINK(""x"")"`);
    expect(csvCell('+1')).toBe("'+1");
    expect(csvCell('-cmd')).toBe("'-cmd");
    expect(csvCell('@SUM(A1)')).toBe("'@SUM(A1)");
    expect(csvCell('\tx')).toBe("'\tx");
    expect(csvCell('\rx')).toBe(`"'\rx"`);
  });

  it('keeps numbers numeric (a negative number is not a formula)', () => {
    expect(csvCell(-1.5)).toBe('-1.5');
    expect(csvCell(0)).toBe('0');
  });

  it('passes plain strings through and quotes commas/quotes/newlines', () => {
    expect(csvCell('opus')).toBe('opus');
    expect(csvCell('a,b')).toBe('"a,b"');
    expect(csvCell('say "hi"')).toBe('"say ""hi"""');
    expect(csvCell('2026-06-20')).toBe('2026-06-20');
  });
});

describe('buildUsageCsv — provider + account + project sheets', () => {
  const modelRow = {
    model: 'gpt-6-astra', tier: 'gpt-6-astra', turns: 2,
    input_tokens: 1, output_tokens: 2, cache_read_tokens: 3, cache_create_tokens: 4,
    cost_usd: 1, cost_per_turn_usd: 0.5, cost_share: 1,
  };

  it('adds a provider column to By model (row field, else inferred from the id)', () => {
    const sh = sheets(buildUsageCsv(summary({
      by_model: [
        { ...modelRow, provider: 'codex' },
        { ...modelRow, model: 'claude-opus-4-6', tier: 'opus' },
      ],
    })));
    expect(sh['By model'][0].startsWith('provider,model,tier,')).toBe(true);
    expect(sh['By model'][1].startsWith('codex,gpt-6-astra,')).toBe(true);
    expect(sh['By model'][2].startsWith('claude,claude-opus-4-6,')).toBe(true);
  });

  it('emits By provider, By account and Top projects only when present', () => {
    const empty = buildUsageCsv(summary());
    expect(empty).not.toContain('# By provider');
    expect(empty).not.toContain('# By account');
    expect(empty).not.toContain('# Top projects');

    const sh = sheets(buildUsageCsv(summary({
      by_provider: [{
        provider: 'codex', cost_usd: 2, cost_share: 1, turns: 3, sessions: 1,
        input_tokens: 10, output_tokens: 20, cache_read_tokens: 30, cache_create_tokens: 40,
        reasoning_output_tokens: 5,
      }],
      by_account: [{
        account: '', turns: 1, input_tokens: 0, output_tokens: 0, cache_read_tokens: 0,
        cache_create_tokens: 0, cost_usd: 1, cost_per_turn_usd: 1, cost_share: 1,
      }],
      top_projects: [{ project: '=evil', cost_usd: 1, turns: 1, cost_share: 1, repo: 'o/r' }],
    })));
    expect(sh['By provider'][1]).toBe('codex,2,1,3,1,10,20,30,40,5');
    expect(sh['By account'][1]).toBe('claude,Unattributed,1,1,1,1');
    expect(sh['Top projects'][0]).toBe('project,repo,cost_usd,turns,cost_share');
    expect(sh['Top projects'][1]).toBe("'=evil,o/r,1,1,1");
  });
});
