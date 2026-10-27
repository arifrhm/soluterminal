// "Reopen closed pane" — the daemon keeps the recently closed panes and
// rebuilds them itself (daemon/closed_panes.go): it holds the authoritative
// conversation identity, role and harness context, and each backend's daemon
// only knows its own panes. The client keeps just one thing the daemon can't
// know: which pane sat next to the closed one, so the reopened pane lands in
// the same spot.

export interface ClosedPane {
  id: string;
  title: string;
  cwd: string;
  kind: 'claude' | 'codex' | 'muse' | 'harness' | 'shell';
  closedAtMs: number;
}

const neighbors = new Map<string, string>();

export function rememberClosedNeighbor(closedId: string, neighborId: string | undefined) {
  if (neighborId) neighbors.set(closedId, neighborId);
}

export function closedNeighbor(closedId: string): string | undefined {
  return neighbors.get(closedId);
}

export function closedPaneLabel(c: Pick<ClosedPane, 'title' | 'id'>): string {
  return c.title || c.id.slice(-6);
}

export function closedPaneHint(c: ClosedPane): string {
  const what = c.kind === 'shell' ? 'shell in' : c.kind === 'harness' ? 'agent session in' : `${c.kind} conversation in`;
  return `Reopens the ${what} ${c.cwd}`;
}
