import { fmtUSD } from './format';

// Multi-member TREND OVERLAY — one polyline per member of their daily value
// (cost or turns) across the window, on a SHARED y-scale so a steeper line
// reads as "ramping up". DUMB: the caller computed the zero-filled `dates`
// spine, each series' aligned `values`, and the per-series `color`. This
// complements the single per-member Δ badge in the comparison table by showing
// the SHAPE of each member's trajectory over time.
//
// Style mirrors CumulativeSpendChart / ModelMixTrendChart: inline SVG with a
// viewBox, first/mid/last date axis labels, per-point <title> tooltips, and the
// DailyChart legend pattern below (color dot + name, "you" chip for isYou).
//
// Inactive / all-zero series render faint (de-emphasized stroke) and are not
// emphasized in the legend. Empty (no series, no dates, or every value zero)
// → an inline empty-state. A single date → dots (no line to draw between one x).
export interface TeamTrendSeries {
  userId: string;
  name: string;
  color: string;
  values: number[];
  isYou?: boolean;
  active?: boolean;
}

export function TeamTrendChart({
  dates,
  series,
  metricLabel = 'daily cost',
  valueFormat = 'usd',
}: {
  dates: string[];
  series: TeamTrendSeries[];
  metricLabel?: string;
  valueFormat?: 'usd' | 'int';
}) {
  const fmtVal = (v: number) =>
    valueFormat === 'int' ? Math.round(v).toLocaleString() : fmtUSD(v);

  // Empty-state: no spine, no members, or nothing but zeros across every
  // series → the inline note instead of a blank SVG frame. (Matches the sibling
  // charts which never render an empty frame.)
  const hasValue = series.some((s) => s.values.some((v) => v > 0));
  if (dates.length === 0 || series.length === 0 || !hasValue) {
    return (
      <div className="usage-page-empty-inline">No member trend data in this window.</div>
    );
  }

  const W = 720;
  const H = 160;
  const PAD_B = 16; // x-axis label room
  const PAD_T = 4;
  const n = dates.length;
  const plotH = H - PAD_B - PAD_T;
  const slot = n > 1 ? W / (n - 1) : W;

  const xOf = (i: number) => (n > 1 ? i * slot : W / 2);
  const single = n === 1; // one date → dot mode (no segment to draw)

  // Shared y-scale across ALL series so steeper == more.
  const max = Math.max(
    0,
    ...series.flatMap((s) => s.values),
  );
  const yOf = (v: number) => (max <= 0 ? H - PAD_B : H - PAD_B - (v / max) * plotH);

  // A series is de-emphasized when explicitly inactive or carries no value.
  const isFaint = (s: TeamTrendSeries) =>
    s.active === false || !s.values.some((v) => v > 0);

  // Precompute geometry ONCE: each series → its polyline `d` + per-point dots.
  const lines = series.map((s) => {
    const pts = dates.map((_, i) => ({
      x: xOf(i),
      y: yOf(s.values[i] ?? 0),
    }));
    const d = 'M ' + pts.map((p) => `${p.x.toFixed(2)},${p.y.toFixed(2)}`).join(' L ');
    return { s, pts, d, faint: isFaint(s) };
  });

  const labelIdx = new Set([0, Math.floor((n - 1) / 2), n - 1]);
  // Dots get drawn only when sparse enough to be legible (or in single-date
  // mode, where they're the only mark). Otherwise just the lines.
  const showDots = single || n <= 31;

  return (
    <>
      <svg
        className="usage-teamtrend-chart"
        viewBox={`0 0 ${W} ${H}`}
        width="100%"
        height={H}
        preserveAspectRatio="none"
        role="img"
        aria-label={`Per-member ${metricLabel} over time`}
      >
        {lines.map(({ s, pts, d, faint }) => (
          <g
            key={s.userId}
            className={`usage-teamtrend-series${faint ? ' is-faint' : ''}`}
            style={{ color: s.color }}
          >
            {!single && (
              <path className="usage-teamtrend-line" d={d}>
                <title>{s.name}</title>
              </path>
            )}
            {showDots &&
              pts.map((p, i) => (
                <circle
                  key={dates[i]}
                  className="usage-teamtrend-dot"
                  cx={p.x.toFixed(2)}
                  cy={p.y.toFixed(2)}
                  r={single ? 3 : 2}
                >
                  <title>{`${s.name} · ${dates[i]}: ${fmtVal(s.values[i] ?? 0)}`}</title>
                </circle>
              ))}
          </g>
        ))}
        {dates.map((date, i) =>
          labelIdx.has(i) ? (
            <text
              key={`lbl-${date}`}
              className="usage-daily-axis"
              x={xOf(i).toFixed(2)}
              y={H - 4}
              textAnchor={i === 0 ? 'start' : i === n - 1 ? 'end' : 'middle'}
            >
              {date.slice(5)}
            </text>
          ) : null,
        )}
      </svg>
      <div className="usage-daily-legend usage-teamtrend-legend" aria-label="Member trend colors">
        {series.map((s) => (
          <span
            className={`usage-daily-legend-item${isFaint(s) ? ' is-faint' : ''}`}
            key={s.userId}
          >
            <span className="usage-daily-legend-dot" style={{ background: s.color }} />
            <span className="usage-daily-legend-name" title={s.name}>
              {s.name}
            </span>
            {s.isYou && <span className="usage-member-you">you</span>}
          </span>
        ))}
      </div>
    </>
  );
}
