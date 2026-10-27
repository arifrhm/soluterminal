import type { UsageProvider } from '../../api';
import type { ProviderRow } from '../../lib/usage';
import { fmtUSD, pct } from './format';

// Compact token count (1_234_567 → "1.2M"), mirroring UsagePage.fmtTokens.
function fmtTok(n: number): string {
  if (!Number.isFinite(n)) return '—';
  if (n <= 0) return '0';
  if (n >= 1e9) return (n / 1e9).toFixed(1) + 'B';
  if (n >= 1e6) return (n / 1e6).toFixed(1) + 'M';
  if (n >= 1e3) return (n / 1e3).toFixed(1) + 'K';
  return String(Math.round(n));
}

// (Codex provider) By-provider card — one row per CLI (Claude Code / Codex)
// with its cost, a cost-share bar, turns, sessions and the fresh / cached /
// output token split (plus the reasoning subset of output when non-zero). Each
// row is a toggle button: clicking it narrows the whole page to that provider;
// clicking the active provider again clears the filter. DUMB: the caller
// shaped the rows (providerRows) and owns the filter state.
export function ByProviderCard({
  rows,
  active,
  onSelect,
}: {
  rows: ProviderRow[];
  active: UsageProvider;
  onSelect: (p: UsageProvider) => void;
}) {
  if (rows.length === 0) return null;
  return (
    <section className="usage-block usage-providers">
      <h3 className="usage-block-title">By provider</h3>
      <div className="usage-provider-rows">
        {rows.map((r) => {
          const isActive = active === r.provider;
          return (
            <button
              type="button"
              key={r.provider}
              className={`usage-provider-row usage-provider-${r.provider}${isActive ? ' active' : ''}`}
              aria-pressed={isActive}
              title={isActive ? 'Show all providers' : `Show only ${r.label} usage`}
              onClick={() => onSelect(isActive ? 'all' : r.provider)}
            >
              <span className="usage-provider-line">
                <span className="usage-provider-name">
                  <span className="usage-provider-dot" aria-hidden="true" />
                  {r.label}
                </span>
                <span className="usage-provider-cost">{fmtUSD(r.cost_usd)}</span>
              </span>
              <span className="usage-provider-shareline">
                <span className="usage-share-track">
                  <span
                    className="usage-share-fill usage-provider-fill"
                    style={{ width: (r.share * 100).toFixed(1) + '%' }}
                  />
                </span>
                <span className="usage-share-pct">{pct(r.share)}</span>
              </span>
              <span className="usage-provider-meta">
                <span>{r.turns.toLocaleString()} turns</span>
                <span className="usage-provider-sep">·</span>
                <span>{r.sessions.toLocaleString()} sessions</span>
                <span className="usage-provider-sep">·</span>
                <span>{fmtTok(r.fresh_tokens)} fresh</span>
                <span className="usage-provider-sep">·</span>
                <span>{fmtTok(r.cached_tokens)} cached</span>
                <span className="usage-provider-sep">·</span>
                <span>
                  {fmtTok(r.output_tokens)} output
                  {r.reasoning_tokens > 0 && ` (${fmtTok(r.reasoning_tokens)} reasoning)`}
                </span>
              </span>
            </button>
          );
        })}
      </div>
    </section>
  );
}
