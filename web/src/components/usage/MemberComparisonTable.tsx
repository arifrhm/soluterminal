import { MiniSparkline } from './MiniSparkline';
import { DeltaBadge } from './DeltaBadge';
import { isCodexModel, type DeltaDir, type ModelMixSeg } from '../../lib/usage';

// (Phase 3a) Tier → CSS class for the per-member model-mix bar segments. Keeps
// the magenta/violet family (opus = brightest magenta, down to haiku = muted
// violet) so "Opus-heavy vs Haiku-heavy" reads at a glance without touching the
// reserved status color slots. (Codex provider) Any OpenAI tier/model gets the
// Codex hue so a mixed Claude/Codex member reads at a glance. Unknown/other
// tiers fall back to a neutral fill.
export function tierMixClass(tier: string, model?: string): string {
  if (tier !== 'other' && (isCodexModel(tier) || isCodexModel(model))) return 'usage-mix-codex';
  switch ((tier || '').toLowerCase()) {
    case 'opus': return 'usage-mix-opus';
    case 'sonnet': return 'usage-mix-sonnet';
    case 'haiku': return 'usage-mix-haiku';
    case 'fable': return 'usage-mix-fable';
    default: return 'usage-mix-other';
  }
}

// (Phase 3a) Compact tier-colored stacked bar of a member's model mix. DUMB: the
// caller normalized the segments (memberModelMix) so they sum to ~1. Renders
// nothing for an empty mix (older response / non-owner). A per-segment title
// surfaces the model + its share on hover.
function ModelMixBar({ segs }: { segs: ModelMixSeg[] }) {
  if (segs.length === 0) return null;
  return (
    <span className="usage-mix-bar" role="img" aria-label="model mix">
      {segs.map((s, i) =>
        s.share > 0 ? (
          <span
            key={`${s.model}-${i}`}
            className={`usage-mix-seg ${tierMixClass(s.tier, s.model)}`}
            style={{ width: (s.share * 100).toFixed(1) + '%' }}
            title={`${s.model}: ${Math.round(s.share * 100)}%`}
          />
        ) : null,
      )}
    </span>
  );
}

// Owner team-view comparison table — one sortable row per member, a cell per
// active-lens column. DUMB/presentational: ALL sorting + lens + baseline math is
// done by the caller; this component only REFLECTS `sortKey`/`sortDir`, paints
// the cells the caller computed, and calls `onSort`/`onDrill` on interaction.
//
// Each row carries pre-formatted cell displays (`cells[key].display`) plus the
// raw number (`raw`) for the per-cell baseline tint. The PRIMARY column draws a
// share bar from `share` (computed by the caller from the active metric — NOT
// always cost) and the member's sparkline (`spark`). For a lower-is-better
// primary column the caller omits `share` (a bar would be misleading) and we
// rely on the value + the per-cell baseline tint instead.
//
// (item 8) Each column may carry a raw `baseline` mean + `lowerIsBetter`; when
// present we tint every numeric cell above/below the team baseline with the
// existing usage-delta-good/bad tones (good = better-than-avg per lowerIsBetter)
// so an owner can see who's above/below the team at a glance.
//
// Inactive members are shown MUTED (only their metric cells dim — the name +
// inactive chip stay readable so the "who's ramping" signal isn't hidden). An
// optional muted baseline ("team avg") row renders last. When `canDrill` the row
// gets a drill button with a chevron.
//
// (Phase 3a) Each row may also carry a period-over-period `delta` (rendered as a
// DeltaBadge on the PRIMARY cell — the "who's ramping" signal) and a `modelMix`
// (a compact tier-colored stacked bar under the member name showing HOW they
// split work across tiers). Both are optional and degrade to nothing when the
// owner-gated backing fields are absent (older response / non-owner).

export interface MemberComparisonColumn {
  key: string;
  label: string;
  primary?: boolean;
  // (item 8) Raw team-mean baseline for this column + its better-direction. When
  // both are present, numeric cells are tinted vs the baseline. Absent → no tint.
  baseline?: number;
  lowerIsBetter?: boolean;
}

