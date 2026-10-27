import { fmtUSD } from './format';

// Inline-SVG area+line chart of running (cumulative) spend over the window.
// DUMB: the caller passes already-computed running-sum points
// ({ date, value } where value is the cumulative cost at that date). We draw a
// filled area under a magenta line with first/mid/last date axis labels and a
// per-vertex <title> tooltip. Matches the DailyChart sizing/axis conventions.
//
// Empty-state: no points (or a degenerate single point) → an inline note.
export function CumulativeSpendChart({
  points,
}: {
  points: Array<{ date: string; value: number }>;
}) {
  if (points.length === 0) {
    return <div className="usage-page-empty-inline">No spend to chart in this window.</div>;
  }

  const W = 720;
  const H = 140;
  const PAD_B = 16; // x-axis label room
  const PAD_T = 4;
  const n = points.length;
  const plotH = H - PAD_B - PAD_T;
  const max = Math.max(...points.map((p) => p.value), 0);
  const slot = n > 1 ? W / (n - 1) : W;

  const xOf = (i: number) => (n > 1 ? i * slot : W / 2);
  const yOf = (v: number) => (max <= 0 ? H - PAD_B : H - PAD_B - (v / max) * plotH);

  const linePts = points.map((p, i) => `${xOf(i).toFixed(2)},${yOf(p.value).toFixed(2)}`);
  const linePath = 'M ' + linePts.join(' L ');
  // Close the area down to the baseline at both ends.
  const areaPath =
    `M ${xOf(0).toFixed(2)},${(H - PAD_B).toFixed(2)} ` +
    'L ' + linePts.join(' L ') +
    ` L ${xOf(n - 1).toFixed(2)},${(H - PAD_B).toFixed(2)} Z`;

  const labelIdx = new Set([0, Math.floor((n - 1) / 2), n - 1]);

  return (
    <svg
      className="usage-cumulative-chart"
      viewBox={`0 0 ${W} ${H}`}
      width="100%"
      height={H}
      preserveAspectRatio="none"
      role="img"
      aria-label="Cumulative notional cost"
    >
      <path className="usage-cumulative-area" d={areaPath} />
      <path className="usage-cumulative-line" d={linePath} />
      {points.map((p, i) => (
        <circle
          key={p.date}
          className="usage-cumulative-dot"
          cx={xOf(i).toFixed(2)}
          cy={yOf(p.value).toFixed(2)}
          r={n > 60 ? 0 : 2}
        >
          <title>{`${p.date}: ${fmtUSD(p.value)} cumulative`}</title>
        </circle>
      ))}
      {points.map((p, i) =>
        labelIdx.has(i) ? (
          <text
            key={`lbl-${p.date}`}
            className="usage-daily-axis"
            x={xOf(i).toFixed(2)}
            y={H - 4}
            textAnchor={i === 0 ? 'start' : i === n - 1 ? 'end' : 'middle'}
          >{p.date.slice(5)}</text>
        ) : null,
      )}
    </svg>
  );
}
