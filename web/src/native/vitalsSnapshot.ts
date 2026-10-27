// vitalsSnapshot.ts — a tiny per-pane "last known vitals" cache for the Focus
// (native) pane's bottom NativeMetaBar.
//
// WHY this exists: the NativeMetaBar's vitals (model / context% / cost / cwd) are
// derived from session_init + usage events, which are fetched ASYNC on entering
// Focus mode (useNativePane.seedOnce → fetchNativeHistory, not awaited). So the
// FIRST render after a pane enters Focus has model/contextPct/cost all undefined,
// and the bar paints blank for ~100-300ms until the seed lands — an ugly flash,
// most visible when flipping Terminal→Focus on a pane that already has a session.
//
// The snapshot rehydrates the bar INSTANTLY from the last values we saw for this
// pane id. On mount the component initializes its vitals from getVitalsSnapshot
// (a synchronous module-map / localStorage read) so the bar shows the prior
// values immediately, then live events overwrite them as they stream in. It is a
// pure CLIENT cache — no daemon/wire changes — mirroring the per-pane draft /
// cleared-seq / allowlist module caches in useNativePane, but additionally
// persisted to localStorage so it survives a full reload / app restart (a warm
// re-entry weeks later still shows the last-known model + folder at once).
//
// For a brand-new pane with NO prior snapshot, model/context/cost are simply
// absent until the first turn — that's acceptable (there's nothing to remember
// yet); cwd is sourced synchronously from the pane DTO by the caller, so it shows
// regardless. The snapshot only ever ACCELERATES; it never invents data.

import type { AgentKind } from '../lib/agentKind';

// The persisted shape. Every field optional — a partial snapshot (e.g. model
// known but no turn yet → no cost) is valid and still useful. contextPct is the
// 0..1 fraction (same units the metabar consumes); cost is cumulative USD.
export interface VitalsSnapshot {
  agent?: AgentKind; // provider that owns model/context/cost (cwd is provider-neutral)
  model?: string; // pretty label, e.g. "Opus 4.8"
  contextPct?: number; // 0..1 fraction of the context window used
  cost?: number; // cumulative session cost (USD)
  cwd?: string; // "…/<project folder>" display form
  cwdFull?: string; // full absolute path (hover title)
}

// localStorage key prefix — one entry per pane, mirroring the `solu.<thing>`
// idiom used across store.ts (solu.nativeMetaBar, solu.terminalTheme, …).
const KEY_PREFIX = 'solu.nativeVitals.';
function keyFor(paneId: string): string {
  return KEY_PREFIX + paneId;
}

// Module-level mirror so an in-session re-entry (warm-pane remount, zoom, drag)
// reads without touching localStorage, exactly like the draft/cleared caches.
const snapshotByPane = new Map<string, VitalsSnapshot>();

// sanitizeVitalsSnapshot — coerce an arbitrary parsed blob into a clean,
// typed snapshot: keep only the known fields, and only when they are the right
// primitive type (string for the labels, finite number for the numerics). A
// corrupt/partial blob degrades to whatever subset is valid (worst case {}),
// never throws, and never yields NaN/Infinity (which would render as garbage).
export function sanitizeVitalsSnapshot(raw: unknown): VitalsSnapshot {
  if (!raw || typeof raw !== 'object') return {};
  const r = raw as Record<string, unknown>;
  const out: VitalsSnapshot = {};
  if (r.agent === 'claude' || r.agent === 'codex' || r.agent === 'muse') out.agent = r.agent;
  if (typeof r.model === 'string' && r.model) out.model = r.model;
  if (typeof r.contextPct === 'number' && Number.isFinite(r.contextPct)) {
    // Clamp into the 0..1 range the metabar expects; a stale out-of-range blob
    // shouldn't paint a >100% / negative chip.
    out.contextPct = Math.min(1, Math.max(0, r.contextPct));
  }
  if (typeof r.cost === 'number' && Number.isFinite(r.cost) && r.cost >= 0) out.cost = r.cost;
  if (typeof r.cwd === 'string' && r.cwd) out.cwd = r.cwd;
  if (typeof r.cwdFull === 'string' && r.cwdFull) out.cwdFull = r.cwdFull;
  return out;
}

// getVitalsSnapshot — the last-known vitals for a pane, or {} if none. Reads the
// module mirror first (fast, survives warm remounts); on a miss it hydrates the
// mirror from localStorage (survives a reload). Always returns a fresh object so
// callers can't mutate the cache by reference.
export function getVitalsSnapshot(paneId: string, agent?: AgentKind | null): VitalsSnapshot {
  const mem = snapshotByPane.get(paneId);
  if (mem) return scopeToAgent(mem, agent);
  try {
    const raw = localStorage.getItem(keyFor(paneId));
    if (!raw) return {};
    const snap = sanitizeVitalsSnapshot(JSON.parse(raw));
    snapshotByPane.set(paneId, snap); // warm the mirror so re-reads stay cheap
    return scopeToAgent(snap, agent);
  } catch {
    // Corrupt blob or storage unavailable (private mode / quota) — degrade to
    // empty; the live events still populate the bar a beat later as before.
    return {};
  }
}