export interface MemberComparisonRow {
  userId: string;
  name: string;
  isYou: boolean;
  roles: string[];
  active: boolean;
  cells: Record<string, { display: string; raw: number }>;
  share?: number; // 0..1 — drives the primary-cell share bar (omit for lower-is-better)
  spark?: number[];
  // (Phase 3a) Period-over-period Δ for the "who's ramping" signal, sitting on
  // the PRIMARY cell next to the value + sparkline. pct is in PERCENTAGE POINTS
  // (already ×100 by the caller — DeltaBadge expects pp, not a fraction). null/
  // absent → no badge (older response / non-owner / no prior window).
  delta?: { pct: number | null; dir: DeltaDir } | null;
  // (Phase 3a) Compact tier-colored model-mix segments (cost-share normalized,
  // sum ~1). Empty/absent → no mix bar (older response / non-owner).
  modelMix?: ModelMixSeg[];
  // (Codex provider) Pre-formatted non-Claude share, e.g. "Codex 32%". Absent /
  // null → no chip (Claude-only member, older response, or a filtered view).
  providerMix?: string | null;
  // Pre-formatted "Last active" value ("today" / "3d ago" / "—"), shown as a
  // muted line under the member name (a column overflowed the dialog). Absent
  // on older responses that omit daily.
  lastActive?: string;
}

// (item 8) Tone for a numeric cell vs its column baseline. Returns '' (no tint)
// when there's no baseline, the value sits on the baseline (within a small
// relative epsilon so float wobble doesn't flicker a tone), or the value is a
// degenerate 0 on a higher-is-better column for an inactive member (we don't
// shout "below average" at someone with no activity — that's the inactive chip's
// job). Otherwise 'good' when the value beats the baseline per lowerIsBetter.
const TINT_EPS = 1e-6;
function cellTone(
  raw: number,
  baseline: number | undefined,
  lowerIsBetter: boolean | undefined,
): '' | 'good' | 'bad' {
  if (baseline == null || !Number.isFinite(baseline) || !Number.isFinite(raw)) return '';
  const denom = Math.max(Math.abs(baseline), 1e-9);
  const rel = (raw - baseline) / denom;
  if (Math.abs(rel) <= TINT_EPS) return '';
  const above = raw > baseline;
  const better = lowerIsBetter ? !above : above;
  return better ? 'good' : 'bad';
}

