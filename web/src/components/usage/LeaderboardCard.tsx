// A compact top-N leaderboard card — a titled list of ranked rows, each a
// preformatted label + value (+ optional sub line). DUMB: the caller has already
// sorted, sliced to N, and FORMATTED the value string for the active lens/metric
// (so this stays metric-agnostic). The caller's own row is highlighted via
// `isYou`. Renders an empty-state line when there are no rows.
//
// (item 5) An optional muted `caption` lets the caller flag an honesty caveat
// (e.g. "lowest cost/turn reflects model choice, not output quality") right
// under the title so a cost-based board can't read as a productivity ranking.
export function LeaderboardCard({
  title,
  rows,
  caption,
}: {
  title: string;
  rows: Array<{ label: string; value: string; sub?: string; isYou?: boolean }>;
  caption?: string;
}) {
  return (
    <div className="usage-leaderboard">
      <h3 className="usage-block-title usage-leaderboard-title">{title}</h3>
      {caption && <div className="usage-leaderboard-caption">{caption}</div>}
      {rows.length === 0 ? (
        <div className="usage-page-empty-inline">No data in this window.</div>
      ) : (
        <ol className="usage-leaderboard-list">
          {rows.map((r, i) => (
            <li
              className={`usage-leaderboard-row${r.isYou ? ' is-you' : ''}`}
              key={`${i}-${r.label}`}
            >
              <span className="usage-leaderboard-rank">{i + 1}</span>
              <span className="usage-leaderboard-label" title={r.label}>
                {r.label}
                {r.isYou && <span className="usage-member-you">you</span>}
              </span>
              <span className="usage-leaderboard-val">
                {r.value}
                {r.sub && <span className="usage-leaderboard-sub">{r.sub}</span>}
              </span>
            </li>
          ))}
        </ol>
      )}
    </div>
  );
}
