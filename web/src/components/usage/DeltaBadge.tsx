import { fmtSignedPct } from './format';

// A tiny ▲/▼/– delta chip showing the signed % change vs a prior window.
//
// DUMB: the caller has already computed the magnitude (`pct`) and direction
// (`dir`). We only render the glyph + signed number and pick a semantic color.
//
// Color semantics are direction-agnostic by default: which direction is "good"
// varies per metric (more turns = neutral/good; more cost-per-turn = bad), so
// the caller passes `goodWhen` (default 'up'). When the actual direction matches
// goodWhen we render the positive (magenta accent) class; the opposite gets the
// muted/negative class; 'flat' is always neutral.
//
// `pct === null` means there's no prior period to compare against → render a
// neutral "new" pill instead of a bogus +∞%.
//
// (item 11) `neutral` forces the FLAT tone for any direction — used for cost
// deltas: nothing is billed (flat subscription), so a cost rise is informational,
// not alarming, and shouldn't render in the muted-red "bad" tone. The glyph still
// shows the direction; only the color is neutralized.
//
// UNIT: `pct` is in PERCENTAGE POINTS (12.4 → "+12%"), NOT a 0..1 fraction.
// `deltaPct()` in lib/usage returns a FRACTION, so every caller must scale it
// ×100 before passing it here (e.g. `pct={d.pct === null ? null : d.pct * 100}`).
// Passing the raw fraction silently rounds to "0%".
export function DeltaBadge({
  pct,
  dir,
  goodWhen = 'up',
  neutral = false,
}: {
  pct: number | null;
  dir: 'up' | 'down' | 'flat';
  goodWhen?: 'up' | 'down';
  neutral?: boolean;
}) {
  if (pct === null) {
    return <span className="usage-delta usage-delta-new" title="no prior period to compare">new</span>;
  }
  const glyph = dir === 'up' ? '▲' : dir === 'down' ? '▼' : '–';
  // Tone: neutral forces flat; otherwise flat is always neutral and good vs bad
  // depends on goodWhen.
  const tone =
    neutral || dir === 'flat' ? 'flat' : dir === goodWhen ? 'good' : 'bad';
  return (
    <span className={`usage-delta usage-delta-${tone}`}>
      <span className="usage-delta-glyph" aria-hidden="true">{glyph}</span>
      {fmtSignedPct(pct)}
    </span>
  );
}