export function MemberComparisonTable({
  rows,
  columns,
  sortKey,
  sortDir,
  onSort,
  baseline,
  canDrill,
  onDrill,
}: {
  rows: MemberComparisonRow[];
  columns: MemberComparisonColumn[];
  sortKey: string;
  sortDir: 'asc' | 'desc';
  onSort: (key: string) => void;
  baseline?: Record<string, string>;
  canDrill: boolean;
  onDrill?: (userId: string) => void;
}) {
  // Largest primary-column share across members → normalizes the share bars so
  // the leader's bar is full-width.
  const maxShare = Math.max(
    0,
    ...rows.map((r) => (r.share != null ? r.share : 0)),
  );

  const sortArrow = (key: string) =>
    key === sortKey ? (sortDir === 'asc' ? '▲' : '▼') : '';

  return (
    <table className="usage-table usage-compare-table">
      <thead>
        <tr>
          <th className="usage-compare-rankhdr" scope="col">#</th>
          <th scope="col">Member</th>
          {columns.map((c) => (
            <th
              key={c.key}
              className={`num usage-compare-colhdr${c.key === sortKey ? ' sorted' : ''}`}
              scope="col"
              aria-sort={c.key === sortKey ? (sortDir === 'asc' ? 'ascending' : 'descending') : 'none'}
            >
              <button
                type="button"
                className="usage-compare-sortbtn"
                onClick={() => onSort(c.key)}
              >
                {c.label}
                <span className="usage-compare-arrow" aria-hidden="true">{sortArrow(c.key)}</span>
              </button>
            </th>
          ))}
          {canDrill && <th className="usage-compare-drillhdr" aria-label="drill" />}
        </tr>
      </thead>
      <tbody>
        {rows.map((r, i) => (
          <tr
            key={r.userId}
            className={`usage-compare-row${r.isYou ? ' is-you' : ''}${r.active ? '' : ' is-inactive'}`}
          >
            <td className="num usage-compare-rank">{i + 1}</td>
            <td className="usage-compare-member">
              <span className="usage-compare-name" title={r.name}>
                {r.name}
                {r.isYou && <span className="usage-member-you">you</span>}
                {!r.active && <span className="usage-compare-inactive-chip">inactive</span>}
              </span>
              {r.roles.length > 0 && (
                <span className="usage-compare-roles">
                  {r.roles.map((role) => (
                    <span className="usage-member-role" key={role}>{role}</span>
                  ))}
                </span>
              )}
              {/* (Phase 3a) Per-member model mix — how this member splits work
                  across tiers (Opus-heavy vs Haiku-heavy). Hidden when absent. */}
              {r.modelMix && r.modelMix.length > 0 && (
                <ModelMixBar segs={r.modelMix} />
              )}
              {r.providerMix && (
                <span className="usage-compare-provider-mix">{r.providerMix}</span>
              )}
              {r.lastActive !== undefined && (
                <span className="usage-compare-lastactive">Last active {r.lastActive}</span>
              )}
            </td>
            {columns.map((c) => {
              const cell = r.cells[c.key];
              const display = cell?.display ?? '—';
              // (item 8) Tint vs the column baseline. Inactive members keep the
              // tone too — a high-cache-hit but low-volume member should still
              // read "good" on quality columns; only the row's metric dimming
              // (CSS) softens it. We pass the raw value for the tone math.
              const tone = cellTone(cell?.raw ?? 0, c.baseline, c.lowerIsBetter);
              const toneClass = tone ? ` usage-cell-${tone}` : '';
              if (c.primary) {
                // (item 4) Share bar drawn from the ACTIVE-metric share the
                // caller computed (omitted for lower-is-better primaries, where
                // a bar would be misleading — the value + tint carry the read).
                const share = r.share;
                const width = share != null && maxShare > 0 ? (share / maxShare) * 100 : 0;
                return (
                  <td key={c.key} className={`num usage-compare-primary${toneClass}`}>
                    <div className="usage-compare-primary-cell">
                      <span className="usage-compare-primary-val">{display}</span>
                      {/* (Phase 3a) Period-over-period Δ — the "who's ramping"
                          signal, next to the headline value + sparkline. The
                          caller computes it on THIS column's metric, so its
                          tone follows the column: cost Δ is neutral (nothing
                          is billed), a lower-is-better column (cost / turn)
                          reads up=bad, the engagement columns up=good. */}
                      {r.delta && (
                        <DeltaBadge
                          pct={r.delta.pct}
                          dir={r.delta.dir}
                          neutral={c.key === 'cost_usd'}
                          goodWhen={c.lowerIsBetter ? 'down' : 'up'}
                        />
                      )}
                      {share != null && (
                        <span className="usage-share-track usage-compare-bar">
                          <span
                            className="usage-share-fill usage-share-magenta"
                            style={{ width: width.toFixed(1) + '%' }}
                          />
                        </span>
                      )}
                      {r.spark && r.spark.length > 0 && (
                        <MiniSparkline values={r.spark} accent />
                      )}
                    </div>
                  </td>
                );
              }
              return (
                <td key={c.key} className={`num${toneClass}`}>{display}</td>
              );
            })}
            {canDrill && (
              <td className="usage-compare-drill">
                {onDrill && (
                  <button
                    type="button"
                    className="usage-compare-drillbtn"
                    onClick={() => onDrill(r.userId)}
                    title={`View ${r.name}'s usage`}
                    aria-label={`View ${r.name}'s usage`}
                  >›</button>
                )}
              </td>
            )}
          </tr>
        ))}
        {baseline && (
          <tr className="usage-compare-row usage-compare-baseline">
            <td className="num usage-compare-rank" aria-hidden="true" />
            <td className="usage-compare-member">
              <span className="usage-compare-name usage-compare-baseline-name">team avg</span>
            </td>
            {columns.map((c) => (
              <td key={c.key} className="num usage-compare-baseline-cell">
                {baseline[c.key] ?? '—'}
              </td>
            ))}
            {canDrill && <td className="usage-compare-drill" aria-hidden="true" />}
          </tr>
        )}
      </tbody>
    </table>
  );
}
