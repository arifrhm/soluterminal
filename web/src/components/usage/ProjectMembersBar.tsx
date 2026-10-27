import { fmtUSD, pct } from './format';

// (Phase 3c) "Who works on what" — a compact, member-colored breakdown of a
// SINGLE project's spend, sitting under a project row in the owner team view.
// DUMB/presentational: the caller (via lib/usage.projectMembers) already mapped
// the owner-only `by_member` breakdown to `members` (color-resolved, sorted by
// share DESC). This component only PAINTS that:
//
//   - a horizontal stacked bar — one segment per member, width = the member's
//     project-local cost share, filled with the member's color; a per-segment
//     <title> surfaces "name: $cost · N turns · xx%".
//   - a small legend of the top `maxSegments` members (name + share%), with a
//     "you" chip for the caller and a folded "+N others" tail segment + legend
//     entry when there are more members than fit.
//
// Empty/absent members → renders nothing (return null), so a project with no
// owner-gated breakdown (older response / non-owner / solo) shows no bar.
export interface ProjectMemberDatum {
  userId: string;
  name: string;
  color: string;
  costUsd: number;
  turns: number;
  share: number; // 0..1 — member cost in THIS project / project total
  isYou?: boolean;
}

// Neutral fill for the folded "others" tail — mirrors the usage-mix "other"
// segment so a many-member project still reads as a compact bar.
const OTHERS_COLOR = 'color-mix(in srgb, var(--fg) 28%, transparent)';

export function ProjectMembersBar({
  members,
  maxSegments = 5,
}: {
  members: ProjectMemberDatum[];
  maxSegments?: number;
}) {
  if (!members || members.length === 0) return null;

  // Head = the visible top members; tail folds into one "others" segment so the
  // bar + legend stay compact. Guard maxSegments >= 1.
  const cap = Math.max(1, maxSegments);
  const head = members.slice(0, cap);
  const tail = members.slice(cap);

  // Folded tail aggregate (share + cost + turns) for the trailing segment/legend.
  const others =
    tail.length > 0
      ? tail.reduce(
          (acc, m) => ({
            share: acc.share + m.share,
            costUsd: acc.costUsd + m.costUsd,
            turns: acc.turns + m.turns,
            count: acc.count + 1,
          }),
          { share: 0, costUsd: 0, turns: 0, count: 0 },
        )
      : null;

  const segTitle = (name: string, costUsd: number, turns: number, share: number) =>
    `${name}: ${fmtUSD(costUsd)} · ${Math.round(turns).toLocaleString()} turns · ${pct(share)}`;

  return (
    <div className="usage-projmembers" aria-label="member breakdown">
      <span className="usage-projmembers-bar" role="img" aria-label="member cost share">
        {head.map((m) =>
          m.share > 0 ? (
            <span
              key={m.userId}
              className="usage-projmembers-seg"
              style={{ width: (m.share * 100).toFixed(1) + '%', background: m.color }}
              title={segTitle(m.name, m.costUsd, m.turns, m.share)}
            />
          ) : null,
        )}
        {others && others.share > 0 && (
          <span
            className="usage-projmembers-seg usage-projmembers-seg-others"
            style={{ width: (others.share * 100).toFixed(1) + '%', background: OTHERS_COLOR }}
            title={segTitle(
              `+${others.count} others`,
              others.costUsd,
              others.turns,
              others.share,
            )}
          />
        )}
      </span>
      <span className="usage-projmembers-legend">
        {head.map((m) => (
          <span className="usage-projmembers-legend-item" key={m.userId}>
            <span className="usage-projmembers-dot" style={{ background: m.color }} />
            <span className="usage-projmembers-name" title={m.name}>
              {m.name}
            </span>
            {m.isYou && <span className="usage-member-you">you</span>}
            <span className="usage-projmembers-share">{pct(m.share)}</span>
          </span>
        ))}
        {others && others.count > 0 && (
          <span className="usage-projmembers-legend-item usage-projmembers-legend-others">
            <span className="usage-projmembers-dot" style={{ background: OTHERS_COLOR }} />
            <span className="usage-projmembers-name">+{others.count} others</span>
            <span className="usage-projmembers-share">{pct(others.share)}</span>
          </span>
        )}
      </span>
    </div>
  );
}
