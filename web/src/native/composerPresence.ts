import { api } from '../api';

const EDGE_DEBOUNCE_MS = 250;
const HEARTBEAT_MS = 30_000;

const desired = new Map<string, boolean>();
const edgeTimers = new Map<string, ReturnType<typeof setTimeout>>();
let heartbeat: ReturnType<typeof setInterval> | null = null;

function post(paneId: string, nonEmpty: boolean) {
  void api.setPaneComposerState(paneId, nonEmpty).catch(() => {});
}

function refreshHeartbeat() {
  const anyNonEmpty = Array.from(desired.values()).some(Boolean);
  if (anyNonEmpty && heartbeat == null) {
    heartbeat = setInterval(() => {
      for (const [paneId, nonEmpty] of desired) {
        if (nonEmpty) post(paneId, true);
      }
    }, HEARTBEAT_MS);
  } else if (!anyNonEmpty && heartbeat != null) {
    clearInterval(heartbeat);
    heartbeat = null;
  }
}

export function reportComposerPresence(paneId: string, nonEmpty: boolean): void {
  if ((desired.get(paneId) ?? false) === nonEmpty) return;
  desired.set(paneId, nonEmpty);
  const pending = edgeTimers.get(paneId);
  if (pending != null) clearTimeout(pending);
  edgeTimers.set(paneId, setTimeout(() => {
    edgeTimers.delete(paneId);
    post(paneId, nonEmpty);
  }, EDGE_DEBOUNCE_MS));
  refreshHeartbeat();
}

export function resetComposerPresenceForTests(): void {
  for (const timer of edgeTimers.values()) clearTimeout(timer);
  edgeTimers.clear();
  desired.clear();
  if (heartbeat != null) clearInterval(heartbeat);
  heartbeat = null;
}
