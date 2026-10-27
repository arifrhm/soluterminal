// Running work reported by a terminal harness client in a pane (the DeepSeek /
// ZCode clients emit OSC 7658 `SoluWork=<n>`). A pane's × close has no other way
// to know a turn or background job is still running — the client owns that
// state — so this is the signal the close dialog warns from.
//
// Deliberately a plain module map, not store state: it is written on every
// tool-job transition and only ever read at the moment the user clicks close, so
// re-rendering the tree on each tick would be pure cost.
import { currentRoutingScope } from '../api';
const running = new Map<string, number>();

export function setPaneRunningWork(paneId: string, count: number): void {
  const n = Number.isFinite(count) && count > 0 ? Math.floor(count) : 0;
  if (n > 0) running.set(paneId, n);
  else running.delete(paneId);
}

export function paneRunningWork(paneId: string): number {
  return running.get(JSON.stringify([currentRoutingScope().key,paneId])) ?? running.get(paneId) ?? 0;
}

export function clearPaneRunningWork(paneId: string): void {
  running.delete(paneId);
}
