import { WEEKDAY_LABELS } from './format';

// Hour-of-day × day-of-week activity heatmap — a 7-row (Sun..Sat) × 24-col
// (0..23h) grid of cells whose fill intensity scales to `max` on a magenta ramp.
// DUMB: the caller has already built a dense LOCAL 7×24 matrix
// (matrix[localDow][localHour]) — rotating UTC cells for older clouds; cells
// from a `tz`-echoing cloud are already local — and computed `max`. We only
// paint it.
//
// Empty-state: when max <= 0 (no activity) we render a small inline note rather
// than a grid of dead cells.
const HOUR_LABELS = [0, 6, 12, 18];

export function ActivityHeatmap({
  matrix,
  max,
  metricLabel = 'turns',
}: {
  matrix: number[][];
  max: number;
  metricLabel?: string;
}) {
  if (max <= 0) {
    return (
      <div className="usage-page-empty-inline">No activity to chart in this window.</div>
    );
  }

  return (
    <div className="usage-heatmap" role="img" aria-label={`Activity by hour and weekday (${metricLabel})`}>
      <div className="usage-heatmap-grid">
        {WEEKDAY_LABELS.map((day, dow) => {
          const row = matrix[dow] ?? [];
          return (
            <div className="usage-heatmap-row" key={day}>
              <span className="usage-heatmap-rowlabel">{day}</span>
              <div className="usage-heatmap-cells">
                {Array.from({ length: 24 }, (_, hour) => {
                  const v = row[hour] ?? 0;
                  // (item 10) Floor non-zero cells to a slightly higher intensity
                  // (0.18) and give them a faint border (CSS data-filled) so a
                  // single low-activity turn is distinguishable from an empty
                  // cell on the dark bg; 0 stays a bare track cell.
                  const intensity = v <= 0 ? 0 : 0.18 + (v / max) * 0.82;
                  const hh = String(hour).padStart(2, '0');
                  return (
                    <span
                      className="usage-heatmap-cell"
                      key={hour}
                      style={{ opacity: intensity || undefined }}
                      data-empty={v <= 0 ? '' : undefined}
                      data-filled={v > 0 ? '' : undefined}
                    >
                      <title>{`${day} ${hh}:00 — ${v.toLocaleString()} ${metricLabel}`}</title>
                    </span>
                  );
                })}
              </div>
            </div>
          );
        })}
        <div className="usage-heatmap-axis">
          <span className="usage-heatmap-rowlabel" aria-hidden="true" />
          <div className="usage-heatmap-hours">
            {Array.from({ length: 24 }, (_, hour) => (
              <span className="usage-heatmap-hour" key={hour}>
                {HOUR_LABELS.includes(hour) ? String(hour) : ''}
              </span>
            ))}
          </div>
        </div>
      </div>
      {/* (item 10) Intensity scale key — a small "less → more" gradient so a
          reader can map cell fill to relative activity. */}
      <div className="usage-heatmap-legend" aria-hidden="true">
        <span className="usage-heatmap-legend-lbl">less</span>
        <span className="usage-heatmap-legend-ramp" />
        <span className="usage-heatmap-legend-lbl">more</span>
      </div>
    </div>
  );
}
