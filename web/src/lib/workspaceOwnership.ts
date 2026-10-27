// Workspace ownership for multi-monitor support (Phase 1a).
//
// Each pane's PTY has a single size, so showing the SAME workspace (=
// the same panes) in two differently-sized windows causes width-thrash.
// We dodge that by keeping each window on a DISJOINT workspace: a window
// claims the named workspace it shows. Another view saves and suspends that
// owner before moving the workspace; different workspaces stay independent.
//
// The registry is a shared, same-origin localStorage map keyed by
// workspace name → { label, ts }. A 5s heartbeat refreshes the live
// window's claim; a claim older than the 15s TTL is considered abandoned
// (window closed/crashed) and is reclaimable. NB: we deliberately do NOT
// release on unload — a window reloading for an auto-update must be able
// to re-claim its OWN workspace within the TTL instead of a sibling
// grabbing it in the ~1s reload gap.
//
// The scratch workspace ("_current") is per-window-blank and is NEVER
// owned — two windows may both sit in scratch without a false collision.
//
// Pure decision functions (suffix-free, take `now`/`ttl`) carry the logic
// and are unit-tested; the impure wrappers below them touch localStorage
// and BroadcastChannel.

export const OWNERS_KEY = 'solu.workspaceOwners';
export const OWNERSHIP_TTL_MS = 15_000;
export const OWNERSHIP_HEARTBEAT_MS = 5_000;
const OWNERSHIP_CHANNEL = 'solu-workspace-ownership';

// Must match the store's CURRENT_WS sentinel (the unsaved scratch
// workspace). Confirmed by store.ts (`w.name === '_current'`).
const SCRATCH_WS = '_current';

export interface OwnerEntry {
  label: string;
  ts: number;
  visible?: boolean;
}
export type OwnersMap = Record<string, OwnerEntry>;

/** Named, ownable workspace? Scratch and empty names never participate. */
export function isOwnable(name: string | null | undefined): boolean {
  return !!name && name !== SCRATCH_WS;
}

function isLive(entry: OwnerEntry | undefined, now: number, ttlMs: number): boolean {
  return !!entry && now - entry.ts <= ttlMs;
}

/**
 * If `name` is currently held by a LIVE window whose label differs from
 * `label`, return that owner's label; otherwise null (free / self / stale).
 */
export function ownedByOther(
  map: OwnersMap,
  name: string,
  label: string,
  now: number,
  ttlMs: number,
): string | null {
  if (!isOwnable(name)) return null;
  const e = map[name];
  if (!isLive(e, now, ttlMs)) return null;
  return e!.label === label ? null : e!.label;
}

/** May `label` switch this window to `name`? (Self/stale/free → yes.) */
export function canClaim(
  map: OwnersMap,
  name: string,
  label: string,
  now: number,
  ttlMs: number,
): boolean {
  return ownedByOther(map, name, label, now, ttlMs) === null;
}

/**
 * Record `label` as the owner of `name` (forceful — call only AFTER the
 * guard has allowed the switch, or when restoring this window's own
 * workspace). A window owns at most one workspace, so any other entry
 * owned by `label` is dropped. Scratch/empty names are not recorded.
 * Returns a NEW map (input is not mutated).
 */
export function applyClaim(map: OwnersMap, name: string, label: string, now: number): OwnersMap {
  if (!isOwnable(name)) {
    // Leaving a named workspace for scratch: drop this window's prior claim.
    return dropOwnedBy(map, label);
  }
  const next: OwnersMap = {};
  for (const [k, v] of Object.entries(map)) {
    if (v.label === label) continue; // drop other self-owned entries
    next[k] = v;
  }
  next[name] = { label, ts: now };
  return next;
}

/** Remove every entry owned by `label`. Returns a NEW map. */
export function dropOwnedBy(map: OwnersMap, label: string): OwnersMap {
  const next: OwnersMap = {};
  for (const [k, v] of Object.entries(map)) {
    if (v.label === label) continue;
    next[k] = v;
  }
  return next;
}

/** Drop expired entries. Returns a NEW map (used to keep storage tidy). */
export function pruneStale(map: OwnersMap, now: number, ttlMs: number): OwnersMap {
  const next: OwnersMap = {};
  for (const [k, v] of Object.entries(map)) {
    if (now - v.ts <= ttlMs) next[k] = v;
  }
  return next;
}

// ---------------------------------------------------------------------------
// Impure wrappers — localStorage + BroadcastChannel. Thin; logic lives above.
// ---------------------------------------------------------------------------