// saveVitalsSnapshot — MERGE the given (partial) vitals into the pane's snapshot
// and persist. Merge (not replace) so a partial update — e.g. cost arrives this
// turn but contextPct was already known — never drops a previously-captured
// field. Only TRUTHY/defined values overwrite; undefined/empty are ignored so a
// transient blank derive (sessionInit not yet loaded) can't WIPE a good snapshot.
// Cheap idempotency guard: skip the write when nothing actually changed, so the
// per-render save call in NativePane isn't a localStorage write every frame.
export function saveVitalsSnapshot(
  paneId: string,
  next: VitalsSnapshot,
  agent?: AgentKind | null,
): void {
  const prev = snapshotByPane.get(paneId) ?? readFromStorage(paneId);
  // Model/context/cost belong to a provider, not merely a pane id. Pane ids are
  // intentionally reused across Claude↔Codex handoffs, so carrying those fields
  // over would briefly paint the old provider's model (most visibly Opus in a
  // Codex footer). Keep only cwd across a provider change; it is pane/project
  // identity and is safe to share.
  const sameAgent = !agent || prev.agent === agent;
  const merged: VitalsSnapshot = sameAgent
    ? { ...prev }
    : { cwd: prev.cwd, cwdFull: prev.cwdFull };
  if (agent) merged.agent = agent;
  if (next.model) merged.model = next.model;
  if (typeof next.contextPct === 'number' && Number.isFinite(next.contextPct)) {
    merged.contextPct = Math.min(1, Math.max(0, next.contextPct));
  }
  if (typeof next.cost === 'number' && Number.isFinite(next.cost) && next.cost >= 0) {
    merged.cost = next.cost;
  }
  if (next.cwd) merged.cwd = next.cwd;
  if (next.cwdFull) merged.cwdFull = next.cwdFull;
  // No-op when unchanged (the common per-render path once vitals settle).
  if (shallowEqual(prev, merged)) return;
  snapshotByPane.set(paneId, merged);
  try {
    localStorage.setItem(keyFor(paneId), JSON.stringify(merged));
  } catch {
    // Storage full / unavailable — the module mirror still serves this session;
    // we just lose cross-reload persistence. Non-fatal.
    pruneOldVitals();
  }
}

// clearVitalsSnapshot — drop a pane's snapshot from BOTH the mirror and
// localStorage. Called when the pane is closed so we don't leak unbounded
// per-pane entries (each pane id is unique forever, so without this a long-lived
// install would accumulate stale solu.nativeVitals.* keys).
export function clearVitalsSnapshot(paneId: string): void {
  snapshotByPane.delete(paneId);
  try {
    localStorage.removeItem(keyFor(paneId));
  } catch {
    // ignore
  }
}

// clearVitalsContextPct — drop ONLY the contextPct from a pane's snapshot, keeping
// model/cost/cwd intact. Called on a compact fallback: older boundaries without
// postTokens make the last-known ctx% stale, and the saved value would otherwise
// resurrect on the next Focus re-entry. Current boundaries append a real usage and
// do not stay in this state. No-op when absent.
export function clearVitalsContextPct(paneId: string, agent?: AgentKind | null): void {
  const prev = snapshotByPane.get(paneId) ?? readFromStorage(paneId);
  if (agent && prev.agent !== agent) return;
  if (prev.contextPct === undefined) return; // nothing to clear
  const next: VitalsSnapshot = { ...prev };
  delete next.contextPct;
  snapshotByPane.set(paneId, next);
  try {
    localStorage.setItem(keyFor(paneId), JSON.stringify(next));
  } catch {
    // Storage unavailable — the module mirror still serves this session.
  }
}

function readFromStorage(paneId: string): VitalsSnapshot {
  try {
    const raw = localStorage.getItem(keyFor(paneId));
    return raw ? sanitizeVitalsSnapshot(JSON.parse(raw)) : {};
  } catch {
    return {};
  }
}

function shallowEqual(a: VitalsSnapshot, b: VitalsSnapshot): boolean {
  return (
    a.agent === b.agent &&
    a.model === b.model &&
    a.contextPct === b.contextPct &&
    a.cost === b.cost &&
    a.cwd === b.cwd &&
    a.cwdFull === b.cwdFull
  );
}

// Return only values safe for the requested provider. Old persisted snapshots
// have no `agent`; treat their model/context/cost as untrusted rather than
// guessing. cwd remains useful and cannot claim the wrong model/account.
function scopeToAgent(snap: VitalsSnapshot, agent?: AgentKind | null): VitalsSnapshot {
  if (!agent || snap.agent === agent) return { ...snap };
  const out: VitalsSnapshot = {};
  if (snap.cwd) out.cwd = snap.cwd;
  if (snap.cwdFull) out.cwdFull = snap.cwdFull;
  return out;
}

// Safety cap so a forgotten clear (or a crash before close) can't grow these keys
// without bound. When a write fails (quota), drop the OLDEST half of our keys.
// LocalStorage has no per-key timestamp, so "oldest" is approximated by insertion
// order (browsers iterate localStorage in insertion order), which is good enough
// for a best-effort prune of a non-critical accelerator cache.
const VITALS_KEY_CAP = 200;
function pruneOldVitals(): void {
  try {
    const keys: string[] = [];
    for (let i = 0; i < localStorage.length; i++) {
      const k = localStorage.key(i);
      if (k && k.startsWith(KEY_PREFIX)) keys.push(k);
    }
    if (keys.length <= VITALS_KEY_CAP) return;
    const drop = keys.slice(0, keys.length - VITALS_KEY_CAP);
    for (const k of drop) {
      localStorage.removeItem(k);
      snapshotByPane.delete(k.slice(KEY_PREFIX.length));
    }
  } catch {
    // ignore — prune is best-effort
  }
}

// Test-only surface: wipe the in-memory mirror so a unit test starts clean (the
// mirror persists across tests in the same module instance, like the other native
// module caches). Not imported by production code.
export const __vitalsSnapshotTest = {
  reset(): void {
    snapshotByPane.clear();
  },
};
