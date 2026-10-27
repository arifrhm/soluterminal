import { useEffect, useMemo, useRef, useState } from 'react';
import { createPortal } from 'react-dom';
import {
  api,
  ApiError,
  type CCUsageSummary,
  type CCUsageTeamSummary,
  type CCUsageMemberSummary,
  type CCUsageTierPrice,
  type MemberMachine,
  type UsageProvider,
} from '../api';
import {
  zeroFilledDaily,
  type DailyPoint,
  heroDeltas,
  deltaPct,
  costPerTurn,
  activityMatrix,
  activityOffsetMinutes,
  cumulativeSeries,
  modelMixSeries,
  lensColumns,
  primaryShares,
  sortMembers,
  pooledBaseline,
  memberDelta,
  memberModelMix,
  periodDigest,
  accountRows,
  alignMemberDailies,
  projectMembers,
  providerRows,
  providerLabel,
  modelProvider,
  modelTier,
  modelLabel,
  modelColorMap,
  isCodexModel,
  normalizeProvider,
  memberProviderMix,
  lastActiveDate,
  relativeDay,
  MODEL_COLOR_FALLBACK,
  type Lens,
  type ColumnSpec,
  type MemberRow,
} from '../lib/usage';
import { DeltaBadge } from './usage/DeltaBadge';
import { ActivityHeatmap } from './usage/ActivityHeatmap';
import { CumulativeSpendChart } from './usage/CumulativeSpendChart';
import { ModelMixTrendChart } from './usage/ModelMixTrendChart';
import { TeamTrendChart, type TeamTrendSeries } from './usage/TeamTrendChart';
import { ProjectMembersBar } from './usage/ProjectMembersBar';
import { PeriodDigest } from './usage/PeriodDigest';
import { LensToggle } from './usage/LensToggle';
import { ProviderToggle } from './usage/ProviderToggle';
import { ByProviderCard } from './usage/ByProviderCard';
import {
  MemberComparisonTable,
  type MemberComparisonColumn,
  type MemberComparisonRow,
} from './usage/MemberComparisonTable';

// Full-screen "Usage" page (overlay), opened from the account footer.
// Reads the ccUsageSummary endpoint with a user-selectable window. This SPA has
// no router, so it's a React-state-toggled fixed overlay mounted from the
// Sidebar (mirrors WelcomeOverlay / dialog-backdrop). Closes on Esc and
// backdrop click.
//
// HONESTY RULES (docs/USAGE-PHASE2-CONTRACT.md §"Honesty rules"):
//   1. Never surface raw total_tokens — lead with cost_usd, show fresh vs
//      cached split, never a naked sum.
//   2. cost_usd is NOTIONAL (API-equivalent) — show the framing banner.
//   3. in_out is skewed to out_app on backfilled history by design — label
//      it as reflecting post-tracking sessions, not all-time truth.

interface Props {
  onClose: () => void;
}

type WindowKey = '7d' | '30d' | '90d' | 'all';
const WINDOWS: Array<{ key: WindowKey; label: string }> = [
  { key: '7d', label: '7d' },
  { key: '30d', label: '30d' },
  { key: '90d', label: '90d' },
  { key: 'all', label: 'All' },
];

// (Phase 3) View scope. "you" is the per-account summary (existing, default);
// "team" is the team-wide rollup. (Phase 3.1) "member" is the owner drill-down
// into ONE team member's full per-account detail (entered from a by_member row,
// only when the team grants the owner per-member visibility). The Team option
// only appears once we've detected the caller is in a team (probe response
// carries a non-null `team`); "member" is only ever reachable FROM team scope.
type Scope = 'you' | 'team' | 'member';

// (Phase 1) The time-series view shown in the daily-chart block: the existing
// per-day bars, the running-total spend line, or the 100%-stacked model-mix
// area. Local UI state only — the underlying `daily` data is the same for all
// three (no refetch).
type SeriesView = 'daily' | 'cumulative' | 'modelmix';
const SERIES_VIEWS: Array<{ key: SeriesView; label: string }> = [
  { key: 'daily', label: 'Daily' },
  { key: 'cumulative', label: 'Cumulative' },
  { key: 'modelmix', label: 'Model mix' },
];

// (Phase 3.1) The member currently being drilled into. We keep the display_name
// alongside the user_id so the title/back UI + CSV filename can render before the
// detail finishes loading (and even if the detail's own `member` meta is absent).
type SelectedMember = { user_id: string; display_name: string };

// USD with cents for the detail page (full precision matters more here than
// in the cramped sidebar). Whole-dollar grouping once we're past $1,000.
// Non-finite / missing values render "—" (never "$NaN").
function fmtUSD(usd: number | undefined): string {
  if (typeof usd !== 'number' || !Number.isFinite(usd)) return '—';
  if (usd >= 1000) return '$' + Math.round(usd).toLocaleString();
  return '$' + usd.toFixed(2);
}

// Compact "$0.41/turn" style for the per-turn column (always cents — these
// are small numbers).
function fmtPerTurn(usd: number | undefined): string {
  if (typeof usd !== 'number' || !Number.isFinite(usd)) return '—';
  if (usd <= 0) return '$0.00';
  // Always 3 decimals. These are small numbers where sub-cent precision is
  // honest, and a 3-vs-2 cliff at $1.00 made two near-equal members ($0.998 vs
  // $1.004) read as a 2× gap that isn't there.
  return '$' + usd.toFixed(3);
}

// Humanize a big token count (fresh/cached split, never the meaningless
// total). 1_234_567 → "1.2M".
function fmtTokens(n: number | undefined): string {
  if (typeof n !== 'number' || !Number.isFinite(n)) return '—';
  if (n <= 0) return '0';
  if (n >= 1e9) return (n / 1e9).toFixed(1) + 'B';
  if (n >= 1e6) return (n / 1e6).toFixed(1) + 'M';
  if (n >= 1e3) return (n / 1e3).toFixed(1) + 'K';
  return String(Math.round(n));
}

// Share as a rounded percent; "—" for a missing/NaN share so a partial
// response never prints "NaN%".
function pct(share: number | undefined): string {
  if (typeof share !== 'number' || !Number.isFinite(share)) return '—';
  return Math.round(Math.max(0, Math.min(1, share)) * 100) + '%';
}

// CSS width for a 0..1 share bar. Unlike pct() it never returns "—" (an invalid
// width would be dropped and the bar would render at its natural width).
function pctWidth(share: number | undefined): string {
  if (typeof share !== 'number' || !Number.isFinite(share)) return '0%';
  return Math.round(Math.max(0, Math.min(1, share)) * 100) + '%';
}

// Localized integer count; "—" for a missing/NaN value (older/partial hero).
function fmtInt(n: number | undefined): string {
  if (typeof n !== 'number' || !Number.isFinite(n)) return '—';
  return Math.round(n).toLocaleString();
}

// "Updated <local time>" status line; never "Invalid Date" when generated_at is
// missing or malformed.
function fmtUpdated(iso: string | undefined): string {
  const t = iso ? Date.parse(iso) : NaN;
  return Number.isNaN(t) ? 'Updated' : `Updated ${new Date(t).toLocaleString()}`;
}

function changePhrase(label: string, value: number | null): string | null {
  if (value == null || !Number.isFinite(value)) return null;
  const amount = Math.abs(Math.round(value * 100));
  if (amount < 1) return `${label} was steady`;
  return `${label} ${value > 0 ? 'increased' : 'decreased'} ${amount}%`;
}

// One deterministic period readout for the personal/member view. It uses words
// as well as signed values, so meaning never depends on color alone.
function periodInsight(deltas: ReturnType<typeof heroDeltas> | null): string | null {
  if (!deltas) return null;
  const clauses = [
    changePhrase('Turns', deltas.turns.pct),
    changePhrase('estimated cost', deltas.cost_usd.pct),
  ].filter((v): v is string => !!v);

  const currentPerTurn = costPerTurn(deltas.cost_usd.cur, deltas.turns.cur);
  const previousPerTurn = costPerTurn(deltas.cost_usd.prev, deltas.turns.prev);
  if (previousPerTurn > 0) {
    const efficiencyDelta = (currentPerTurn - previousPerTurn) / previousPerTurn;
    const amount = Math.abs(Math.round(efficiencyDelta * 100));
    if (amount >= 1) {
      clauses.push(`cost per turn ${efficiencyDelta < 0 ? 'improved' : 'increased'} ${amount}%`);
    }
  }
  if (clauses.length === 0) return 'This is the first period with a comparable usage baseline.';
  return clauses.join(' · ') + '.';
}

// (Phase 2) Format a raw metric value per a lens column's declared format. Used
// by the owner comparison table cells AND the muted baseline row so they print
// identically: usd→fmtUSD, int→localized integer, pct→pct, tok→fmtTokens.
function fmtColumn(format: ColumnSpec['format'], value: number): string {
  switch (format) {
    case 'usd': return fmtUSD(value);
    case 'int': return Math.round(value).toLocaleString();
    case 'pct': return pct(value);
    case 'tok': return fmtTokens(value);
  }
}

// Per-Mtok price for the collapsible pricing table. These are small, sub-$50
// numbers (e.g. 0.10, 6.25) so always show enough cents to be exact.
function fmtPrice(usd: number): string {
  if (!isFinite(usd) || usd < 0) return '$0';
  return '$' + usd.toFixed(2);
}

// Humanize a duration in seconds → "3h 12m", "8m", "45s". Used by the busiest-
// sessions list (durations are window-clamped server-side).
function fmtDuration(seconds: number): string {
  if (!isFinite(seconds) || seconds <= 0) return '0s';
  const s = Math.round(seconds);
  const h = Math.floor(s / 3600);
  const m = Math.floor((s % 3600) / 60);
  if (h > 0) return m > 0 ? `${h}h ${m}m` : `${h}h`;
  if (m > 0) {
    const rem = s % 60;
    // Only show seconds for sub-10-minute spans; longer ones round to minutes.
    return m < 10 && rem > 0 ? `${m}m ${rem}s` : `${m}m`;
  }
  return `${s}s`;
}

// Model categories are colored by lib/usage modelColorMap: stable per model
// NAME (Claude ramp vs Codex ramp), so a model keeps its hue across windows and
// filters. Tier badges use modelTier (Claude + OpenAI aware).

// Per-member categories use the same role-based palette in a different order,
// preventing a model and teammate at the same ordinal from looking identical.
const MEMBER_COLOR_RAMP = [
  'var(--usage-blue)',
  'var(--usage-green)',
  'var(--usage-amber)',
  'var(--usage-rose)',
  'var(--usage-cyan)',
  'var(--usage-orange)',
  'var(--usage-violet)',
  'var(--usage-teal)',
];
const MEMBER_COLOR_FALLBACK = MEMBER_COLOR_RAMP[0];

// (Phase 3c) Build the ONE stable userId→color map for the team view. Hue is
// assigned by the team roster's cost-DESC order (the order the server sends
// by_member, which we sort here defensively), so the top spenders get the most
// saturated hues and a given teammate keeps the SAME color everywhere it appears
// — the multi-member trend chart AND every per-project breakdown bar. Overflow
// (>8 members) cycles the ramp. Returns a lookup that falls back to the first
// hue for an unknown id (e.g. a project member absent from the roster — shouldn't
// happen, but never returns undefined).
function buildMemberColors(
  byMember: ReadonlyArray<{ user_id: string; cost_usd?: number }>,
): (userId: string) => string {
  const ordered = [...byMember]
    .map((m, i) => ({ m, i }))
    .sort((a, b) => (b.m.cost_usd ?? 0) - (a.m.cost_usd ?? 0) || a.i - b.i);
  const map = new Map<string, string>();
  ordered.forEach(({ m }, i) => {
    map.set(m.user_id, MEMBER_COLOR_RAMP[i % MEMBER_COLOR_RAMP.length]);
  });
  return (userId: string) => map.get(userId) ?? MEMBER_COLOR_FALLBACK;
}

// Lowercase, dash-collapse a name into a filesystem-safe slug for CSV filenames
// (team slug / member display name). Returns '' for empty/symbol-only input so
// callers can fall back to a generic label.
function slugify(s: string): string {
  return (s || '')
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, '-')
    .replace(/^-+|-+$/g, '');
}

