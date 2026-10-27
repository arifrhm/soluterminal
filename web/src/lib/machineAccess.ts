export const MACHINE_ACCESS_401_THRESHOLD = 3;
export const MACHINE_ACCESS_PROBE_INTERVAL_MS = 120_000;
export const ACCOUNT_PROBE_MIN_INTERVAL_MS = 30_000;

export interface MachineAccessState {
  blocked: boolean;
  machineId: string | null;
  checking: boolean;
}

type MachineScopeContext = {
  machineId: string;
  token: string | null;
};

const initialState: MachineAccessState = {
  blocked: false,
  machineId: null,
  checking: false,
};

let state = initialState;
let consecutive401s = 0;
let lastProbeAt: number | null = null;
let lastAccountProbeAt: number | null = null;
let generation = 0;

const listeners = new Set<() => void>();

function clockNow(): number {
  if (typeof performance !== 'undefined' && typeof performance.now === 'function') {
    return performance.now();
  }
  return Date.now();
}

function replaceState(next: MachineAccessState): void {
  if (
    next.blocked === state.blocked
    && next.machineId === state.machineId
    && next.checking === state.checking
  ) return;
  state = next;
  for (const listener of listeners) listener();
}

/**
 * Feed an HTTP response from the selected machine through the shared access
 * classifier. Network failures deliberately never call this function: only an
 * actual HTTP status can change the classification.
 */
export async function reportMachineScopeStatus(
  status: number,
  ctx: MachineScopeContext,
): Promise<void> {
  if (status !== 401) {
    consecutive401s = 0;
    generation += 1;
    lastProbeAt = null;
    replaceState(initialState);
    return;
  }

  consecutive401s += 1;
  if (state.blocked || state.checking || consecutive401s < MACHINE_ACCESS_401_THRESHOLD) return;

  const now = clockNow();
  if (
    lastAccountProbeAt !== null
    && now - lastAccountProbeAt < ACCOUNT_PROBE_MIN_INTERVAL_MS
  ) return;

  lastAccountProbeAt = now;
  const probeGeneration = generation;
  replaceState({ blocked: false, machineId: ctx.machineId, checking: true });

  try {
    const response = await fetch('/api/machines', {
      headers: { Authorization: `Bearer ${ctx.token}` },
    });
    if (probeGeneration !== generation) return;

    if (response.ok) {
      lastProbeAt = clockNow();
      replaceState({ blocked: true, machineId: ctx.machineId, checking: false });
      return;
    }

    // A root-scope 401 means the whole login is stale, not that this user is
    // specifically denied access to the selected machine.
    replaceState({ blocked: false, machineId: null, checking: false });
  } catch {
    if (probeGeneration === generation) {
      replaceState({ blocked: false, machineId: null, checking: false });
    }
  }
}

/** Allow normal traffic, or one gated machine probe per backoff interval. */
export function shouldAllowMachineRequest(now = clockNow()): boolean {
  if (!state.blocked) return true;
  if (lastProbeAt !== null && now - lastProbeAt < MACHINE_ACCESS_PROBE_INTERVAL_MS) {
    return false;
  }
  lastProbeAt = now;
  return true;
}

export function resetMachineAccess(): void {
  consecutive401s = 0;
  lastProbeAt = null;
  lastAccountProbeAt = null;
  generation += 1;
  replaceState(initialState);
}

export function forceProbeNow(): void {
  lastProbeAt = null;
}

export function getMachineAccessState(): MachineAccessState {
  return state;
}

export function subscribeMachineAccess(listener: () => void): () => void {
  listeners.add(listener);
  return () => listeners.delete(listener);
}
