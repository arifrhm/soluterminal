// Diagnostic ring buffer for the resize-preserves-scroll logic in Pane.tsx.
// Toggled via Alt+Shift+D from anywhere in the app; the overlay reads from
// this module. Kept tiny and dependency-free so logging adds negligible
// overhead — a single push + splice per event.
//
// We surface this in-app instead of via console because the production
// Tauri shell has no easy DevTools access; users can screenshot the
// overlay when a resize misbehaves and share it back.

export interface ScrollDiagEvent {
  t: number;                  // performance.now() at log time
  paneId: string;
  label: string;              // e.g. "pointerdown", "fit-pre", "restore-line"
  ydisp?: number;
  length?: number;
  rows?: number;
  cols?: number;
  scrollTop?: number;
  scrollHeight?: number;
  clientHeight?: number;
  saved?: { viewportY: number; scrollTop: number; wasAtBottom: boolean };
  note?: string;
}

const MAX_EVENTS = 80;
const events: ScrollDiagEvent[] = [];
let visible = false;
const listeners = new Set<() => void>();

export function logScrollDiag(ev: ScrollDiagEvent) {
  events.push(ev);
  if (events.length > MAX_EVENTS) events.splice(0, events.length - MAX_EVENTS);
  for (const l of listeners) l();
}

export function getScrollDiagEvents(): readonly ScrollDiagEvent[] {
  return events;
}

// Console/Playwright access to the ring without the overlay — used by
// the scroll-stuck repro harness and handy for field debugging.
if (typeof window !== 'undefined') {
  (window as unknown as { __scrollDiag?: () => readonly ScrollDiagEvent[] }).__scrollDiag = getScrollDiagEvents;
}

export function isScrollDiagVisible(): boolean {
  return visible;
}

export function toggleScrollDiag() {
  visible = !visible;
  for (const l of listeners) l();
}

export function subscribeScrollDiag(listener: () => void): () => void {
  listeners.add(listener);
  return () => listeners.delete(listener);
}