// CSV cell quoting: wrap in double-quotes and double any inner quotes whenever a
// field contains a comma, quote, or newline. Numbers pass through untouched (a
// negative number stays numeric). STRING cells that a spreadsheet would treat
// as a formula (leading = + - @ tab CR — e.g. a project or display name like
// "=HYPERLINK(...)") are neutralised with a leading single quote.
export function csvCell(v: string | number): string {
  if (typeof v === 'number') return String(v);
  let s = String(v ?? '');
  if (/^[=+\-@\t\r]/.test(s)) s = "'" + s;
  return /[",\n\r]/.test(s) ? `"${s.replace(/"/g, '""')}"` : s;
}

// Build the export CSV from the CURRENTLY-LOADED summary (no network). One file
// with labelled sections: by model, by provider, the daily series, by account,
// top projects and (when present) daily-by-model. Excel-openable; commas inside
// fields are quoted. Optional sections are emitted only when the response
// carries rows, so older responses keep their original sheets.
export function buildUsageCsv(data: CCUsageSummary): string {
  const lines: string[] = [];
  // By-model sheet. `provider` comes from the row (Codex provider contract) or,
  // on older responses, is inferred from the model id.
  lines.push('# By model');
  lines.push([
    'provider', 'model', 'tier', 'turns', 'cost_usd', 'cost_per_turn_usd', 'cost_share',
    'input_tokens', 'output_tokens', 'cache_read_tokens', 'cache_create_tokens',
  ].join(','));
  for (const m of data.by_model ?? []) {
    lines.push([
      csvCell(modelProvider(m.model, m.provider)),
      csvCell(m.model),
      csvCell(m.tier),
      m.turns,
      m.cost_usd,
      m.cost_per_turn_usd,
      m.cost_share,
      m.input_tokens,
      m.output_tokens,
      m.cache_read_tokens,
      m.cache_create_tokens,
    ].join(','));
  }
  // (Codex provider) By-provider sheet — only when the cloud sent the split.
  if ((data.by_provider ?? []).length > 0) {
    lines.push('');
    lines.push('# By provider');
    lines.push([
      'provider', 'cost_usd', 'cost_share', 'turns', 'sessions', 'input_tokens',
      'output_tokens', 'cache_read_tokens', 'cache_create_tokens', 'reasoning_output_tokens',
    ].join(','));
    for (const p of data.by_provider ?? []) {
      lines.push([
        csvCell(normalizeProvider(p.provider)),
        p.cost_usd,
        p.cost_share,
        p.turns,
        p.sessions,
        p.input_tokens,
        p.output_tokens,
        p.cache_read_tokens,
        p.cache_create_tokens,
        p.reasoning_output_tokens ?? 0,
      ].join(','));
    }
  }
  lines.push('');
  // Daily series — emit the raw server days (the honest, non-zero-filled rows).
  lines.push('# Daily');
  lines.push(['date', 'cost_usd', 'turns'].join(','));
  for (const d of data.daily ?? []) {
    lines.push([csvCell(d.date), d.cost_usd, d.turns].join(','));
  }
  // By-account sheet ("" = unattributed turns).
  if ((data.by_account ?? []).length > 0) {
    lines.push('');
    lines.push('# By account');
    lines.push(['provider', 'account', 'turns', 'cost_usd', 'cost_per_turn_usd', 'cost_share'].join(','));
    for (const a of data.by_account ?? []) {
      lines.push([
        csvCell(normalizeProvider(a.provider)),
        csvCell(a.account || 'Unattributed'),
        a.turns,
        a.cost_usd,
        a.cost_per_turn_usd,
        a.cost_share,
      ].join(','));
    }
  }
  // Top-projects sheet.
  if ((data.top_projects ?? []).length > 0) {
    lines.push('');
    lines.push('# Top projects');
    lines.push(['project', 'repo', 'cost_usd', 'turns', 'cost_share'].join(','));
    for (const p of data.top_projects ?? []) {
      lines.push([csvCell(p.project), csvCell(p.repo ?? ''), p.cost_usd, p.turns, p.cost_share].join(','));
    }
  }
  // (Phase 3a) Daily-by-model sheet — one row per (date, model) from
  // daily[].by_model, so an owner can pivot the model split offline. Appended
  // AFTER the existing sheets so those stay byte-stable. Emitted only when ANY
  // day carries a by_model breakdown (older responses omit it → the sheet is
  // skipped entirely, keeping the file byte-stable for them). Each day's models
  // are emitted cost-DESC for a stable, readable order.
  const hasDailyByModel = (data.daily ?? []).some(
    (d) => d.by_model && Object.keys(d.by_model).length > 0,
  );
  if (hasDailyByModel) {
    lines.push('');
    lines.push('# Daily by model');
    lines.push(['date', 'model', 'cost_usd'].join(','));
    for (const d of data.daily ?? []) {
      const bm = d.by_model;
      if (!bm) continue;
      const models = Object.entries(bm).sort((a, b) => b[1] - a[1]);
      for (const [model, cost] of models) {
        lines.push([csvCell(d.date), csvCell(model), cost].join(','));
      }
    }
  }
  return lines.join('\r\n');
}

// (Phase 3) Team export — the per-account by-model + daily sheets PLUS a
// by-member sheet. The by_member rows are exactly what the server allowed the
// caller to see (owner → all, non-owner → own-only), so this never leaks beyond
// the redaction. Empty/absent by_member → the section header with no rows.
export function buildTeamCsv(data: CCUsageTeamSummary): string {
  const lines: string[] = [buildUsageCsv(data)];
  lines.push('');
  // (Phase 2) Emit the enriched per-member columns (active_days, output_tokens,
  // cost_per_turn_usd, cache_hit_rate) only when ANY member carries them — older
  // responses omit these, so a non-owner / older sheet keeps the original 7
  // columns and stays byte-stable.
  const members = data.by_member ?? [];
  const enriched = members.some(
    (m) =>
      m.active_days != null ||
      m.output_tokens != null ||
      m.cost_per_turn_usd != null ||
      m.cache_hit_rate != null,
  );
  lines.push('# By member');
  const header = [
    'display_name', 'email', 'cost_usd', 'turns', 'sessions', 'top_model', 'cost_share',
  ];
  if (enriched) header.push('active_days', 'output_tokens', 'cost_per_turn_usd', 'cache_hit_rate');
  lines.push(header.join(','));
  for (const m of members) {
    const row: Array<string | number> = [
      csvCell(m.display_name),
      csvCell(m.email),
      m.cost_usd,
      m.turns,
      m.sessions,
      csvCell(m.top_model),
      m.cost_share,
    ];
    if (enriched) {
      row.push(
        m.active_days ?? '',
        m.output_tokens ?? '',
        m.cost_per_turn_usd ?? '',
        m.cache_hit_rate ?? '',
      );
    }
    lines.push(row.join(','));
  }
  return lines.join('\r\n');
}

// Trigger a client-side download of `text` as `filename` via a Blob + object
// URL (revoked shortly after). Mirrors the transcript-export path; works in the
// Tauri webview (IPC doesn't intercept <a download>).
// CSV callers prepend a UTF-8 BOM (CSV_BOM) so Excel decodes non-ASCII names.
const CSV_BOM = '\uFEFF';
function downloadText(filename: string, text: string, mime: string) {
  const blob = new Blob([text], { type: mime });
  const url = URL.createObjectURL(blob);
  const a = document.createElement('a');
  a.href = url;
  a.download = filename;
  document.body.appendChild(a);
  a.click();
  a.remove();
  setTimeout(() => URL.revokeObjectURL(url), 1000);
}

// The four token classes of the overall cost_breakdown, in render order, with
// their display labels and bar variants. Kept in one place so the legend, bar,
// and table all agree. `cache_create` carries the cache-WRITE cost (the field
// is named for the token class; we label it "cache write" for the user).
// Each token class maps a cost_breakdown $ key to its matching by_model[] token-
// count field, so the breakdown section can show BOTH the token count (Σ over
// by_model) and the dollars per class. `cache_create` carries the cache-WRITE
// cost/tokens (labeled "cache write" for the user).
const BREAKDOWN_PARTS: Array<{
  key: keyof NonNullable<CCUsageSummary['cost_breakdown']>;
  tokenKey: 'input_tokens' | 'output_tokens' | 'cache_read_tokens' | 'cache_create_tokens';
  label: string;
  variant: 'magenta' | 'violet' | 'cache' | 'fresh';
}> = [
  { key: 'cache_read', tokenKey: 'cache_read_tokens', label: 'cache read', variant: 'cache' },
  { key: 'output', tokenKey: 'output_tokens', label: 'output', variant: 'magenta' },
  { key: 'input', tokenKey: 'input_tokens', label: 'input', variant: 'violet' },
  { key: 'cache_create', tokenKey: 'cache_create_tokens', label: 'cache write', variant: 'fresh' },
];

// Overall per-class token totals, summed across by_model[] (the contract says to
// derive these client-side — no dedicated overall token-count fields exist).
// Returns null when by_model is empty/absent so the breakdown can fall back to
// the cost-only columns without crashing.
function classTokenTotals(
  byModel: CCUsageSummary['by_model'] | undefined,
): Record<(typeof BREAKDOWN_PARTS)[number]['tokenKey'], number> | null {
  if (!byModel || byModel.length === 0) return null;
  const totals = {
    input_tokens: 0,
    output_tokens: 0,
    cache_read_tokens: 0,
    cache_create_tokens: 0,
  };
  for (const m of byModel) {
    totals.input_tokens += m?.input_tokens ?? 0;
    totals.output_tokens += m?.output_tokens ?? 0;
    totals.cache_read_tokens += m?.cache_read_tokens ?? 0;
    totals.cache_create_tokens += m?.cache_create_tokens ?? 0;
  }
  return totals;
}

// Pricing table columns, rendered straight from pricing.tiers. Each maps a
// CCUsageTierPrice field to a header label.
const PRICE_COLS: Array<{
  key: 'input' | 'output' | 'cache_read' | 'cache_write_5m' | 'cache_write_1h';
  label: string;
}> = [
  { key: 'input', label: 'Input' },
  { key: 'output', label: 'Output' },
  { key: 'cache_read', label: 'Cache read' },
  { key: 'cache_write_5m', label: 'Cache write 5m' },
  { key: 'cache_write_1h', label: 'Cache write 1h' },
];
// Preferred tier render order (opus → sonnet → haiku → fable). The pricing table
// renders whatever tiers the response actually carries (see priceTierKeys), but
// orders the known ones consistently; any extra/unknown tier the server adds is
// appended after these.
const PRICE_TIER_ORDER = ['opus', 'sonnet', 'haiku', 'fable'];

// Resolve the tiers present in the response into render order. Known tiers first
// (in PRICE_TIER_ORDER), then any unexpected extras the server sent, so we never
// silently drop a 4th (or 5th) tier and never assume one that's absent.
function priceTierKeys(tiers: Record<string, unknown>): string[] {
  const present = new Set(Object.keys(tiers ?? {}));
  const ordered = PRICE_TIER_ORDER.filter((t) => present.has(t));
  for (const k of present) {
    if (!ordered.includes(k)) ordered.push(k);
  }
  return ordered;
}

// (Codex provider) Group the ordered tier keys by provider for the pricing
// table: Anthropic (Claude Code) first, then OpenAI (Codex). A tier's provider
// is its own `provider` field, else inferred from the key (older clouds only
// send Claude tiers). Empty groups are dropped.
function priceTierGroups(
  tiers: Partial<Record<string, CCUsageTierPrice>>,
): Array<{ provider: 'claude' | 'codex'; label: string; keys: string[] }> {
  const groups = [
    { provider: 'claude' as const, label: 'Claude Code (Anthropic)', keys: [] as string[] },
    { provider: 'codex' as const, label: 'Codex (OpenAI)', keys: [] as string[] },
  ];
  for (const k of priceTierKeys(tiers)) {
    const row = tiers[k];
    if (!row) continue;
    const prov = row.provider ? normalizeProvider(row.provider) : isCodexModel(k) ? 'codex' : 'claude';
    groups[prov === 'codex' ? 1 : 0].keys.push(k);
  }
  return groups.filter((g) => g.keys.length > 0);
}

// CSS class for a tier badge color. Fable gets a distinct violet-heavy accent so
// it's visually separable from sonnet/opus/haiku (which share the neutral dim
// label); (Codex provider) any OpenAI tier/model gets the Codex accent. Keyed
// off the lowercased tier string (plus the model id when known); unknown tiers
// get no extra class.
function tierBadgeClass(tier: string, model?: string): string {
  const t = (tier || '').toLowerCase();
  if (isCodexModel(t) || isCodexModel(model)) return 'usage-model-tier-codex';
  if (t === 'fable') return 'usage-model-tier-fable';
  return '';
}

// Overall per-token-class cost breakdown: a horizontal stacked (by cost) bar plus
// a legend table. Each legend row carries BOTH the TOKEN COUNT (+ % of total
// tokens) and the DOLLARS (+ % of total cost), so the key insight is legible:
// cache-read is ~97% of TOKENS but ~61% of COST; output is ~0.4% of tokens but
// ~12% of cost. Token totals are summed across by_model[] (passed in). When
// by_model is empty/absent (`tokenTotals == null`), the token columns are
// dropped and we fall back to just the cost columns (no crash). Renders nothing
// if the breakdown is absent or sums to ~0.
function CostBreakdown({
  breakdown,
  tokenTotals,
}: {
  breakdown: NonNullable<CCUsageSummary['cost_breakdown']>;
  tokenTotals: ReturnType<typeof classTokenTotals>;
}) {
  const parts = BREAKDOWN_PARTS.map((p) => ({
    ...p,
    value: Math.max(0, breakdown[p.key] ?? 0),
    tokens: tokenTotals ? Math.max(0, tokenTotals[p.tokenKey] ?? 0) : 0,
  }));
  const total = parts.reduce((s, p) => s + p.value, 0);
  if (total <= 0) {
    return <div className="usage-page-empty-inline">No cost to break down in this window.</div>;
  }
  const tokenGrand = parts.reduce((s, p) => s + p.tokens, 0);
  const showTokens = tokenTotals != null && tokenGrand > 0;
  return (
    <div className="usage-breakdown">
      <div className="usage-breakdown-bar" role="img" aria-label="Cost by token class">
        {parts.map((p) =>
          p.value > 0 ? (
            <span
              key={p.key}
              className={`usage-breakdown-seg usage-breakdown-${p.variant}`}
              style={{ width: (p.value / total) * 100 + '%' }}
              title={`${p.label}: ${fmtUSD(p.value)} (${pct(p.value / total)})`}
            />
          ) : null,
        )}
      </div>
      <table className="usage-breakdown-legend">
        {showTokens && (
          <thead>
            <tr>
              <th />
              <th className="num usage-breakdown-colhdr" colSpan={2}>tokens</th>
              <th className="num usage-breakdown-colhdr" colSpan={2}>cost</th>
            </tr>
          </thead>
        )}
        <tbody>
          {parts.map((p) => (
            <tr key={p.key}>
              <td className="usage-breakdown-key">
                <span className={`usage-breakdown-dot usage-breakdown-${p.variant}`} />
                {p.label}
              </td>
              {showTokens && (
                <>
                  <td className="usage-breakdown-tok num">{fmtTokens(p.tokens)}</td>
                  <td className="usage-breakdown-pct num">{pct(tokenGrand > 0 ? p.tokens / tokenGrand : 0)}</td>
                </>
              )}
              <td className="usage-breakdown-usd num">{fmtUSD(p.value)}</td>
              <td className="usage-breakdown-pct num">{pct(p.value / total)}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

// Inline-SVG daily cost bar chart. No dependency. Zero-baseline; bars scale to
// the max day. STACKED by model (Phase 2.2) when `daily[].by_model` is present —
// each day's bar is split into per-model segments using a stable model→color map
// (top-cost models get the magenta/violet brand accents). A small legend maps
// color→model. Falls back GRACEFULLY to a single-color total bar for days (or
// whole responses) lacking `by_model`. Zero-filled gap days simply have no
// segments, keeping the x-axis even (honesty rule: the chart agrees with hero).
function DailyChart({
  days,
  modelColors,
  legendModels,
}: {
  days: DailyPoint[];
  modelColors: Map<string, string>;
  legendModels: string[];
}) {
  if (days.length === 0) {
    return <div className="usage-page-empty-inline">No daily activity in this window.</div>;
  }
  const max = Math.max(...days.map((d) => d.cost_usd), 0);
  const W = 720;
  const H = 140;
  const PAD_B = 16; // room for the x-axis label
  const n = days.length;
  const gap = n > 60 ? 0.5 : n > 30 ? 1 : 2;
  const slot = W / n;
  const barW = Math.max(1, slot - gap);
  const plotH = H - PAD_B - 4;
  // Show first / mid / last date labels only (avoid an unreadable axis).
  const labelIdx = new Set([0, Math.floor((n - 1) / 2), n - 1]);

  // Stack a day's per-model segments bottom-up in the SAME order as the legend
  // (cost-DESC across the window) so colors read consistently between days. Any
  // model present that day but absent from the legend (defensive) renders last
  // with the fallback color.
  function segmentsFor(d: DailyPoint): Array<{ model: string; value: number; color: string }> {
    if (!d.by_model) return [];
    const segs: Array<{ model: string; value: number; color: string }> = [];
    const seen = new Set<string>();
    for (const model of legendModels) {
      const v = d.by_model[model];
      if (v && v > 0) {
        segs.push({ model, value: v, color: modelColors.get(model) ?? MODEL_COLOR_FALLBACK });
        seen.add(model);
      }
    }
    for (const [model, v] of Object.entries(d.by_model)) {
      if (!seen.has(model) && v && v > 0) {
        segs.push({ model, value: v, color: modelColors.get(model) ?? MODEL_COLOR_FALLBACK });
      }
    }
    return segs;
  }

  return (
    <>
      <svg
        className="usage-daily-chart"
        viewBox={`0 0 ${W} ${H}`}
        width="100%"
        height={H}
        preserveAspectRatio="none"
        role="img"
        aria-label="Daily notional cost by model"
      >
        {days.map((d, i) => {
          const x = i * slot + (slot - barW) / 2;
          const segs = segmentsFor(d);
          if (segs.length === 0) {
            // Fallback single-color total bar (older response, or a zero-filled
            // gap day where height resolves to 0).
            const h = max <= 0 ? 0 : (d.cost_usd / max) * plotH;
            return (
              <rect
                key={d.date}
                className="usage-daily-bar"
                x={x.toFixed(2)}
                y={(H - PAD_B - h).toFixed(2)}
                width={barW.toFixed(2)}
                height={Math.max(0, h).toFixed(2)}
              >
                <title>{`${d.date}: ${fmtUSD(d.cost_usd)}`}</title>
              </rect>
            );
          }
          // Stacked segments, bottom-up. Scale by the day's total so the stack
          // height equals the equivalent single bar.
          let yCursor = H - PAD_B;
          return (
            <g key={d.date}>
              {segs.map((s) => {
                const h = max <= 0 ? 0 : (s.value / max) * plotH;
                yCursor -= h;
                return (
                  <rect
                    key={s.model}
                    className="usage-daily-seg"
                    x={x.toFixed(2)}
                    y={yCursor.toFixed(2)}
                    width={barW.toFixed(2)}
                    height={Math.max(0, h).toFixed(2)}
                    style={{ fill: s.color }}
                  >
                    <title>{`${d.date} · ${s.model}: ${fmtUSD(s.value)} (of ${fmtUSD(d.cost_usd)})`}</title>
                  </rect>
                );
              })}
            </g>
          );
        })}
        {days.map((d, i) =>
          labelIdx.has(i) ? (
            <text
              key={`lbl-${d.date}`}
              className="usage-daily-axis"
              x={(i * slot + slot / 2).toFixed(2)}
              y={H - 4}
              textAnchor={i === 0 ? 'start' : i === n - 1 ? 'end' : 'middle'}
            >{d.date.slice(5)}</text>
          ) : null,
        )}
      </svg>
      {legendModels.length > 0 && (
        <div className="usage-daily-legend" aria-label="Daily chart model colors">
          {legendModels.map((model) => (
            <span className="usage-daily-legend-item" key={model}>
              <span
                className="usage-daily-legend-dot"
                style={{ background: modelColors.get(model) ?? MODEL_COLOR_FALLBACK }}
              />
              <span className="usage-daily-legend-name" title={model}>{model}</span>
            </span>
          ))}
        </div>
      )}
    </>
  );
}

// Busiest sessions (Phase 2.2) — compact list of the top sessions by cost. Rank,
// project, cost, turns, model tier label, human duration, and an optional
// %-of-top-session cost bar. NEVER surfaces the raw session_id (UUID) — used
// only as the React key. Renders nothing when the list is empty.
function BusiestSessions({ sessions }: { sessions: NonNullable<CCUsageSummary['top_sessions']> }) {
  const topCost = Math.max(...sessions.map((s) => s.cost_usd), 0);
  return (
    <div className="usage-sessions">
      {sessions.map((s, i) => (
        <div className="usage-session-row" key={s.session_id}>
          <span className="usage-session-rank">{i + 1}</span>
          <div className="usage-session-main">
            <div className="usage-session-line">
              <span className="usage-session-project" title={s.project}>{s.project || '(unknown)'}</span>
              <span className="usage-session-cost">{fmtUSD(s.cost_usd)}</span>
            </div>
            <div className="usage-session-meta">
              <span
                className={`usage-session-tier ${tierBadgeClass(modelTier(s.model), s.model)}`}
                title={modelLabel(s.model)}
              >{modelTier(s.model)}</span>
              <span className="usage-session-sep">·</span>
              <span>{s.turns.toLocaleString()} turns</span>
              <span className="usage-session-sep">·</span>
              <span>{fmtDuration(s.duration_seconds)}</span>
            </div>
            <span className="usage-share-track usage-session-bar">
              <span
                className="usage-share-fill usage-share-magenta"
                style={{ width: pctWidth(topCost > 0 ? s.cost_usd / topCost : 0) }}
              />
            </span>
          </div>
        </div>
      ))}
    </div>
  );
}

// machineStatus resolves the authoritative connection state, preferring the
// server `health` enum (online | reconnecting | offline | never_connected) and
// falling back to the online/reconnecting booleans for older payloads.
function machineStatus(mc: MemberMachine): { label: string; cls: string } {
  const h = mc.health;
  if (h === 'reconnecting' || (!h && mc.reconnecting)) return { label: 'reconnecting', cls: 'is-reconnecting' };
  if (h === 'online' || (!h && mc.online)) return { label: 'online', cls: 'is-online' };
  if (h === 'never_connected') return { label: 'never connected', cls: 'is-offline' };
  return { label: 'offline', cls: 'is-offline' };
}

// fmtBackends turns the relay backend ids into a human label — a machine with
// both wsl+win tunnels is a dual install. Empty/legacy ("") entries are dropped.
function fmtBackends(backends?: string[]): string {
  const labels = (backends ?? [])
    .filter(Boolean)
    .map((b) => (b === 'wsl' ? 'WSL' : b === 'win' ? 'Windows' : b === 'host' ? 'host' : b));
  if (labels.length === 0) return '';
  return labels.join(' + ');
}

// machineRelative — compact "just now / 2m ago / 3h ago / 5d ago" from an
// RFC3339 timestamp, mirroring the shared native relativeTime formatter.
function machineRelative(iso?: string): string {
  if (!iso) return '';
  const t = Date.parse(iso);
  if (Number.isNaN(t)) return '';
  const s = Math.max(0, Math.floor((Date.now() - t) / 1000));
  if (s < 45) return 'just now';
  const m = Math.floor(s / 60);
  if (m < 60) return `${m}m ago`;
  const h = Math.floor(m / 60);
  if (h < 24) return `${h}h ago`;
  return `${Math.floor(h / 24)}d ago`;
}

function fmtShortDate(iso?: string): string {
  if (!iso) return '';
  const t = Date.parse(iso);
  if (Number.isNaN(t)) return '';
  return new Date(t).toLocaleDateString(undefined, { month: 'short', day: 'numeric' });
}

// (installer visibility) Renders one member's machines with the full per-machine
// picture: name, live relay status, backends (WSL / Windows / dual), platform,
// the installer + daemon versions announced, and — for anything not currently
// online — when it was last seen, plus when it first connected. Rendered in the
// member-detail Machines section. An empty list shows a friendly note.
function MemberMachines({ machines }: { machines: MemberMachine[] }) {
  if (machines.length === 0) {
    return <div className="usage-machines-note">No machines connected.</div>;
  }
  return (
    <ul className="usage-machines-list">
      {machines.map((mc) => {
        const status = machineStatus(mc);
        const meta: string[] = [];
        const backends = fmtBackends(mc.backends);
        if (backends) meta.push(backends);
        if (mc.shellPlatform) meta.push(mc.shellPlatform);
        if (mc.shellVersion) meta.push(`installer ${mc.shellVersion}`);
        if (mc.daemonVersion) meta.push(`daemon ${mc.daemonVersion}`);
        if (meta.length === 0) meta.push('version unknown');

        const sub: string[] = [];
        // Last-seen only matters when it's NOT live — online machines are here now.
        if (status.cls !== 'is-online') {
          const seen = machineRelative(mc.lastSeenAt);
          if (seen) sub.push(`last seen ${seen}`);
        }
        const since = fmtShortDate(mc.firstConnectAt);
        if (since) sub.push(`since ${since}`);

        return (
          <li className="usage-machine-row" key={mc.id}>
            <div className="usage-machine-head">
              <span className="usage-machine-name" title={mc.name}>{mc.name}</span>
              <span className={`usage-machine-status ${status.cls}`}>{status.label}</span>
            </div>
            <div className="usage-machine-meta">{meta.join(' · ')}</div>
            {sub.length > 0 && <div className="usage-machine-sub">{sub.join(' · ')}</div>}
          </li>
        );
      })}
    </ul>
  );
}

// (Phase 3) By-member breakdown — one row per member the server allowed the
// caller to see. Only rendered on the NON-owner path (owner → all members goes
// through the comparison table instead), so the server-redacted list here is
// just the caller's own row: display name (with a "you" marker), a roles chip,
// cost, turns, the member's top-model tier label, and a %-of-team cost-share
// bar. Rows are plain, non-interactive. Renders nothing for an empty list (the
// caller decides whether to show the redaction note).
function ByMember({ members }: { members: NonNullable<CCUsageTeamSummary['by_member']> }) {
  return (
    <div className="usage-members">
      {members.map((m) => (
        <div className={`usage-member-row${m.is_you ? ' is-you' : ''}`} key={m.user_id}>
          <div className="usage-member-main">
            <div className="usage-member-line">
              <span className="usage-member-name" title={m.email || m.display_name}>
                {m.display_name || m.email || '(unknown)'}
                {m.is_you && <span className="usage-member-you">you</span>}
              </span>
              <span className="usage-member-cost">{fmtUSD(m.cost_usd)}</span>
            </div>
            <div className="usage-member-meta">
              {(m.roles ?? []).map((r) => (
                <span className="usage-member-role" key={r}>{r}</span>
              ))}
              {(m.roles ?? []).length > 0 && <span className="usage-member-sep">·</span>}
              <span
                className={`usage-member-tier ${tierBadgeClass(modelTier(m.top_model), m.top_model)}`}
                title={modelLabel(m.top_model)}
              >{modelTier(m.top_model)}</span>
              <span className="usage-member-sep">·</span>
              <span>{fmtInt(m.turns ?? 0)} turns</span>
              <span className="usage-member-sep">·</span>
              <span>{fmtInt(m.sessions ?? 0)} sessions</span>
            </div>
            <div className="usage-member-shareline">
              <span className="usage-share-track usage-member-bar">
                <span
                  className="usage-share-fill usage-share-magenta"
                  style={{ width: pctWidth(m.cost_share) }}
                />
              </span>
              <span className="usage-share-pct">{pct(m.cost_share)}</span>
            </div>
          </div>
        </div>
      ))}
    </div>
  );
}

function PeerStanding({ stats }: { stats: CCUsageTeamSummary['team_peer_stats'] }) {
  if (!stats || !stats.metrics || stats.metrics.length === 0) return null;
  const preferred = ['turns', 'output_tokens', 'cost_per_turn_usd', 'cache_hit_rate', 'cost_usd'];
  const metrics = preferred
    .map((key) => stats.metrics.find((m) => m.key === key))
    .filter((m): m is NonNullable<typeof m> => !!m)
    .slice(0, 4);
  if (metrics.length === 0) return null;
  return (
    <section className="usage-block usage-peer">
      <div className="usage-block-head">
        <h3 className="usage-block-title">Your standing</h3>
        <span className="usage-peer-count">
          anonymous team comparison · {stats.members_active.toLocaleString()} active
        </span>
      </div>
      <div className="usage-peer-grid">
        {metrics.map((m) => {
          const percentile = Math.max(0, Math.min(1, m.percentile ?? 0));
          const rankText = m.rank > 0 && m.rank_total > 0
            ? `#${m.rank.toLocaleString()} of ${m.rank_total.toLocaleString()}`
            : 'not ranked';
          return (
            <div className="usage-peer-card" key={m.key}>
              <div className="usage-peer-top">
                <span className="usage-peer-label">{m.label}</span>
                <span className="usage-peer-rank">{rankText}</span>
              </div>
              <div className="usage-peer-value">
                {fmtColumn(m.format as ColumnSpec['format'], m.value)}
              </div>
              <div className="usage-peer-bar" aria-hidden="true">
                <span style={{ width: pctWidth(percentile) }} />
              </div>
              <div className="usage-peer-meta">
                <span>{m.higher_is_better ? 'higher is better' : 'lower is better'}</span>
                <span>team avg {fmtColumn(m.format as ColumnSpec['format'], m.team_avg)}</span>
              </div>
            </div>
          );
        })}
      </div>
    </section>
  );
}