export function readOwners(): OwnersMap {
  try {
    const raw = localStorage.getItem(OWNERS_KEY);
    if (!raw) return {};
    const parsed = JSON.parse(raw) as unknown;
    if (!parsed || typeof parsed !== 'object') return {};
    const out: OwnersMap = {};
    for (const [k, v] of Object.entries(parsed as Record<string, unknown>)) {
      if (
        v &&
        typeof v === 'object' &&
        typeof (v as OwnerEntry).label === 'string' &&
        typeof (v as OwnerEntry).ts === 'number'
      ) {
        out[k] = {
          label: (v as OwnerEntry).label,
          ts: (v as OwnerEntry).ts,
          ...((v as OwnerEntry).visible === undefined
            ? {}
            : { visible: (v as OwnerEntry).visible !== false }),
        };
      }
    }
    return out;
  } catch {
    return {};
  }
}

let channel: BroadcastChannel | null | undefined;
function getChannel(): BroadcastChannel | null {
  if (channel !== undefined) return channel;
  try {
    channel =
      typeof BroadcastChannel !== 'undefined' ? new BroadcastChannel(OWNERSHIP_CHANNEL) : null;
  } catch {
    channel = null;
  }
  return channel;
}

export function writeOwners(map: OwnersMap): void {
  try {
    localStorage.setItem(OWNERS_KEY, JSON.stringify(map));
  } catch {
    /* ignore quota / no-storage */
  }
  try {
    getChannel()?.postMessage({ type: 'ownership-changed' });
  } catch {
    /* ignore */
  }
}

/** Read-only guard used before switching workspaces. */
export function checkOwnedByOther(
  name: string,
  label: string,
  now: number = Date.now(),
): string | null {
  return ownedByOther(readOwners(), name, label, now, OWNERSHIP_TTL_MS);
}

/**
 * Claim `name` for `label` and persist + broadcast. No-op for scratch.
 *
 * Soft ownership: localStorage is a non-transactional read-modify-write, so
 * two windows claiming distinct workspaces in the same instant can last-
 * write-wins clobber — but `applyClaim` only drops SELF-labelled entries, so
 * a sibling's claim survives the merge. A genuine TOCTOU (two windows racing
 * onto the SAME free workspace) is a momentary same-workspace display the
 * 5s heartbeat + re-render self-heal; acceptable for Phase 1a.
 */
export function claimWorkspace(
  name: string,
  label: string,
  now: number = Date.now(),
  retainPrevious = false,
): boolean {
  if (checkOwnedByOther(name, label, now)) return false;
  const owners = pruneStale(readOwners(), now, OWNERSHIP_TTL_MS);
  // A transfer reserves its destination while the outgoing view is still
  // displayed. The active-workspace subscription commits the switch later.
  const next =
    retainPrevious && isOwnable(name)
      ? { ...owners, [name]: { label, ts: now } }
      : applyClaim(owners, name, label, now);
  writeOwners(next);
  return true;
}

/** Notify other windows when the registry changes (e.g. to re-render UI). */
export function subscribeOwners(cb: () => void): () => void {
  const ch = getChannel();
  if (!ch) return () => {};
  const handler = () => cb();
  ch.addEventListener('message', handler);
  return () => ch.removeEventListener('message', handler);
}

/**
 * Start a heartbeat that keeps this window's claim fresh so a sibling's
 * guard never sees it as stale while it's live. `getActiveName` returns
 * the window's current active workspace each tick. Returns a stop fn.
 */
export function startOwnershipHeartbeat(
  getActiveName: () => string,
  label: string,
  getVisible: () => boolean = () => true,
): () => void {
  if (typeof setInterval === 'undefined') return () => {};
  const id = setInterval(() => {
    const name = getActiveName();
    if (isOwnable(name)) refreshWorkspaceVisibility(name, label, getVisible());
  }, OWNERSHIP_HEARTBEAT_MS);
  return () => clearInterval(id);
}

/** Refresh only a claim that is still ours; a delayed heartbeat cannot undo a move. */
export function refreshWorkspaceVisibility(name: string, label: string, visible: boolean): void {
  const owners = readOwners();
  if (owners[name]?.label !== label) return;
  writeOwners({ ...owners, [name]: { label, ts: Date.now(), visible } });
}

export function releaseWorkspace(name: string, label: string): void {
  const owners = readOwners();
  if (owners[name]?.label !== label) return;
  delete owners[name];
  writeOwners(owners);
}
