import type { PaneAutoMode } from '../types';

// Single source of truth for Auto-approve mode copy + cycling, shared by
// the pane-header badge (click-to-cycle), the pane context-menu radio, and the
// command-palette action so all three surfaces describe the SAME three modes
// identically. Mirrors MODE_TOGGLE_TITLE's shared-copy approach for the
// Terminal/Native toggle.

// Cycle order: off → cautious → always → off. Also the radio order in the
// context menu (least → most permissive).
export const AUTO_MODES: readonly PaneAutoMode[] = ['', 'cautious', 'always'];

// Full-word label for menus / palette / radio rows.
export function autoModeMenuLabel(mode: PaneAutoMode): string {
  if (mode === '') return 'Off (manual)';
  if (mode === 'cautious') return 'Approve with caution';
  return 'Always approve';
}

// Compact chip label for the header badge. Off and always both read "auto" —
// the chip's styling (greyed vs. solid magenta box) carries that distinction —
// while cautious gets a trailing "?" to signal "only when low-risk".
export function autoModeBadgeLabel(mode: PaneAutoMode): string {
  return mode === 'cautious' ? 'auto?' : 'auto';
}

// One-line explanation of what a mode does — shared tooltip copy.
export function autoModeDescription(mode: PaneAutoMode): string {
  if (mode === '') return 'off — you approve every prompt manually';
  if (mode === 'cautious') {
    return 'approve with caution — only low-risk prompts are auto-approved';
  }
  return 'always approve — recognized tool permissions are approved with no cancel window; questions and plan changes still need you';
}

// Next mode in the off → cautious → always → off cycle.
export function nextAutoMode(mode: PaneAutoMode): PaneAutoMode {
  const i = AUTO_MODES.indexOf(mode);
  return AUTO_MODES[(i + 1) % AUTO_MODES.length] ?? '';
}
