import type { WardenRun } from '../types';

// Client-side read ledger for Warden runs. Unlike reports (daemon-synced
// lastReadAt) and feedback (cloud read_at), a Warden run carries NO server
// read state — so the inbox tracks "seen" here, in localStorage, keyed by the
// run's stable id (its log-file name, or its timestamp when it produced no
// log). Without this, every retained run would count as unread forever and
// permanently inflate the inbox badge. The set is pruned to runs that still
// exist (see pruneWardenRead) so it stays bounded at ~the ring cap.

const READ_KEY = 'solu:warden:read';

let readSet: Set<string> = load();

function load(): Set<string> {
  try {
    const raw = localStorage.getItem(READ_KEY);
    if (raw) return new Set(JSON.parse(raw) as string[]);
  } catch {
    // Private mode / corrupt value → start empty; the ledger is best-effort.
  }
  return new Set();
}

function persist(): void {
  try {
    localStorage.setItem(READ_KEY, JSON.stringify([...readSet]));
  } catch {
    // Non-fatal: read state just won't survive this reload.
  }
}

/** Stable id for a run: its log file, or its timestamp when it has no log. */
export function wardenRunId(run: WardenRun): string {
  return run.logFile ?? run.at;
}

export function isWardenRunRead(run: WardenRun): boolean {
  return readSet.has(wardenRunId(run));
}

/** Mark one run read. Returns true if the state changed (for notify). */
export function markWardenRunRead(run: WardenRun): boolean {
  const id = wardenRunId(run);
  if (readSet.has(id)) return false;
  readSet.add(id);
  persist();
  return true;
}

/** Mark every run in the list read. Returns true if anything changed. */
export function markAllWardenRead(runs: WardenRun[]): boolean {
  let changed = false;
  for (const r of runs) {
    const id = wardenRunId(r);
    if (!readSet.has(id)) {
      readSet.add(id);
      changed = true;
    }
  }
  if (changed) persist();
  return changed;
}

/** Drop read-marks for runs no longer present, bounding the ledger. Returns
 *  true if anything was pruned. */
export function pruneWardenRead(runs: WardenRun[]): boolean {
  const live = new Set(runs.map(wardenRunId));
  let pruned = false;
  for (const id of [...readSet]) {
    if (!live.has(id)) {
      readSet.delete(id);
      pruned = true;
    }
  }
  if (pruned) persist();
  return pruned;
}

/** Test hook: reset the ledger. */
export function __resetWardenReadForTests(read: string[] = []): void {
  readSet = new Set(read);
}
