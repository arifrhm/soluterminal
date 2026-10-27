// A minimal inline-SVG trend line for table rows — no axes, no labels, just the
// shape of a member's daily series. Mirrors the sidebar `.usage-spark` language
// (violet stroke, non-scaling). DUMB: the caller passes the raw values.
//
// (item 8) When `accent` AND the series is trending UP (last value above the
// first), the stroke shifts to the magenta accent so a ramping member's
// trajectory reads at a glance; otherwise it keeps the muted violet.
//
// Degenerate inputs render a flat baseline line rather than crashing or drawing
// nothing: empty/single-value/all-equal all map to a centered horizontal line.
export function MiniSparkline({
  values,
  width = 72,
  height = 18,
  accent = false,
}: {
  values: number[];
  width?: number;
  height?: number;
  accent?: boolean;
}) {
  const n = values.length;
  const pad = 1.5; // keep the stroke off the edges
  const innerW = Math.max(1, width - pad * 2);
  const innerH = Math.max(1, height - pad * 2);
  const midY = pad + innerH / 2;

  let d: string;
  if (n <= 1) {
    // Nothing to trend → a flat baseline across the middle.
    d = `M ${pad.toFixed(2)} ${midY.toFixed(2)} L ${(pad + innerW).toFixed(2)} ${midY.toFixed(2)}`;
  } else {
    const min = Math.min(...values);
    const max = Math.max(...values);
    const span = max - min;
    const stepX = innerW / (n - 1);
    const pts = values.map((v, i) => {
      const x = pad + i * stepX;
      // Higher value → higher on screen (smaller y). Flat when span==0.
      const y = span <= 0 ? midY : pad + innerH - ((v - min) / span) * innerH;
      return `${x.toFixed(2)} ${y.toFixed(2)}`;
    });
    d = 'M ' + pts.join(' L ');
  }

  // Up-trend = the last value sits above the first (a ramping member). Only
  // meaningful with >= 2 points; a flat/empty series is never "up".
  const trendingUp = accent && n > 1 && values[n - 1] > values[0];

  return (
    <svg
      className="usage-sparkline"
      viewBox={`0 0 ${width} ${height}`}
      width={width}
      height={height}
      preserveAspectRatio="none"
      role="img"
      aria-label="trend"
    >
      <path
        className={`usage-sparkline-line${trendingUp ? ' usage-sparkline-up' : ''}`}
        d={d}
      />
    </svg>
  );
}