function ShareBar({ share, variant }: { share: number; variant: 'magenta' | 'violet' }) {
  return (
    <span className="usage-share-track">
      <span
        className={`usage-share-fill usage-share-${variant}`}
        style={{ width: pctWidth(share) }}
      />
    </span>
  );
}

function UsageLoadingSkeleton() {
  return (
    <div className="usage-loading" role="status" aria-live="polite">
      <div className="usage-loading-head">Preparing your usage summary…</div>
      <div className="usage-loading-kpis" aria-hidden="true">
        {[0, 1, 2, 3].map((i) => (
          <div className="usage-loading-card" key={i}>
            <span className="usage-loading-value" />
            <span className="usage-loading-label" />
          </div>
        ))}
      </div>
      <div className="usage-loading-support" aria-hidden="true">
        <span /><span /><span /><span />
      </div>
      <div className="usage-loading-chart" aria-hidden="true">
        {[42, 68, 35, 78, 54, 88, 47, 62, 31, 73, 51, 82].map((height, i) => (
          <span key={i} style={{ height: `${height}%` }} />
        ))}
      </div>
    </div>
  );
}

// A loaded payload tagged with the selection that requested it (usageKey). The
// page renders a payload as CURRENT only when its key matches the active
// selection; a payload for an older selection is shown only while its
// replacement loads, never after that fetch fails (see `data` below).
type Loaded<T> = { key: string; data: T };

