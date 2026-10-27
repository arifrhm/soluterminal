// Shared drag-and-drop constants and helpers for pane reordering.
// Terminal panes (Pane.tsx), browser tiles (BrowserPane.tsx), and file
// tiles (FileTile.tsx) all participate in the same drag protocol, so
// they swap / split against each other — a user can drag a browser
// between two terminals, or dock a terminal next to a browser.

import type { DragEvent } from 'react';

export type DropZone = 'top' | 'bottom' | 'left' | 'right' | 'center';

export const DRAG_MIME = 'application/x-solu-pane';

// Map the drag cursor position inside a target tile to one of five
// zones: center (swap), or one of the four edges (split + insert).
// Anything past the 30% inset on all sides falls into the center; the
// closer edge wins otherwise.
export function computeZone(e: DragEvent): DropZone {
  const r = e.currentTarget.getBoundingClientRect();
  const x = (e.clientX - r.left) / r.width;
  const y = (e.clientY - r.top) / r.height;
  const dxEdge = Math.min(x, 1 - x);
  const dyEdge = Math.min(y, 1 - y);
  if (Math.min(dxEdge, dyEdge) > 0.3) return 'center';
  if (dxEdge < dyEdge) {
    return x < 0.5 ? 'left' : 'right';
  }
  return y < 0.5 ? 'top' : 'bottom';
}
