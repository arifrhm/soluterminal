import { MODEL_COLOR_FALLBACK } from './format';

// 100%-stacked area chart of per-model SHARE over time. Each day carries
// `fractions` (model → 0..1, summing to ~1); the chart stacks them top-to-bottom
// so every column fills the full height and the band thickness reads as that
// model's share that day. DUMB: the caller computed the normalized fractions and
// the stable color map + legend order (cost-DESC, matching DailyChart). Legend
// mirrors DailyChart's. Empty-state when there are no days.
export function ModelMixTrendChart({
  days,
  modelColors,
  legendModels,
}: {
  days: Array<{ date: string; fractions: Record<string, number> }>;
  modelColors: Map<string, string>;
  legendModels: string[];
}) {
  // (item 6) Empty-state for: no days, no legend models (older daemon omits the
  // daily by_model breakdown → an empty frame otherwise), or a legend whose
  // bands are ALL zero (no day carries any share). Any of these → the inline
  // note instead of a blank SVG.
  const hasShare = days.some((d) =>
    legendModels.some((m) => (d.fractions[m] ?? 0) > 0),
  );
  if (days.length === 0 || legendModels.length === 0 || !hasShare) {
    return <div className="usage-page-empty-inline">No model mix to chart in this window.</div>;
  }

  const W = 720;
  const H = 140;
  const PAD_B = 16;
  const n = days.length;
  const plotH = H - PAD_B - 4;
  const slot = n > 1 ? W / (n - 1) : W;
  const xOf = (i: number) => (n > 1 ? i * slot : W / 2);

  // Build one filled band per legend model: for each day, the band spans from
  // the cumulative share BELOW it to BELOW+itsShare (top-down stacking). We walk
  // forward along the top edge and back along the bottom edge to close the area.
  const bands = legendModels.map((model) => {
    const top: string[] = [];
    const bottom: string[] = [];
    days.forEach((d, i) => {
      // Cumulative share of models stacked ABOVE this one (earlier in legend).
      let above = 0;
      for (const m of legendModels) {
        if (m === model) break;
        above += Math.max(0, d.fractions[m] ?? 0);
      }
      const share = Math.max(0, d.fractions[model] ?? 0);
      const x = xOf(i);
      const yTop = 4 + above * plotH;
      const yBot = 4 + (above + share) * plotH;
      top.push(`${x.toFixed(2)},${yTop.toFixed(2)}`);
      bottom.push(`${x.toFixed(2)},${yBot.toFixed(2)}`);
    });
    const d =
      'M ' + top.join(' L ') + ' L ' + bottom.reverse().join(' L ') + ' Z';
    return { model, d, color: modelColors.get(model) ?? MODEL_COLOR_FALLBACK };
  });

  const labelIdx = new Set([0, Math.floor((n - 1) / 2), n - 1]);

  return (
    <>
      <svg
        className="usage-modelmix-chart"
        viewBox={`0 0 ${W} ${H}`}
        width="100%"
        height={H}
        preserveAspectRatio="none"
        role="img"
        aria-label="Model mix over time (100% stacked)"
      >
        {bands.map((b) => (
          <path key={b.model} className="usage-modelmix-band" d={b.d} style={{ fill: b.color }}>
            <title>{b.model}</title>
          </path>
        ))}
        {days.map((d, i) =>
          labelIdx.has(i) ? (
            <text
              key={`lbl-${d.date}`}
              className="usage-daily-axis"
              x={xOf(i).toFixed(2)}
              y={H - 4}
              textAnchor={i === 0 ? 'start' : i === n - 1 ? 'end' : 'middle'}
            >{d.date.slice(5)}</text>
          ) : null,
        )}
      </svg>
      {legendModels.length > 0 && (
        <div className="usage-daily-legend" aria-label="Model mix colors">
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
