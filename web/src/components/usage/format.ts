// Local copies of the small format helpers the usage viz primitives need.
// UsagePage.tsx defines these privately (not exported), and this contract
// forbids editing UsagePage to export them, so the dumb components own a tiny
// local copy here. Kept byte-compatible with the originals so the rebuilt
// surfaces read identically to the existing page.

// USD with cents; whole-dollar grouping past $1,000 (matches UsagePage.fmtUSD).
export function fmtUSD(usd: number): string {
  if (typeof usd !== 'number' || !Number.isFinite(usd)) return '—';
  if (usd >= 1000) return '$' + Math.round(usd).toLocaleString();
  return '$' + usd.toFixed(2);
}

// Clamp a 0..1 share to a rounded integer percent string (matches UsagePage.pct).
export function pct(share: number): string {
  if (typeof share !== 'number' || !Number.isFinite(share)) return '—';
  return Math.round(Math.max(0, Math.min(1, share)) * 100) + '%';
}

// Signed integer percent for deltas, e.g. +12% / -4% / 0%. `value` is already a
// percentage number (not a 0..1 fraction); we just round and sign it.
export function fmtSignedPct(value: number): string {
  const r = Math.round(value);
  if (r === 0) return '0%';
  return (r > 0 ? '+' : '') + r + '%';
}

// Shared fallback color for any model the color map doesn't carry (mirrors
// UsagePage.MODEL_COLOR_FALLBACK) so model-keyed surfaces never go transparent.
export const MODEL_COLOR_FALLBACK = 'color-mix(in srgb, var(--fg) 30%, transparent)';

// Local weekday labels for the activity heatmap rows (Sun..Sat — index matches
// Date.getDay() / the contract's dow 0=Sun).
export const WEEKDAY_LABELS = ['Sun', 'Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat'];
