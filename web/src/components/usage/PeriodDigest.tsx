import type { PeriodDigest as PeriodDigestData, DigestMover } from '../../lib/usage';
import { DeltaBadge } from './DeltaBadge';
import { fmtUSD, pct } from './format';

// (Phase 4a) "Period digest" — the owner's at-a-glance "this period vs the prior
// equal-length one" card. DUMB/presentational: the caller (lib/usage.periodDigest)
// has already derived the headline, the hero deltas, the up/down movers, the top
// contributor and the most-efficient member from the team-summary response (no
// new API). This component only PAINTS that, adapting to whatever the digest
// carries (it never recomputes). Returns null when there's no digest (no
// comparison basis / solo / non-owner / "all" window) so the owner view degrades
// cleanly to nothing.
//
// REMEMBER: every delta on the digest is a FRACTION (0.12 == +12%); DeltaBadge
// wants PERCENTAGE POINTS, so each pct is scaled ×100 before being passed in. The
// spend/cost badge uses `neutral` (nothing is billed — a cost rise is
// informational, not alarming), matching the team-insight mover badge.

// The four hero metrics shown as compact key-deltas, in display order. cost_usd
// renders neutral (see above); the rest use the default up-is-good tone.
const KEY_METRICS: Array<{
  key: 'cost_usd' | 'turns' | 'active_days' | 'cache_hit_rate';
  label: string;
  format: (v: number) => string;
  neutral?: boolean;
}> = [
  { key: 'cost_usd', label: 'Spend', format: (v) => fmtUSD(v), neutral: true },
  { key: 'turns', label: 'Turns', format: (v) => Math.round(v).toLocaleString() },
  { key: 'active_days', label: 'Active', format: (v) => Math.round(v).toLocaleString() },
  { key: 'cache_hit_rate', label: 'Cache hit', format: (v) => pct(v) },
];

// One Accelerating/Cooling member row: name (+ "you" chip) and their ±% as a
// DeltaBadge (fraction ×100). The badge is neutral — the cooling/accelerating
// framing already conveys direction, and nothing is billed.
function MoverRow({ m }: { m: DigestMover }) {
  return (
    <li className="usage-digest-mover">
      <span className="usage-digest-mover-name" title={m.name}>
        {m.name}
        {m.isYou && <span className="usage-member-you">you</span>}
      </span>
      <DeltaBadge pct={m.pct * 100} dir={m.dir} neutral />
    </li>
  );
}

export function PeriodDigest({ digest }: { digest: PeriodDigestData | null }) {
  if (!digest) return null;

  return (
    <div className="usage-digest" role="note">
      <p className="usage-digest-headline">{digest.headline}</p>

      {/* Key hero deltas — value + a signed badge each. */}
      <div className="usage-digest-deltas">
        {KEY_METRICS.map((metric) => {
          const d = digest.heroDeltas[metric.key];
          return (
            <div className="usage-digest-delta" key={metric.key}>
              <span className="usage-digest-delta-label">{metric.label}</span>
              <span className="usage-digest-delta-val">{metric.format(d.cur)}</span>
              <DeltaBadge
                pct={d.pct === null ? null : d.pct * 100}
                dir={d.dir}
                neutral={metric.neutral}
              />
            </div>
          );
        })}
      </div>

      {/* Accelerating ▲ / Cooling ▼ columns. Each lists its movers, or an
          empty-state line when none ramped in that direction this period. */}
      <div className="usage-digest-movers">
        <div className="usage-digest-movers-col usage-digest-accel">
          <h4 className="usage-digest-movers-title">Accelerating ▲</h4>
          {digest.moversUp.length === 0 ? (
            <div className="usage-digest-movers-empty">No one ramped up.</div>
          ) : (
            <ul className="usage-digest-movers-list">
              {digest.moversUp.map((m) => (
                <MoverRow key={m.userId} m={m} />
              ))}
            </ul>
          )}
        </div>
        <div className="usage-digest-movers-col usage-digest-cool">
          <h4 className="usage-digest-movers-title">Cooling ▼</h4>
          {digest.moversDown.length === 0 ? (
            <div className="usage-digest-movers-empty">No one cooled off.</div>
          ) : (
            <ul className="usage-digest-movers-list">
              {digest.moversDown.map((m) => (
                <MoverRow key={m.userId} m={m} />
              ))}
            </ul>
          )}
        </div>
      </div>

      {/* Small chips: top contributor (name + share%) + most efficient
          (name + $/turn). Each is null-safe — its chip hides when absent. */}
      {(digest.topContributor || digest.mostEfficient) && (
        <div className="usage-digest-chips">
          {digest.topContributor && (
            <span className="usage-digest-chip">
              <span className="usage-digest-chip-label">Top contributor</span>
              <strong>{digest.topContributor.name}</strong>
              <span className="usage-digest-chip-meta">{pct(digest.topContributor.share)}</span>
            </span>
          )}
          {digest.mostEfficient && (
            <span className="usage-digest-chip">
              <span className="usage-digest-chip-label">Most efficient</span>
              <strong>{digest.mostEfficient.name}</strong>
              <span className="usage-digest-chip-meta">
                {fmtUSD(digest.mostEfficient.costPerTurn)}/turn
              </span>
            </span>
          )}
        </div>
      )}
    </div>
  );
}