// Request identity for a usage payload: window + provider (+ team / member).
// An undefined team id (server picks the primary team) is its own identity.
function usageKey(...parts: Array<string | undefined>): string {
  return parts.map((p) => p ?? '').join('|');
}

// One delayed retry for the team-context probe, so a transient failure on open
// doesn't hide the Team toggle for the whole session.
const TEAM_PROBE_RETRY_MS = 5000;

export function UsagePage({ onClose }: Props) {
  const [windowKey, setWindowKey] = useState<WindowKey>('30d');
  // (Phase 3) View scope: "you" (per-account, default + unchanged) or "team".
  const [scope, setScope] = useState<Scope>('you');
  // Selected team for the team view. undefined = let the server pick the primary
  // team (the one the caller owns, else the first). Only used in team scope.
  const [teamId, setTeamId] = useState<string | undefined>(undefined);
  // (Codex provider) Provider filter, applied to every scope's fetch. "all"
  // sends the unfiltered (pre-provider) URLs.
  const [provider, setProvider] = useState<UsageProvider>('all');
  const providerRef = useRef(provider);
  useEffect(() => {
    providerRef.current = provider;
  }, [provider]);

  // (Phase 1) Time-series view for the daily-chart block — local UI state, no
  // refetch. Shared across all scopes.
  const [seriesView, setSeriesView] = useState<SeriesView>('daily');

  // (Phase 2) Owner team-comparison lens (Output / Efficiency / Cost) + the
  // current sort key/dir. `lens` drives which columns the comparison table and
  // leaderboards show; the sort defaults to the lens's primary column (see the
  // effect below that re-seeds it on a lens change). Only meaningful in the
  // owner team view; harmless otherwise.
  const [lens, setLens] = useState<Lens>('output');
  const [sortKey, setSortKey] = useState<string>('turns');
  const [sortDir, setSortDir] = useState<'asc' | 'desc'>('desc');

  // (Phase 3b) Metric for the owner "Member trends" overlay — cost (USD) or
  // turns (count). Local UI state; drives both the chart valueFormat and the
  // alignMemberDailies metric. Team-scope owner view only.
  const [trendMetric, setTrendMetric] = useState<'cost' | 'turns'>('cost');

  const [you, setYou] = useState<Loaded<CCUsageSummary> | null>(() => {
    const peek = api.ccUsageSummaryPeek('30d', 'all');
    return peek ? { key: usageKey('30d', 'all'), data: peek } : null;
  });
  const [youLoading, setYouLoading] = useState(() => !api.ccUsageSummaryPeek('30d', 'all'));
  const [youError, setYouError] = useState<string | null>(null);

  const [team, setTeam] = useState<Loaded<CCUsageTeamSummary> | null>(null);
  const [teamLoading, setTeamLoading] = useState(false);
  const [teamError, setTeamError] = useState<string | null>(null);
  const teamData = team?.data ?? null;

  // (Phase 3.1) Member drill-down state. `selectedMember` holds the member the
  // owner is viewing (null = not in a member view). The detail itself loads into
  // `member` and re-fetches on window / provider / selected-member change. A 403
  // (the privacy boundary) sets memberForbidden so we can fall back to team scope
  // with a small "not authorized" note instead of an error wall.
  const [selectedMember, setSelectedMember] = useState<SelectedMember | null>(null);
  const [member, setMember] = useState<Loaded<CCUsageMemberSummary> | null>(null);
  const [memberLoading, setMemberLoading] = useState(false);
  const [memberError, setMemberError] = useState<string | null>(null);
  const [memberForbidden, setMemberForbidden] = useState(false);
  const memberData = member?.data ?? null;

  // Team membership is detected through the context-only team endpoint. New
  // clouds resolve only the roster metadata; older clouds may return the full
  // summary and we reuse it. This avoids running a ten-query team aggregation
  // merely because somebody opened their personal dashboard.
  const [hasTeam, setHasTeam] = useState(false);
  // 0 = first probe; 1 = the single delayed retry after a transient failure.
  const [teamProbeAttempt, setTeamProbeAttempt] = useState(0);
  // Bumped by the Retry button on the error wall to re-run the active-scope
  // fetch after its internal transient retries (502/503/504) are exhausted.
  const [reloadKey, setReloadKey] = useState(0);
  // (Codex provider) Sticky once ANY payload showed more than one provider, so
  // the provider toggle doesn't flicker away while a filtered view loads.
  const [multiProvider, setMultiProvider] = useState(false);

  // (Phase 3.1) The team id we resolved for the team view, independent of the
  // active scope (the `teamInfo` derivation below only resolves in team scope).
  // Passed to the member fetch so the ownership check is narrowed to the team the
  // owner drilled from. Falls back to the explicit `teamId` if the user picked one.
  const teamInfoTeamId = teamId ?? teamData?.team?.team_id;

  // The selection each scope is CURRENTLY asking for.
  const youKey = usageKey(windowKey, provider);
  const teamKey = usageKey(windowKey, provider, teamId);
  const memberKey = usageKey(windowKey, provider, teamInfoTeamId, selectedMember?.user_id);

  // The active-scope summary the rest of the page renders from. In "you" scope
  // this is the per-account CCUsageSummary; in "team" scope it's the (shape-
  // compatible) CCUsageTeamSummary; in "member" scope (Phase 3.1) it's the (also
  // shape-compatible) CCUsageMemberSummary, so the entire detail render path
  // works unchanged for all three.
  //
  // A payload for a DIFFERENT selection (previous window / team / provider) is
  // kept on screen only while its replacement is loading (the status chip says
  // "Updating…"). Once that fetch fails it is dropped, so the error wall shows
  // instead of the old numbers rendered under the new selection.
  const loaded = scope === 'member' ? member : scope === 'team' ? team : you;
  const currentKey = scope === 'member' ? memberKey : scope === 'team' ? teamKey : youKey;
  const loading = scope === 'member' ? memberLoading : scope === 'team' ? teamLoading : youLoading;
  const error = scope === 'member' ? memberError : scope === 'team' ? teamError : youError;
  const dataCurrent = !!loaded && loaded.key === currentKey;
  const data: CCUsageSummary | null =
    loaded && (dataCurrent || (loading && !error)) ? loaded.data : null;

  // "You" fetch — re-fetch whenever the window or provider changes. The
  // previous payload stays on screen while the new one loads (see `data`).
  useEffect(() => {
    const force = reloadKey > 0 && scope === 'you';
    const key = usageKey(windowKey, provider);
    const cached = force ? null : api.ccUsageSummaryPeek(windowKey, provider);
    if (cached) {
      setYou({ key, data: cached });
      setYouError(null);
    }
    const ctrl = new AbortController();
    setYouLoading(true);
    setYouError(null);
    api
      .ccUsageSummaryCached(windowKey, { force, signal: ctrl.signal, provider })
      .then((d) => {
        setYou({ key, data: d });
        if (force) setReloadKey(0);
        setYouLoading(false);
      })
      .catch((e) => {
        if (force) setReloadKey(0);
        if (e instanceof DOMException && e.name === 'AbortError') return;
        setYouError((e as Error).message);
        setYouLoading(false);
      });
    return () => ctrl.abort();
  }, [windowKey, reloadKey, scope, provider]);

  // Lightweight team-context probe (always unfiltered, 30d). The query parameter
  // passes through old daemons unchanged. If an older cloud ignores it and
  // returns the full summary, seed the team slot so the first Team click is
  // instant — but ONLY into an EMPTY slot while the unfiltered view is active,
  // so it can never overwrite a newer team fetch (a 7d or provider-filtered
  // load that landed first). A transient failure retries once after a delay.
  useEffect(() => {
    const ctrl = new AbortController();
    let retry: ReturnType<typeof setTimeout> | undefined;
    api
      .ccUsageTeamContextCached('30d', { signal: ctrl.signal })
      .then((d) => {
        if (!d?.team) return;
        setHasTeam(true);
        if (!('context_only' in d) && providerRef.current === 'all') {
          setTeam((prev) => prev ?? { key: usageKey('30d', 'all', undefined), data: d });
        }
      })
      .catch((e) => {
        if (e instanceof DOMException && e.name === 'AbortError') return;
        // Best-effort: personal usage remains fully usable without team context.
        if (teamProbeAttempt === 0) {
          retry = setTimeout(() => setTeamProbeAttempt(1), TEAM_PROBE_RETRY_MS);
        }
      });
    return () => {
      ctrl.abort();
      if (retry) clearTimeout(retry);
    };
  }, [teamProbeAttempt]);

  // (Phase 3) Team fetch — runs only in team scope, and re-fetches on window,
  // provider or selected-team change. Keeps the prior team payload on screen
  // while refreshing, mirroring the "you" path.
  useEffect(() => {
    if (scope !== 'team') return;
    const force = reloadKey > 0;
    const key = usageKey(windowKey, provider, teamId);
    const cached = force ? null : api.ccUsageTeamSummaryPeek(windowKey, teamId, provider);
    if (cached) {
      setTeam({ key, data: cached });
      if (cached?.team) setHasTeam(true);
      setTeamError(null);
    }
    const ctrl = new AbortController();
    setTeamLoading(true);
    setTeamError(null);
    api
      .ccUsageTeamSummaryCached(windowKey, teamId, { force, signal: ctrl.signal, provider })
      .then((d) => {
        setTeam({ key, data: d });
        if (d?.team) setHasTeam(true);
        if (force) setReloadKey(0);
        setTeamLoading(false);
      })
      .catch((e) => {
        if (force) setReloadKey(0);
        if (e instanceof DOMException && e.name === 'AbortError') return;
        setTeamError((e as Error).message);
        setTeamLoading(false);
      });
    return () => ctrl.abort();
  }, [scope, windowKey, teamId, reloadKey, provider]);

  // (Phase 3.1) Member-detail fetch — runs only in member scope, and re-fetches
  // on window, provider or selected-member change (so the toggles keep working
  // while drilled in). Keeps the prior member payload on screen while
  // refreshing, mirroring the "you"/"team" paths. A 403 is the privacy boundary,
  // NOT an error: we mark memberForbidden and bounce back to team scope so the
  // owner sees a friendly note instead of an error wall.
  useEffect(() => {
    if (scope !== 'member' || !selectedMember) return;
    const force = reloadKey > 0;
    const key = usageKey(windowKey, provider, teamInfoTeamId, selectedMember.user_id);
    const cached = !force
      ? api.ccUsageMemberSummaryPeek(selectedMember.user_id, windowKey, teamInfoTeamId, provider)
      : null;
    if (cached) {
      setMember({ key, data: cached });
      setMemberError(null);
    }
    const ctrl = new AbortController();
    setMemberLoading(true);
    setMemberError(null);
    api
      .ccUsageMemberSummaryCached(
        selectedMember.user_id,
        windowKey,
        teamInfoTeamId,
        { force, signal: ctrl.signal, provider },
      )
      .then((d) => {
        setMember({ key, data: d });
        if (force) setReloadKey(0);
        setMemberLoading(false);
      })
      .catch((e) => {
        if (force) setReloadKey(0);
        if (e instanceof DOMException && e.name === 'AbortError') return;
        setMemberLoading(false);
        if (e instanceof ApiError && e.status === 403) {
          // Not authorized to view this member — fall back to team with a note.
          setMemberForbidden(true);
          setSelectedMember(null);
          setMember(null);
          setScope('team');
          return;
        }
        setMemberError((e as Error).message);
      });
    return () => ctrl.abort();
    // teamInfoTeamId derives from teamData (stable while drilled in — no team
    // refetch happens in member scope), so it's intentionally not a dep;
    // selectedMember/windowKey/provider/teamId/reloadKey drive the (re)fetch.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [scope, windowKey, selectedMember, teamId, reloadKey, provider]);

  // (Phase 3.1) Enter a member's detail view from a comparison row. Seeds from
  // the client cache (or clears) so we show "Loading…" for the new member rather
  // than the previous one, resets the forbidden note, and flips scope.
  function openMember(m: SelectedMember) {
    setMemberForbidden(false);
    setMemberError(null);
    const peek = api.ccUsageMemberSummaryPeek(m.user_id, windowKey, teamInfoTeamId, provider);
    setMember(peek ? { key: usageKey(windowKey, provider, teamInfoTeamId, m.user_id), data: peek } : null);
    setSelectedMember(m);
    setScope('member');
  }

  // (Phase 3.1) Back to the team view from a member detail. Leaves the team
  // payload intact (no refetch) and drops the member detail so memory is
  // reclaimed.
  function backToTeam() {
    setScope('team');
    setSelectedMember(null);
    setMember(null);
    setMemberError(null);
  }

  // Dialog focus: move focus into the page on open so keyboard users land in it
  // (the dialog itself is focusable; its controls follow in tab order).
  const dialogRef = useRef<HTMLDivElement>(null);
  useEffect(() => {
    dialogRef.current?.focus({ preventScroll: true });
  }, []);

  // Esc closes — unless it came from a focused form control (the team <select>
  // uses Esc to dismiss its own popup; an input uses it to cancel), so a native
  // control's Esc never also closes the whole page.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key !== 'Escape') return;
      const t = e.target as HTMLElement | null;
      if (t && (/^(SELECT|INPUT|TEXTAREA)$/.test(t.tagName) || t.isContentEditable)) return;
      e.preventDefault();
      onClose();
    };
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
  }, [onClose]);

  // (Codex provider) Remember that more than one provider exists.
  useEffect(() => {
    if ((data?.by_provider?.length ?? 0) > 1) setMultiProvider(true);
  }, [data]);

  // (Codex provider) The filter is active but the payload for it carries no (or
  // a different) provider echo: an older cloud ignored the filter, so its
  // numbers are ALL providers. Show a notice instead of mislabeled numbers.
  const providerUnsupported =
    provider !== 'all' && !!data && dataCurrent && data.provider !== provider;
  const showProviderToggle =
    multiProvider || provider !== 'all' || (data?.by_provider?.length ?? 0) > 1;
  const providerName = provider !== 'all' ? providerLabel(provider) : null;

  const hero = data?.hero;
  const empty = !!data && !providerUnsupported && hero != null && hero.turns === 0;

  const days = useMemo(
    () => (data ? zeroFilledDaily(data.daily, data.window_days, data.tz) : []),
    [data],
  );

  // Phase 2.1 — does any model row carry an assumed tier? Drives the footnote.
  const anyEstimated = useMemo(
    () => !!data?.by_model?.some((m) => m.estimated),
    [data],
  );

  // (item 9) Overall per-class token totals — summed across by_model once and
  // shared by the efficiency strip + the cost-breakdown section (was recomputed
  // inline in render twice). Keyed on `data` (by_model is stable within a load).
  const tokenTotals = useMemo(() => classTokenTotals(data?.by_model), [data]);
  // Per-account breakdown rows (null until a named account exists → card hidden).
  const accountCardRows = useMemo(() => accountRows(data?.by_account), [data]);
  // (Codex provider) Per-provider card rows (null when the cloud sent none).
  const providerCardRows = useMemo(() => providerRows(data?.by_provider), [data]);
  // (Codex provider) Provider chips on the by-model / by-account rows only earn
  // their space when the rows actually span providers (or include Codex).
  const byModelShowsProvider = useMemo(() => {
    const set = new Set((data?.by_model ?? []).map((m) => modelProvider(m.model, m.provider)));
    return set.size > 1 || set.has('codex');
  }, [data]);
  const byAccountShowsProvider = useMemo(() => {
    const set = new Set((accountCardRows ?? []).map((a) => a.provider));
    return set.size > 1 || set.has('codex');
  }, [accountCardRows]);

  // Phase 2.2 — stable model→color map + legend order for the stacked daily
  // chart. Legend order = by_model (cost-DESC from the server) UNION any model
  // that only appears inside daily[].by_model (defensive). Colors are keyed by
  // model NAME (modelColorMap: Claude ramp vs Codex ramp), not by rank, so a
  // model keeps its hue across windows and provider filters. Empty when the
  // response carries no models → chart falls back to the single-color total bar.
  const { modelColors, legendModels } = useMemo(() => {
    const ordered: string[] = [];
    const seen = new Set<string>();
    const colorInput: Array<{ model: string; provider?: string }> = [];
    for (const m of data?.by_model ?? []) {
      colorInput.push({ model: m.model, provider: m.provider });
      if (!seen.has(m.model)) { ordered.push(m.model); seen.add(m.model); }
    }
    for (const d of data?.daily ?? []) {
      if (!d.by_model) continue;
      for (const model of Object.keys(d.by_model)) {
        if (!seen.has(model)) { ordered.push(model); seen.add(model); }
      }
    }
    // Only models that actually appear in any day's by_model belong in the
    // chart legend (a model can have window cost but no daily breakdown on older
    // mixed responses). Keep the cost-DESC order.
    const dailyModels = new Set<string>();
    for (const d of data?.daily ?? []) {
      if (d.by_model) for (const k of Object.keys(d.by_model)) dailyModels.add(k);
    }
    for (const model of ordered) colorInput.push({ model });
    return {
      modelColors: modelColorMap(colorInput),
      legendModels: ordered.filter((m) => dailyModels.has(m)),
    };
  }, [data]);

  // (Phase 1) Per-metric hero deltas vs the prior equal-length window. Only
  // meaningful when `data.previous` is present (absent on older responses / the
  // "all" window / a freshly-tracked account) — the hero cards guard on
  // `showDeltas` before rendering a badge, so an absent `previous` simply hides
  // the affordance. null-safe in the derivation regardless.
  const showDeltas = !!data?.previous;
  const deltas = useMemo(
    () => (data?.hero ? heroDeltas(data.hero, data.previous ?? null) : null),
    [data],
  );
  const costPerTurnDelta = useMemo(() => {
    if (!data?.previous?.hero || !data.hero) return null;
    const current = costPerTurn(data.hero.cost_usd, data.hero.turns);
    const previous = costPerTurn(data.previous.hero.cost_usd, data.previous.hero.turns);
    return deltaPct(current, previous);
  }, [data]);
  const insight = useMemo(
    () => (scope === 'team' || !showDeltas ? null : periodInsight(deltas)),
    [scope, showDeltas, deltas],
  );

  // (Phase 1) Cumulative + model-mix series for the time-series switcher. Both
  // derive from the SAME `daily` data the bar chart uses, so switching views is
  // free (no refetch). Model-mix reuses the chart's legendModels/modelColors.
  const cumulativePoints = useMemo(
    () => (data ? cumulativeSeries(data.daily, data.window_days, data.tz) : []),
    [data],
  );
  const modelMixDays = useMemo(
    () => (data ? modelMixSeries(data.daily, legendModels, data.window_days, data.tz) : []),
    [data, legendModels],
  );

  // (item 6) The model-mix series view is only meaningful when there's a daily
  // by_model breakdown to stack (older daemons omit it → legendModels === []).
  // Hide the toggle option in that case so we never render an empty SVG frame.
  const hasModelMix = legendModels.length > 0;
  const seriesViews = useMemo(
    () => SERIES_VIEWS.filter((v) => v.key !== 'modelmix' || hasModelMix),
    [hasModelMix],
  );
  // If model-mix becomes unavailable (window/data change) while it's selected,
  // fall back to the daily bars so the chart block never goes blank.
  useEffect(() => {
    if (seriesView === 'modelmix' && !hasModelMix) setSeriesView('daily');
  }, [seriesView, hasModelMix]);

  // (Phase 1) Local-time activity heatmap. activityMatrix densifies the sparse
  // cells into a 7×24 LOCAL matrix: older clouds send UTC cells (rotated by the
  // browser offset); a cloud that echoes `tz` already bucketed them locally (no
  // rotation — activityOffsetMinutes returns 0). Hidden when the response
  // carries no `activity` block (older responses) or there's no activity to
  // chart (max <= 0).
  const activity = useMemo(() => {
    if (!data?.activity) return null;
    return activityMatrix(data.activity.cells, activityOffsetMinutes(data.tz));
  }, [data]);
  const showHeatmap = !!activity && activity.max > 0;

  // (Phase 1) "Your share of team" — only in 'you' scope, and ONLY when the team
  // probe already loaded a team rollup (we NEVER add a fetch for this). Your cost
  // / the team-wide total. Omitted when there's no team data on hand or the team
  // total is 0.
  const teamCostTotal = teamData?.hero?.cost_usd ?? 0;
  // The team payload must be for the same window AND provider as the personal
  // view, or the share would compare unlike numbers.
  const yourTeamSharePct =
    scope === 'you' &&
    teamData?.team &&
    teamData.window === windowKey &&
    (teamData.provider ?? 'all') === provider &&
    teamCostTotal > 0 &&
    hero
      ? hero.cost_usd / teamCostTotal
      : null;

  // Phase 2.2 — show the web-tools stat only when present and non-zero. Web
  // tools are a Claude Code server-tool count, so a Codex-only view hides it.
  const webTools = data?.web_tools;
  const showWebTools =
    provider !== 'codex' && !!webTools && (webTools.searches > 0 || webTools.fetches > 0);

  // Phase 2.2 — busiest sessions; hide when absent or empty. Suppressed in team
  // scope: top_sessions is intentionally NOT included at team level in v1 (it
  // would leak per-session attribution), so even if a stray field arrives we
  // never render it for the team view.
  const topSessions = data?.top_sessions;
  const showSessions = scope !== 'team' && !!topSessions && topSessions.length > 0;

  // (Phase 3) Team-scoped views of the active data. `teamData` is the same
  // object as `data` when in team scope, just typed with the team fields. The
  // team meta block is its `team`; the member list / picker read from here.
  const teamInfo = scope === 'team' ? teamData?.team ?? null : null;
  const teams = scope === 'team' ? teamData?.teams ?? [] : [];
  const byMember = scope === 'team' ? teamData?.by_member ?? [] : [];
  const peerStats = scope === 'team' ? teamData?.team_peer_stats ?? null : null;
  const hasPeerStats = !!peerStats?.metrics?.length;

  // (Phase 3c) ONE stable userId→color map, derived from the team roster
  // (cost-DESC). Shared by BOTH the multi-member trend chart and every
  // per-project "who works on what" breakdown bar, so a teammate is the same
  // color everywhere. Keyed on `byMember` (stable within a team load) so it
  // isn't rebuilt every render. Harmless (empty roster → fallback) in non-team
  // scope where neither consumer renders.
  const memberColor = useMemo(() => buildMemberColors(byMember), [byMember]);

  // (Phase 3c) The caller's own user_id within the team roster, used to mark the
  // "you" segment/legend chip in the per-project breakdown. Resolved from the
  // by_member row flagged is_you; undefined when none (non-owner has only their
  // own row, which still carries is_you, so this still resolves for them).
  const youUserId = useMemo(
    () => byMember.find((m) => m.is_you)?.user_id,
    [byMember],
  );
  // Show the team picker only when the caller is in more than one team.
  const showTeamPicker = scope === 'team' && teams.length > 1;
  // Effective selected team id for the <select> — falls back to whatever the
  // server resolved (teamInfo.team_id) when the user hasn't explicitly picked.
  const selectedTeamId = teamId ?? teamInfo?.team_id ?? '';

  // (Phase 3.1) Display name for the member detail title/back UI. Prefer the
  // detail's own `member` meta (authoritative), then the name captured at click
  // time (shows instantly before the fetch resolves), then a generic fallback.
  const memberName =
    memberData?.member?.display_name || selectedMember?.display_name || 'member';

  // (Phase 3) Owner-only per-member drill: the team grants per-member visibility
  // AND we're in team scope with a member list. When false the rows stay plain
  // (a non-owner would be 403'd by the server anyway).
  const canDrillMembers = scope === 'team' && !!teamInfo?.can_see_members;

  // (Phase 2) Owner comparison surface: shown only when the owner can see
  // members AND there's a member list. When false we keep the existing flat
  // ByMember list (the non-owner path is untouched).
  const showComparison = canDrillMembers && byMember.length > 0;

  // (Phase 2) Columns for the active lens. Computed regardless of scope (cheap,
  // pure) so the lens-default-sort effect below has a stable reference.
  const columns = useMemo(() => lensColumns(lens), [lens]);
  const primaryColumn = useMemo(
    () => columns.find((c) => c.primary) ?? columns[0],
    [columns],
  );

  // (Phase 2) On a lens change, re-seed the sort to that lens's primary column
  // and its natural direction (lowerIsBetter → asc "best first", else desc). The
  // user can then re-sort by clicking a header; that override survives until the
  // next lens change.
  useEffect(() => {
    if (!primaryColumn) return;
    setSortKey(primaryColumn.key);
    setSortDir(primaryColumn.lowerIsBetter ? 'asc' : 'desc');
  }, [primaryColumn]);

  // (Phase 2) The active sort column (falls back to the primary if a stale
  // sortKey doesn't match the current lens's columns, e.g. right after a lens
  // flip before the effect lands).
  const sortColumn: ColumnSpec | undefined =
    columns.find((c) => c.key === sortKey) ?? primaryColumn;

  // (item 4) Per-member share of the ACTIVE primary column's metric, normalized
  // to the leader (raw / max(raw)). This is what the primary-cell bar encodes, so
  // under the Output lens the bar reads turns-share and under Cost it reads
  // cost-share — never a metric mismatch. For a lower-is-better primary (cost/
  // turn) a "more = bigger bar" reading is misleading, so we OMIT the bar (the
  // value + the item-8 baseline tint carry the read). Degenerate metric (all 0)
  // → empty map → fall back to the server cost_share so the bar isn't dead.
  const primaryShareMap = useMemo(() => {
    if (!showComparison || !primaryColumn || primaryColumn.lowerIsBetter) {
      return null;
    }
    const m = primaryShares(byMember as MemberRow[], primaryColumn.metric);
    return m.size > 0 ? m : null;
  }, [showComparison, byMember, primaryColumn]);

  // (item 8 + Phase 3a) Per-column raw team baseline. Drives BOTH the formatted
  // "team avg" row AND the per-cell above/below tint (passed into the table
  // columns with each column's lowerIsBetter so good = better-than-avg).
  //
  // (Phase 3a — correctness fix) Ratio/derived columns (cost_per_turn,
  // output_per_turn, cache_hit_rate) use a POOLED team rate (Σnum / Σden across
  // members), NOT a mean of per-member ratios — pooledBaseline() switches on the
  // column's baselineKind, so additive columns still get the mean.
  const baselineRaw = useMemo(() => {
    const out: Record<string, number> = {};
    if (!showComparison) return out;
    for (const c of columns) out[c.key] = pooledBaseline(byMember as MemberRow[], c);
    return out;
  }, [showComparison, byMember, columns]);

  // (Phase 2) Build the comparison rows: sort the by_member rows by the active
  // column, then map each to the dumb table's row shape (pre-formatted cells +
  // raw values + a cost sparkline). Members with the enriched fields absent
  // (older data) still render — the lens metrics fall back to 0 / cost-only.
  const comparisonRows: MemberComparisonRow[] = useMemo(() => {
    if (!showComparison || !sortColumn) return [];
    const sorted = sortMembers(byMember as MemberRow[], sortColumn, sortDir);
    return sorted.map((m) => {
      const cells: MemberComparisonRow['cells'] = {};
      for (const c of columns) {
        const raw = c.metric(m);
        cells[c.key] = { display: fmtColumn(c.format, raw), raw };
      }
      const spark = zeroFilledDaily(m.daily ?? [], data?.window_days ?? 0, data?.tz).map(
        (d) => d.cost_usd,
      );
      // (item 4) Share for the primary-cell bar: the active-metric share when we
      // have one, else the server cost_share fallback, else omitted entirely
      // (lower-is-better primary — primaryShareMap is null there).
      const share = primaryShareMap
        ? primaryShareMap.get(m.user_id) ?? 0
        : primaryColumn && !primaryColumn.lowerIsBetter
          ? m.cost_share
          : undefined;
      // (Phase 3a) Per-member Δ vs prior window on the PRIMARY column's metric
      // (cost / turns / cost-per-turn per lens — the table colours it by that
      // column's direction). DeltaBadge wants PERCENTAGE POINTS, so scale the
      // fraction ×100. null when previous is absent → no badge.
      const md = memberDelta(m, lens);
      const delta = md
        ? { pct: md.pct === null ? null : md.pct * 100, dir: md.dir }
        : null;
      // (Phase 3a) Tier-colored model mix (cost-share normalized). Empty when
      // by_model is absent → no mix bar.
      const modelMix = memberModelMix(m);
      // (Codex provider) "Codex 32%" chip — only in the unfiltered view (a
      // filtered view is single-provider by definition).
      const providerMix = provider === 'all' ? memberProviderMix(m) : null;
      // Last active day from the member's daily series (undefined → older
      // response without daily → no column).
      const la = lastActiveDate(m);
      const lastActive = la === undefined ? undefined : la === null ? '—' : relativeDay(la, undefined, data?.tz);
      return {
        userId: m.user_id,
        name: m.display_name || m.email || '(unknown)',
        isYou: !!m.is_you,
        roles: m.roles ?? [],
        active: (m.turns ?? 0) > 0,
        cells,
        share,
        spark,
        delta,
        modelMix,
        providerMix,
        lastActive,
      };
    });
  }, [showComparison, byMember, columns, sortColumn, sortDir, data, primaryShareMap, primaryColumn, lens, provider]);

  // (Phase 2 + item 8) The dumb table's column descriptors. Carries the raw
  // baseline + lowerIsBetter so the table can tint each numeric cell vs the team.
  const comparisonColumns: MemberComparisonColumn[] = useMemo(
    () =>
      columns.map((c) => ({
        key: c.key,
        label: c.label,
        primary: c.primary,
        baseline: baselineRaw[c.key],
        lowerIsBetter: c.lowerIsBetter,
      })),
    [columns, baselineRaw],
  );

  // (Phase 2) Per-column baseline, formatted like the cells. Renders as the muted
  // "team avg" row so the owner can read each member against the team.
  const comparisonBaseline: Record<string, string> | undefined = useMemo(() => {
    if (!showComparison) return undefined;
    const out: Record<string, string> = {};
    for (const c of columns) out[c.key] = fmtColumn(c.format, baselineRaw[c.key] ?? 0);
    return out;
  }, [showComparison, columns, baselineRaw]);

  // (Phase 3b) Owner at-a-glance narrative — who carries the spend, who's
  // ramping, who's most efficient. Pure + null-safe; each clause is omitted in
  // render when its highlight is null. Only meaningful in the owner comparison
  // surface, so we skip the work entirely when that surface is hidden.
  const digest = useMemo(
    () => (showComparison && teamData ? periodDigest(teamData) : null),
    [showComparison, teamData],
  );

  // (Phase 3b) Member-trends overlay: align every member's daily series onto the
  // shared window spine (alignMemberDailies), then color each series via the ONE
  // stable `memberColor` map (cost-DESC roster order → top spenders get the
  // saturated hues), so a teammate's trend line matches their per-project bar
  // segment exactly. The `is_you` flag is looked up from by_member by user_id.
  // Memoized so the alignment isn't rebuilt every render. Returns null when
  // there's no comparison surface, no spine, or no member series → section hides.
  const memberTrend = useMemo(() => {
    if (!showComparison) return null;
    const aligned = alignMemberDailies(
      byMember as MemberRow[],
      data?.window_days ?? 0,
      trendMetric,
      undefined,
      data?.tz,
    );
    if (aligned.series.length === 0 || aligned.dates.length === 0) return null;
    // user_id → is_you, so a series can carry the "you" chip in the legend.
    const youById = new Set(
      (byMember as MemberRow[]).filter((m) => m.is_you).map((m) => m.user_id),
    );
    const series: TeamTrendSeries[] = aligned.series.map((s) => ({
      userId: s.userId,
      name: s.name,
      color: memberColor(s.userId),
      values: s.values,
      isYou: youById.has(s.userId),
      active: s.active,
    }));
    return { dates: aligned.dates, series };
  }, [showComparison, byMember, data, trendMetric, memberColor]);

  // (Phase 2) Comparison-table sort: clicking the active column flips direction;
  // clicking another column sorts by it in its natural direction (lowerIsBetter
  // → asc "best first", else desc).
  function onSortComparison(key: string) {
    if (key === sortKey) {
      setSortDir((d) => (d === 'asc' ? 'desc' : 'asc'));
      return;
    }
    const col = columns.find((c) => c.key === key);
    setSortKey(key);
    setSortDir(col?.lowerIsBetter ? 'asc' : 'desc');
  }

  // (Phase 2) Comparison-table drill: adapt the table's (userId) callback to the
  // existing member-drill handler, resolving the display name from by_member so
  // the title/back UI can render before the detail loads.
  function onDrillComparison(userId: string) {
    const m = byMember.find((x) => x.user_id === userId);
    if (!m) return;
    openMember({ user_id: userId, display_name: m.display_name || m.email || '' });
  }

  // CSV export is enabled only when there's real, non-empty, CURRENT data loaded.
  const canExport = !!data && dataCurrent && !empty && !loading && !providerUnsupported;

  function refreshActive() {
    if (scope === 'member') setMemberError(null);
    else if (scope === 'team') setTeamError(null);
    else setYouError(null);
    setReloadKey((k) => k + 1);
  }

  // Build CSV from the loaded summary (no network) and download it. In team
  // scope the file gains a by-member sheet and a "team" filename note; in member
  // scope (Phase 3.1) it's the member's own by-model + daily sheets with the
  // member's name in the filename. Filename carries the window + the generated_at
  // date so multiple exports are distinct.
  function onExportCsv() {
    if (!data) return;
    const datePart = (data.generated_at || '').slice(0, 10) || new Date().toISOString().slice(0, 10);
    // Filtered exports carry the provider in the filename (…-codex-30d-…).
    const win = (provider !== 'all' ? provider + '-' : '') + (data.window || windowKey);
    if (scope === 'member') {
      // Member detail → that member's by-model + daily (the per-account sheets);
      // filename notes the member so multiple members' exports don't collide.
      const name = memberData?.member?.display_name || selectedMember?.display_name || 'member';
      const slug = slugify(name) || 'member';
      const csv = CSV_BOM + buildUsageCsv(data);
      downloadText(`soluterminal-usage-${slug}-${win}-${datePart}.csv`, csv, 'text/csv;charset=utf-8');
      return;
    }
    if (scope === 'team' && teamData) {
      const csv = CSV_BOM + buildTeamCsv(teamData);
      const slug = slugify(teamData.team?.slug || teamData.team?.name || 'team') || 'team';
      downloadText(`soluterminal-usage-team-${slug}-${win}-${datePart}.csv`, csv, 'text/csv;charset=utf-8');
      return;
    }
    const csv = CSV_BOM + buildUsageCsv(data);
    downloadText(`soluterminal-usage-${win}-${datePart}.csv`, csv, 'text/csv;charset=utf-8');
  }

  // (Phase 2 + item 7) The owner comparison section, extracted into a single
  // variable so it can be PROMOTED above the personal-style blocks in team scope
  // (an owner opened "Team" to compare people) without duplicating the JSX. Only
  // built when `showComparison` (owner can see members AND there's a list); the
  // non-owner flat ByMember path stays in its original position below.
  const comparisonSection =
    showComparison && sortColumn ? (
      <section className="usage-block">
        <h3 className="usage-block-title">By member</h3>
        {/* (item 1) HONESTY FRAMING — the team-scope analog of the notional-cost
            callout. Ranks/leaderboards read as a scoreboard for an owner, so we
            state plainly that they measure engagement, not output quality. */}
        <div className="usage-compare-honesty" role="note">
          Ranks reflect engagement (volume of turns/tokens), not output quality or
          performance.
        </div>
        {/* (Phase 4a) Owner PERIOD DIGEST — the synthesized "this period vs the
            prior period" glance: spend/activity/efficiency deltas, who's
            accelerating vs cooling, top contributor + most efficient. Subsumes
            the old one-line insight; PeriodDigest renders nothing when there's
            no prior-period basis (older data / "all" window). */}
        <PeriodDigest digest={digest} />
        <LensToggle lens={lens} onChange={setLens} />
        {/* (item 3) Horizontal-scroll wrapper so the multi-column comparison
            table doesn't overflow the page body on narrow/mobile widths. */}
        <div className="usage-table-wrap">
          <MemberComparisonTable
            rows={comparisonRows}
            columns={comparisonColumns}
            sortKey={sortColumn.key}
            sortDir={sortDir}
            onSort={onSortComparison}
            baseline={comparisonBaseline}
            canDrill={canDrillMembers}
            onDrill={canDrillMembers ? onDrillComparison : undefined}
          />
        </div>
        {/* (Phase 3b) Member trends — the per-member trajectory overlay below the
            table, complementing the single Δ badge per row with the SHAPE of each
            member's cost/turns over the window. A small [Cost | Turns] toggle
            switches the metric. Hidden entirely when alignMemberDailies yields no
            series / no daily data (memberTrend === null), so no empty frame. */}
        {memberTrend && (
          <div className="usage-member-trends">
            <div className="usage-block-head">
              <h4 className="usage-member-trends-title">Member trends</h4>
              <div className="usage-metric-toggle" role="group" aria-label="Trend metric">
                <button
                  type="button"
                  className={`usage-metric-btn${trendMetric === 'cost' ? ' active' : ''}`}
                  aria-pressed={trendMetric === 'cost'}
                  onClick={() => setTrendMetric('cost')}
                >Cost</button>
                <button
                  type="button"
                  className={`usage-metric-btn${trendMetric === 'turns' ? ' active' : ''}`}
                  aria-pressed={trendMetric === 'turns'}
                  onClick={() => setTrendMetric('turns')}
                >Turns</button>
              </div>
            </div>
            <TeamTrendChart
              dates={memberTrend.dates}
              series={memberTrend.series}
              metricLabel={trendMetric === 'turns' ? 'daily turns' : 'daily cost'}
              valueFormat={trendMetric === 'turns' ? 'int' : 'usd'}
            />
          </div>
        )}
      </section>
    ) : null;

  // (Phase 3b) The secondary "personal-style" detail blocks — in/out, subagent
  // share, cost-by-token-class, and the pricing table — extracted into ONE
  // fragment so they render identically whether expanded (personal/member scope)
  // or grouped under a collapsible in team scope (item 3). Built only when there's
  // data + hero (the guard the parent JSX already enforces); the individual blocks
  // keep their own presence guards (cost_breakdown / pricing may be absent).
  // in_out / subagent_share are required by the contract but guarded anyway (a
  // partial response must not crash the page). Subagents are a Claude Code
  // concept, so the subagent stat hides in a Codex-only view.
  const showInOut = !!data?.in_out?.in_app && !!data?.in_out?.out_app;
  const showSubagent = provider !== 'codex' && !!data?.subagent_share;
  const costDetailBlocks =
    data && hero ? (
      <>
        {/* Two small labeled stats: in_app vs out_app + subagent share. */}
        {(showInOut || showSubagent) && (
        <div className="usage-ministats">
          {showInOut && (
          <div className="usage-ministat">
            <h3 className="usage-block-title">In-app vs out-of-app</h3>
            <div className="usage-ministat-rows">
              <div className="usage-ministat-row">
                <span className="usage-ministat-label">in soluterminal</span>
                <span className="usage-ministat-val">
                  {fmtUSD(data.in_out.in_app.cost_usd)}
                  <span className="usage-ministat-sub"> · {fmtInt(data.in_out.in_app.turns)} turns</span>
                </span>
              </div>
              <div className="usage-ministat-row">
                <span className="usage-ministat-label">outside</span>
                <span className="usage-ministat-val">
                  {fmtUSD(data.in_out.out_app.cost_usd)}
                  <span className="usage-ministat-sub"> · {fmtInt(data.in_out.out_app.turns)} turns</span>
                </span>
              </div>
            </div>
            {/* Honesty rule #3 — label the post-tracking caveat. */}
            <div className="usage-ministat-note">
              within selected window · reflects soluterminal-tracked sessions
            </div>
          </div>
          )}

          {showSubagent && (
          <div className="usage-ministat">
            <h3 className="usage-block-title">Subagent share</h3>
            <div className="usage-ministat-rows">
              <div className="usage-ministat-row">
                <span className="usage-ministat-label">subagent cost</span>
                <span className="usage-ministat-val">{fmtUSD(data.subagent_share.cost_usd)}</span>
              </div>
              <div className="usage-ministat-row">
                <span className="usage-ministat-label">share of total</span>
                <span className="usage-ministat-val">{pct(data.subagent_share.cost_share)}</span>
              </div>
            </div>
            <div className="usage-ministat-shareline">
              <ShareBar share={data.subagent_share.cost_share} variant="violet" />
            </div>
          </div>
          )}
        </div>
        )}

        {/* Phase 2.1 — overall per-token-class cost breakdown. Absent on
            older responses → hide the section entirely. */}
        {data.cost_breakdown && (
          <section className="usage-block">
            <h3 className="usage-block-title">Cost by token class</h3>
            <CostBreakdown breakdown={data.cost_breakdown} tokenTotals={tokenTotals} />
          </section>
        )}

        {/* Per-account breakdown (per provider login). Hidden until at least one
            named account exists (accountCardRows is null for all-unattributed,
            e.g. right after install before any daemon has attributed). Reuses
            ministat rows; rows group by provider+account, so the key does too. */}
        {accountCardRows && (
          <section className="usage-block">
            <h3 className="usage-block-title">By account</h3>
            <div className="usage-ministat-rows">
              {accountCardRows.map((a) => (
                <div className="usage-ministat-row" key={`${a.provider}:${a.account || '__unattributed'}`}>
                  <span className="usage-ministat-label">
                    {a.label}
                    {byAccountShowsProvider && (
                      <span className={`usage-provider-chip usage-provider-chip-${a.provider}`}>
                        {providerLabel(a.provider)}
                      </span>
                    )}
                  </span>
                  <span className="usage-ministat-val">
                    {fmtUSD(a.cost_usd)}
                    <span className="usage-ministat-sub">
                      {' '}· {fmtInt(a.turns)} turns · {pct(a.cost_share)}
                    </span>
                  </span>
                </div>
              ))}
            </div>
            <div className="usage-ministat-note">
              which login produced each turn · captured for sessions started after v0.0.764; older turns show as Unattributed
            </div>
          </section>
        )}

        {/* Phase 2.1 — collapsible price table, rendered straight from the
            response pricing object (never hardcoded). Hidden if absent. */}
        {data.pricing && (
          <details className="usage-pricing">
            <summary className="usage-pricing-summary">
              Pricing{data.pricing.as_of ? ` (as of ${data.pricing.as_of})` : ''}
            </summary>
            <div className="usage-pricing-body">
              <table className="usage-table usage-pricing-table">
                <thead>
                  <tr>
                    <th>Tier</th>
                    {PRICE_COLS.map((c) => (
                      <th key={c.key} className="num">{c.label}</th>
                    ))}
                  </tr>
                </thead>
                {/* (Codex provider) One tbody per provider, each headed by its
                    name; OpenAI has a single cache-write rate, so its 1h column
                    reads "—" (cache_write_single). */}
                {priceTierGroups(data.pricing.tiers ?? {}).map((g, _gi, groups) => (
                  <tbody key={g.provider}>
                    {groups.length > 1 && (
                      <tr className="usage-pricing-group">
                        <th colSpan={PRICE_COLS.length + 1} scope="rowgroup">{g.label}</th>
                      </tr>
                    )}
                    {g.keys.map((t) => {
                      const row = data.pricing!.tiers[t];
                      if (!row) return null;
                      return (
                        <tr key={t}>
                          <td>
                            <span className={`usage-model-name ${tierBadgeClass(t)}`} title={t}>
                              {row.label || t}
                            </span>
                          </td>
                          {PRICE_COLS.map((c) => (
                            <td key={c.key} className="num">
                              {c.key === 'cache_write_1h' && row.cache_write_single ? '—' : fmtPrice(row[c.key])}
                            </td>
                          ))}
                        </tr>
                      );
                    })}
                  </tbody>
                ))}
              </table>
              <div className="usage-pricing-foot">
                {data.pricing.currency || 'USD'} per million tokens
                {data.pricing.assumptions ? ` · ${data.pricing.assumptions}` : ''}
              </div>
            </div>
          </details>
        )}
      </>
    ) : null;

  // (Phase 3) Team context line — "team <name> · N of M members active". Only in
  // team scope with team meta; shared by the data view and the idle-window
  // empty state.
  const teamMetaLine =
    scope === 'team' && teamInfo ? (
      <div className="usage-team-meta">
        <span className="usage-team-name">team {teamInfo.name}</span>
        <span className="usage-team-meta-sep">·</span>
        <span>
          {fmtInt(teamInfo.members_active ?? 0)} of {fmtInt(teamInfo.members_total ?? 0)} {teamInfo.members_total === 1 ? 'member' : 'members'} active
        </span>
        {teamInfo.is_owner && (
          <>
            <span className="usage-team-meta-sep">·</span>
            <span className="usage-team-owner">owner</span>
          </>
        )}
      </div>
    ) : null;

  // (Phase 3) By-member breakdown for the NON-owner path. The server has
  // already redacted the list (a non-owner sees only their own row) and the
  // team AGGREGATE stays fully visible; when detail is owner-gated we add a
  // muted note explaining why. The OWNER path is the comparison table
  // (`comparisonSection`), so the rows here are never drillable.
  const memberListSection =
    scope === 'team' && teamInfo && !showComparison ? (
      <>
        {!teamInfo.can_see_members && hasPeerStats && <PeerStanding stats={peerStats} />}
        <section className="usage-block">
          <h3 className="usage-block-title">By member</h3>
          {byMember.length === 0 ? (
            <div className="usage-page-empty-inline">No member activity in this window.</div>
          ) : (
            <ByMember members={byMember} />
          )}
          {!teamInfo.can_see_members && (
            <div className="usage-members-note">
              {hasPeerStats
                ? 'Owner-only detail is hidden; comparison above is anonymous.'
                : 'Owner-only detail is hidden.'}
            </div>
          )}
        </section>
      </>
    ) : null;

  // Empty-window copy. A provider filter names the provider; an idle TEAM
  // window that still has a roster says so (the roster renders below it)
  // rather than implying data is still being gathered.
  const emptyMessage =
    scope === 'member'
      ? `No ${providerName ? providerName + ' ' : ''}usage for ${memberName} in this window`
      : providerName
        ? `No ${providerName} usage in this window`
        : scope === 'team'
          ? byMember.length > 0
            ? 'No team usage in this window'
            : 'still gathering your team’s usage'
          : 'still gathering your usage';

  // Portaled to <body>: the page opens from the sidebar, which on mobile sits
  // inside the transformed drawer — a transform makes the drawer the containing
  // block for position:fixed, so without the portal this rendered squeezed
  // inside the ~340px drawer box on phones.
  return createPortal(
    <div
      className="usage-page-backdrop"
      onClick={(e) => {
        // Close only on a direct click on the backdrop itself. onClick (not
        // onMouseDown) + a target check means a press that started as a text
        // selection inside the panel and dragged out to release over the
        // backdrop does NOT dismiss the page — onMouseDown closed on press.
        if (e.target === e.currentTarget) onClose();
      }}
    >
      <div
        ref={dialogRef}
        className="usage-page"
        role="dialog"
        aria-modal="true"
        aria-label="Usage"
        tabIndex={-1}
        onClick={(e) => e.stopPropagation()}
      >
        <div className="usage-page-header">
          {/* (Phase 3.1) Member-detail breadcrumb — only while drilled into a
              member. Returns to the team view (no refetch). Placed before the
              title so it reads "‹ Back to team / <member> · usage". */}
          {scope === 'member' && (
            <button
              type="button"
              className="usage-breadcrumb"
              onClick={backToTeam}
              title="Back to team"
            >‹ Back to team</button>
          )}
          <h2 className="usage-page-title">
            {scope === 'member'
              ? `${memberName} · usage`
              : scope === 'team'
                ? 'Team usage'
                : 'Usage'}
          </h2>
          {/* (Phase 3) Scope toggle — shown ONLY when the mount probe found the
              caller is in a team. Defaults to "You" (existing behavior). In a
              member drill it stays visible and acts as a shortcut back out (You
              leaves to the personal view; Team returns to the team rollup). The
              first segment carries the auto-margin so the whole control cluster
              right-aligns regardless of whether this toggle is present. */}
          {hasTeam && (
            <div className="usage-scope-toggle" role="group" aria-label="Scope">
              <button
                type="button"
                className={`usage-scope-btn${scope === 'you' ? ' active' : ''}`}
                aria-pressed={scope === 'you'}
                onClick={() => { setSelectedMember(null); setMember(null); setMemberForbidden(false); setScope('you'); }}
              >You</button>
              <button
                type="button"
                className={`usage-scope-btn${scope === 'team' ? ' active' : ''}`}
                aria-pressed={scope === 'team'}
                onClick={() => { setSelectedMember(null); setMember(null); setScope('team'); }}
                onPointerEnter={() => api.ccUsageTeamSummaryPrefetch(windowKey, teamId, provider)}
                onFocus={() => api.ccUsageTeamSummaryPrefetch(windowKey, teamId, provider)}
              >Team</button>
            </div>
          )}
          {/* (Phase 3) Team picker — only when the caller is in >1 team. */}
          {showTeamPicker && (
            <select
              className="usage-team-select"
              aria-label="Team"
              value={selectedTeamId}
              onChange={(e) => setTeamId(e.target.value || undefined)}
            >
              {teams.map((t) => (
                <option key={t.team_id} value={t.team_id}>{t.name}</option>
              ))}
            </select>
          )}
          {/* (Codex provider) Provider filter — shown once usage from more than
              one provider exists, or while a filter is active (so it can always
              be cleared). Carries the cluster's auto-margin when it leads it. */}
          {showProviderToggle && (
            <ProviderToggle
              provider={provider}
              onChange={setProvider}
              className={hasTeam ? 'has-scope' : ''}
            />
          )}
          <div
            className={`usage-window-toggle${hasTeam || showProviderToggle ? ' has-scope' : ''}`}
            role="group"
            aria-label="Window"
          >
            {WINDOWS.map((w) => (
              <button
                key={w.key}
                type="button"
                className={`usage-window-btn${windowKey === w.key ? ' active' : ''}`}
                aria-pressed={windowKey === w.key}
                onClick={() => setWindowKey(w.key)}
              >{w.label}</button>
            ))}
          </div>
          {/* Phase 2.2 — CSV export. Builds from the loaded summary (no network);
              disabled while loading or when there's no data. */}
          <button
            type="button"
            className="usage-export-btn"
            onClick={onExportCsv}
            disabled={!canExport}
            title="Download this view as CSV"
          >Export CSV</button>
          <button
            type="button"
            className="usage-page-close"
            onClick={onClose}
            title="Close (Esc)"
            aria-label="Close"
          >×</button>
        </div>

        {/* Honesty rule #2 — prominent notional framing callout. This is the
            single most important honesty cue on the page: every dollar figure
            below is an estimated API-equivalent figure, NOT a charge. */}
        <div className="usage-notional-callout" role="note">
          <span className="usage-notional-callout-icon" aria-hidden="true">≈</span>
          <span className="usage-notional-callout-text">
            <strong>API-equivalent estimate, not a bill.</strong> Dollar values use published
            API prices for comparison; your flat subscription is not charged from this view.
          </span>
        </div>

        <div className="usage-page-body">
          {/* Error wall ONLY when there's no data for the CURRENT selection. A
              refresh of the same selection that fails keeps its data on screen
              and degrades to the inline status chip below, so a transient relay
              blip never blanks the page; a failed SWITCH (window / team /
              provider) lands here instead of showing the old selection's
              numbers under the new one. The internal transient
              retries (502/503/504) are already exhausted by the time we land
              here, so the Retry button is the manual last resort. */}
          {error && !data && (
            <div className="usage-page-state usage-page-state-error">
              <div>Couldn't load usage: {error}</div>
              <button
                type="button"
                className="usage-retry-btn"
                onClick={refreshActive}
              >Retry</button>
            </div>
          )}

          {/* No payload for this selection yet (and no error) → skeleton. Also
              covers the frame between a selection change and its fetch start. */}
          {!error && !data && (
            <UsageLoadingSkeleton />
          )}

          {/* (Codex provider) The cloud ignored the provider filter (no / wrong
              echo) → its numbers are ALL providers; never show them as one. */}
          {providerUnsupported && (
            <div className="usage-page-state usage-provider-unsupported" role="note">
              <div>Provider filter needs the latest cloud</div>
              <button
                type="button"
                className="usage-retry-btn"
                onClick={() => setProvider('all')}
              >Show all providers</button>
            </div>
          )}

          {empty && (
            <div className="usage-page-state">{emptyMessage}</div>
          )}

          {/* An idle TEAM window still shows who's on the team (inactive rows)
              instead of a bare "no usage" note. */}
          {empty && scope === 'team' && teamInfo && (
            <>
              {teamMetaLine}
              {comparisonSection ?? memberListSection}
            </>
          )}

          {/* (Phase 3.1) Not-authorized note — set when a member drill returned
              403 and we fell back to team scope. Cleared on the next successful
              drill or on a manual scope switch. */}
          {scope === 'team' && memberForbidden && (
            <div className="usage-page-state usage-page-state-error usage-member-forbidden">
              You're not authorized to view that member's detail.
            </div>
          )}

          {data && hero && !empty && !providerUnsupported && (
            <>
              <div className="usage-data-status" aria-live="polite">
                <span>
                  {loading
                    ? 'Updating while you browse…'
                    : error
                      ? 'Update delayed — showing the last successful result'
                      : data._stale
                        ? 'Showing recent cached data while the source refreshes'
                        : fmtUpdated(data.generated_at)}
                </span>
                <button
                  type="button"
                  className="usage-refresh-btn"
                  onClick={refreshActive}
                  disabled={loading}
                >{loading ? 'Refreshing…' : 'Refresh'}</button>
              </div>

              {/* (Phase 3) Team context line — "members active N · team <name>". */}
              {teamMetaLine}

              {/* (Phase 3.1) Member context line — the drilled member's email +
                  roles. Only in member scope and only once the meta is loaded. */}
              {scope === 'member' && memberData?.member && (
                <div className="usage-team-meta">
                  <span className="usage-team-name">{memberData.member.display_name || memberName}</span>
                  {memberData.member.email && (
                    <>
                      <span className="usage-team-meta-sep">·</span>
                      <span title={memberData.member.email}>{memberData.member.email}</span>
                    </>
                  )}
                  {(memberData.member.roles ?? []).map((r) => (
                    <span className="usage-member-role" key={r}>{r}</span>
                  ))}
                  {memberData.member.is_you && (
                    <>
                      <span className="usage-team-meta-sep">·</span>
                      <span className="usage-team-owner">you</span>
                    </>
                  )}
                </div>
              )}

              {/* (installer visibility) The drilled member's machines: what
                  they're running (installer + daemon version) and whether it's
                  online. Owner-gated by the same rule as this whole detail. */}
              {scope === 'member' && memberData && (
                <div className="usage-machines-section">
                  <div className="usage-machines-heading">Machines</div>
                  <MemberMachines machines={memberData.machines ?? []} />
                </div>
              )}

              {/* Four decision KPIs. Supporting counts live in the compact strip
                  below so every value no longer competes at the same weight. */}
              <div className="usage-hero">
                <div className="usage-hero-card primary">
                  <div className="usage-hero-valrow">
                    <span className="usage-hero-val">{fmtUSD(hero.cost_usd)}</span>
                    {/* (item 11) Nothing is billed (flat subscription), so a cost
                        rise is informational, not alarming — render the cost delta
                        NEUTRAL (direction glyph, neutral tone) rather than red. */}
                    {showDeltas && deltas && (
                      <DeltaBadge
                        pct={deltas.cost_usd.pct === null ? null : deltas.cost_usd.pct * 100}
                        dir={deltas.cost_usd.dir}
                        neutral
                      />
                    )}
                  </div>
                  <div className="usage-hero-lbl">est. cost</div>
                  {/* (Codex provider) Part of the cost is priced on an assumed
                      tier (unknown model ids) — say how much. */}
                  {(hero.estimated_cost_usd ?? 0) > 0 && (
                    <div className="usage-hero-note">
                      includes {fmtUSD(hero.estimated_cost_usd)} at assumed pricing
                    </div>
                  )}
                </div>
                <div className="usage-hero-card">
                  <div className="usage-hero-valrow">
                    <span className="usage-hero-val">{fmtInt(hero.turns)}</span>
                    {showDeltas && deltas && (
                      <DeltaBadge
                        pct={deltas.turns.pct === null ? null : deltas.turns.pct * 100}
                        dir={deltas.turns.dir}
                      />
                    )}
                  </div>
                  <div className="usage-hero-lbl">turns</div>
                </div>
                <div className="usage-hero-card">
                  <div className="usage-hero-valrow">
                    <span className="usage-hero-val">{fmtPerTurn(costPerTurn(hero.cost_usd, hero.turns))}</span>
                    {costPerTurnDelta && (
                      <DeltaBadge
                        pct={costPerTurnDelta.pct === null ? null : costPerTurnDelta.pct * 100}
                        dir={costPerTurnDelta.dir}
                        goodWhen="down"
                      />
                    )}
                  </div>
                  <div className="usage-hero-lbl">cost / turn</div>
                </div>
                <div className="usage-hero-card">
                  <div className="usage-hero-valrow">
                    <span className="usage-hero-val">{pct(hero.cache_hit_rate ?? 0)}</span>
                    {showDeltas && deltas && (
                      <DeltaBadge
                        pct={deltas.cache_hit_rate.pct === null ? null : deltas.cache_hit_rate.pct * 100}
                        dir={deltas.cache_hit_rate.dir}
                      />
                    )}
                  </div>
                  <div className="usage-hero-lbl">cache hit rate</div>
                </div>
              </div>

              <div className="usage-supporting-stats" role="group" aria-label="Supporting usage totals">
                <span><strong>{fmtInt(hero.sessions)}</strong> sessions</span>
                <span><strong>{fmtInt(hero.active_days)}</strong> active days</span>
                <span><strong>{fmtTokens(hero.fresh_tokens)}</strong> fresh tokens</span>
                <span><strong>{fmtTokens(hero.cached_tokens)}</strong> cached tokens</span>
                <span><strong>{fmtInt(hero.models_used)}</strong> models</span>
              </div>

              {insight && <div className="usage-period-insight" role="note">{insight}</div>}

              {/* (Codex provider) Claude Code vs Codex split. A row click
                  narrows the whole page to that provider (again → all). */}
              {providerCardRows && (
                <ByProviderCard rows={providerCardRows} active={provider} onSelect={setProvider} />
              )}

              {/* (item 7) PROMOTED owner comparison — in team scope an owner
                  opened "Team" to compare people, so the comparison block leads
                  (right after the team-meta + hero) and the personal-style blocks
                  below are demoted. Same JSX as the non-promoted path (the
                  variable), never duplicated. */}
              {scope === 'team' && comparisonSection}

              {/* (Phase 1) Your share of the team's spend — 'you' scope only, and
                  ONLY when the team probe already loaded a rollup (no new fetch).
                  Omitted entirely when there's no team total on hand. */}
              {yourTeamSharePct != null && (
                <div className="usage-team-share" role="note">
                  You are <strong>{pct(yourTeamSharePct)}</strong> of your team's spend this window.
                </div>
              )}

              {/* Phase 2.2 — web-tools call counts. COUNTS, not dollars (we
                  don't have their token cost). Hidden when absent or both 0. */}
              {showWebTools && webTools && (
                <div className="usage-webtools" role="note">
                  <span className="usage-webtools-icon" aria-hidden="true">⌕</span>
                  <span className="usage-webtools-text">
                    <strong>{webTools.searches.toLocaleString()}</strong> web {webTools.searches === 1 ? 'search' : 'searches'}
                    {' · '}
                    <strong>{webTools.fetches.toLocaleString()}</strong> {webTools.fetches === 1 ? 'fetch' : 'fetches'}
                  </span>
                </div>
              )}

              {/* Daily cost bar chart — stacked by model (Phase 2.2). (Phase 1) A
                  small segmented switch picks the view: the existing per-day bars
                  (unchanged), the running-total spend line, or a 100%-stacked
                  model-mix area. All three render from the SAME loaded `daily`
                  data — switching is free (no refetch). */}
              <section className="usage-block">
                <div className="usage-block-head">
                  <h3 className="usage-block-title">
                    {seriesView === 'cumulative'
                      ? 'Cumulative cost'
                      : seriesView === 'modelmix'
                        ? 'Model mix'
                        : 'Daily cost'}
                  </h3>
                  <div className="usage-series-toggle" role="group" aria-label="Chart view">
                    {seriesViews.map((v) => (
                      <button
                        key={v.key}
                        type="button"
                        className={`usage-series-btn${seriesView === v.key ? ' active' : ''}`}
                        aria-pressed={seriesView === v.key}
                        onClick={() => setSeriesView(v.key)}
                      >{v.label}</button>
                    ))}
                  </div>
                </div>
                {seriesView === 'cumulative' ? (
                  <CumulativeSpendChart points={cumulativePoints} />
                ) : seriesView === 'modelmix' ? (
                  <ModelMixTrendChart days={modelMixDays} modelColors={modelColors} legendModels={legendModels} />
                ) : (
                  <DailyChart days={days} modelColors={modelColors} legendModels={legendModels} />
                )}
              </section>

              {/* (Phase 1) Activity heatmap — hour-of-day × day-of-week intensity,
                  in local time (rotated UTC→local only for older clouds that don't
                  echo `tz`). Scope-titled. Hidden when the response carries
                  no `activity` block (older responses) or there's no activity to
                  chart. The local-tz framing is noted in the caption. */}
              {showHeatmap && activity && (
                <details className="usage-analysis-detail">
                  <summary className="usage-analysis-detail-summary">
                    {scope === 'member'
                      ? `When ${memberName} works`
                      : scope === 'team'
                        ? 'When the team works'
                        : 'When you work'}
                  </summary>
                  <div className="usage-analysis-detail-body">
                    <ActivityHeatmap matrix={activity.matrix} max={activity.max} metricLabel="turns" />
                    <div className="usage-heatmap-caption">turns by hour and weekday · your local time</div>
                  </div>
                </details>
              )}

              {/* By-model table. */}
              <section className="usage-block">
                <h3 className="usage-block-title">By model</h3>
                {(data.by_model ?? []).length === 0 ? (
                  <div className="usage-page-empty-inline">No model activity in this window.</div>
                ) : (
                  <>
                    {/* (item 3) Horizontal-scroll wrapper so the table doesn't
                        overflow the page body on narrow/mobile widths. */}
                    <div className="usage-table-wrap">
                    <table className="usage-table">
                      <thead>
                        <tr>
                          <th>Model</th>
                          <th className="num">Turns</th>
                          <th className="num">Tokens</th>
                          <th className="num">Cost</th>
                          <th className="num">Cost / turn</th>
                          <th className="share-col">Share</th>
                        </tr>
                      </thead>
                      <tbody>
                        {data.by_model.map((m) => (
                          <tr key={`${m.provider ?? ''}:${m.model}`}>
                            <td>
                              <span className="usage-model-name" title={modelLabel(m.model, m.provider)}>{m.model}</span>
                              {byModelShowsProvider && (
                                <span
                                  className={`usage-provider-chip usage-provider-chip-${modelProvider(m.model, m.provider)}`}
                                >{providerLabel(modelProvider(m.model, m.provider))}</span>
                              )}
                              {/* Phase 2.1 — mark rows whose tier (and thus
                                  price) is an assumption so a notional number is
                                  never mistaken for a published-price one. */}
                              {m.estimated && (
                                <span className="usage-model-est" title="Tier (and price) assumed — see note below">est.*</span>
                              )}
                              <span className={`usage-model-tier ${tierBadgeClass(m.tier, m.model)}`}>
                                {isCodexModel(m.model) ? modelTier(m.model) : m.tier}
                              </span>
                            </td>
                            <td className="num">{fmtInt(m.turns)}</td>
                            {/* Total tokens for the row (all four classes). The
                                per-class breakdown lives in the "Cost by token
                                class" section + the CSV export. */}
                            <td className="num" title="input + output + cache read + cache write">
                              {fmtTokens(
                                (m.input_tokens ?? 0) +
                                  (m.output_tokens ?? 0) +
                                  (m.cache_read_tokens ?? 0) +
                                  (m.cache_create_tokens ?? 0),
                              )}
                            </td>
                            <td className="num">{fmtUSD(m.cost_usd)}</td>
                            <td className="num">{fmtPerTurn(m.cost_per_turn_usd)}</td>
                            <td className="share-col">
                              <div className="usage-share-cell">
                                <ShareBar share={m.cost_share} variant="magenta" />
                                <span className="usage-share-pct">{pct(m.cost_share)}</span>
                              </div>
                            </td>
                          </tr>
                        ))}
                      </tbody>
                    </table>
                    </div>
                    {anyEstimated && (
                      <div className="usage-table-note">
                        <span className="usage-model-est">est.*</span>{' '}
                        {data.pricing?.assumptions
                          ? data.pricing.assumptions
                          : 'These models have no published API price, so their tier (and price) is assumed (unknown → opus, conservative).'}
                      </div>
                    )}
                  </>
                )}
              </section>

              {/* Top projects. */}
              <section className="usage-block">
                <h3 className="usage-block-title">Top projects</h3>
                {(data.top_projects ?? []).length === 0 ? (
                  <div className="usage-page-empty-inline">No project activity in this window.</div>
                ) : (
                  <div className="usage-projects">
                    {data.top_projects.map((p) => (
                      // (Phase 3c) Each project is now a small block: the existing
                      // name/repo/cost/turns row, PLUS — in the OWNER team view —
                      // a member-colored "who works on what" breakdown indented
                      // beneath it. projectMembers returns [] (→ ProjectMembersBar
                      // renders null) whenever the entry has no owner-gated
                      // by_member (solo / member / non-owner), so those views are
                      // visually unchanged.
                      <div className="usage-project-block" key={p.project}>
                        <div className="usage-project-row">
                          <div className="usage-project-name-cell">
                            <span className="usage-project-name" title={p.project}>{p.project}</span>
                            {p.repo_url ? (
                              <a
                                className="usage-project-repo"
                                href={p.repo_url}
                                target="_blank"
                                rel="noopener noreferrer"
                                title={`Open ${p.repo} on GitHub`}
                              >
                                ↗ {p.repo}
                              </a>
                            ) : null}
                          </div>
                          <ShareBar share={p.cost_share} variant="violet" />
                          <span className="usage-project-cost">{fmtUSD(p.cost_usd)}</span>
                          <span className="usage-project-turns">{fmtInt(p.turns)} turns</span>
                        </div>
                        <ProjectMembersBar members={projectMembers(p, memberColor, youUserId)} />
                      </div>
                    ))}
                  </div>
                )}
              </section>

              {/* (Phase 3) Non-owner By-member list (see memberListSection). */}
              {memberListSection}

              {/* Busiest sessions (Phase 2.2). Hidden when absent or empty, and
                  always hidden in team scope (no per-session attribution). */}
              {showSessions && topSessions && (
                <details className="usage-analysis-detail">
                  <summary className="usage-analysis-detail-summary">Busiest sessions</summary>
                  <div className="usage-analysis-detail-body">
                    <BusiestSessions sessions={topSessions} />
                  </div>
                </details>
              )}

              {/* (Phase 3b · item 3) The secondary personal-style detail —
                  in/out, subagent, cost-by-token-class, and the pricing table.
                  In TEAM scope an owner came to compare people, so we GROUP these
                  under one collapsible to keep the comparison/trends/leaderboards
                  above the fold; personal + member scope render them expanded and
                  unchanged. Same JSX (the `costDetailBlocks` fragment) either way
                  — never duplicated. */}
              <details className="usage-cost-detail">
                <summary className="usage-cost-detail-summary">Cost &amp; token detail</summary>
                <div className="usage-cost-detail-body">
                  {costDetailBlocks}
                </div>
              </details>
            </>
          )}
        </div>
      </div>
    </div>,
    document.body,
  );
}
