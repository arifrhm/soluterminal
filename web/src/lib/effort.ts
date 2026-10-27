import type { PaneEffort } from '../types';

// Single source of truth for reasoning-effort copy + cycling, shared by the
// New Session dialog selector, the pane context-menu radio, and any header
// chip — so every surface describes the SAME levels identically. Mirrors
// lib/autoMode.ts's shared-copy approach.
//
// Effort is PROVIDER-AGNOSTIC: it sets claude's --effort, which rides the API
// request to whichever backend. Anthropic uses its native levels; a GLM (Z.ai)
// backend maps them to its two tiers — high-and-below → GLM High, xhigh/max →
// GLM Max. '' = claude's built-in default (no flag).

// Order: default → low → … → max (least → most reasoning). Also the radio order.
export const EFFORTS: readonly PaneEffort[] = ['', 'low', 'medium', 'high', 'xhigh', 'max'];

// The default a GLM pane gets when the user doesn't pick one (Z.ai recommends
// max for coding). Anthropic panes left unset keep claude's own default ('').
export const DEFAULT_GLM_EFFORT: PaneEffort = 'max';

// Full-word label for menus / radio rows.
export function effortMenuLabel(e: PaneEffort): string {
  switch (e) {
    case '': return 'Default';
    case 'low': return 'Low';
    case 'medium': return 'Medium';
    case 'high': return 'High';
    case 'xhigh': return 'Extra high';
    case 'max': return 'Max';
  }
}

// Compact label for a chip / dialog button.
export function effortChipLabel(e: PaneEffort): string {
  switch (e) {
    case '': return 'default';
    case 'medium': return 'med';
    case 'xhigh': return 'xhigh';
    default: return e; // low / high / max
  }
}

// One-line tooltip. Notes the GLM mapping so the same copy explains both backends.
export function effortDescription(e: PaneEffort): string {
  switch (e) {
    case '': return "default — claude's built-in effort (no override)";
    case 'low': return 'low — fastest, least reasoning (GLM: High tier)';
    case 'medium': return 'medium reasoning (GLM: High tier)';
    case 'high': return 'high reasoning (GLM: High tier)';
    case 'xhigh': return 'extra-high reasoning (GLM: Max tier)';
    case 'max': return 'max reasoning — best for hard coding (GLM: Max tier)';
  }
}

// Next level in the cycle (default → low → … → max → default). For a
// click-to-cycle chip; menus use the radio rows instead.
export function nextEffort(e: PaneEffort): PaneEffort {
  const i = EFFORTS.indexOf(e);
  return EFFORTS[(i + 1) % EFFORTS.length] ?? '';
}
