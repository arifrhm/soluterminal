// slashCommandsCache.ts — a GLOBAL "last known slash commands" cache for the
// Focus (native) pane's slash menu.
//
// WHY this exists: composeSlashCommands (nativeComposer.ts) builds the Focus `/`
// menu from the pane's session_init.slashCommands — the set of skills + custom +
// MCP + headless built-in commands claude reports for the session. That set only
// arrives in a LIVE session_init (fromReplay=false), which is emitted when
// claude (re)spawns and initializes. A pane that hasn't run claude live since
// the last daemon restart / app reload — i.e. a DORMANT pane showing only a
// transcript SEED (whose synthetic session_init carries model/cwd but NO
// tools/slashCommands) — therefore has empty sessionInit.slashCommands and falls
// back to the 7-command curated list, hiding every skill. That is the common
// state right after an auto-update kills every claude process and the user
// reopens a pane before sending a message.
//
// slash_commands are an INSTALLATION-level property: the same skills/plugins/
// built-ins are available in every pane (modulo a few project-local custom
// commands). So a pane that lacks its OWN live init can legitimately reuse the
// last set ANY pane saw. This cache remembers that set globally and persists it
// to localStorage so it survives a full reload / app restart — exactly the
// scenario above. The fallback chain in NativePane becomes:
//   live sessionInit.slashCommands → this global cache → curated 7.
// It only ever ACCELERATES (shows known skills sooner); it never prevents a live
// set from replacing it, and a command that turns out unavailable in a specific
// pane simply no-ops when forwarded (non-destructive).
//
// Pure module + localStorage mirror + test reset hook — mirrors vitalsSnapshot.ts.

import { normalizeSlashName } from './nativeComposer';

// One global key (NOT per-pane): skills are installation-wide, and a per-pane
// key would leave dormant panes empty precisely when we need the fallback.
// Matches the `solu.<thing>` localStorage idiom used across store.ts.
const KEY = 'solu.knownSlashCommands';

// Module-level mirror so an in-session re-entry (warm-pane remount, zoom, drag)
// reads without touching localStorage, like the draft/cleared/vitals caches.
const mirrors: Record<'claude' | 'codex' | 'muse', string[] | null> = { claude: null, codex: null, muse: null };

function storageKey(agent: 'claude' | 'codex' | 'muse'): string {
  if (agent === 'claude') return KEY;
  if (agent === 'codex') return `${KEY}.codex`;
  return `${KEY}.muse`;
}

// sanitizeSlashCommands — coerce an arbitrary parsed blob into a clean, NORMALIZED
// string[]: keep only non-empty strings, normalize each to the "/name" form the
// menu + send path expect (so "context" and "/context" can't both survive as
// distinct cache entries and make sameSet thrash), dedup, drop the rest. A
// corrupt blob degrades to [] (never throws).
export function sanitizeSlashCommands(raw: unknown): string[] {
  if (!Array.isArray(raw)) return [];
  const seen = new Set<string>();
  const out: string[] = [];
  for (const entry of raw) {
    if (typeof entry !== 'string') continue;
    const name = normalizeSlashName(entry);
    if (!name || seen.has(name)) continue;
    seen.add(name);
    out.push(name);
  }
  return out;
}

// getKnownSlashCommands — the last-known command set, or [] if none. Reads the
// module mirror first (fast, survives warm remounts); on a miss it hydrates the
// mirror from localStorage (survives a reload). Returns a defensive copy so a
// caller can't mutate the cached array by reference.
export function getKnownSlashCommands(agent: 'claude' | 'codex' | 'muse' = 'claude'): string[] {
  if (mirrors[agent] !== null) return mirrors[agent]!.slice();
  try {
    const raw = localStorage.getItem(storageKey(agent));
    mirrors[agent] = raw ? sanitizeSlashCommands(JSON.parse(raw)) : [];
  } catch {
    // Corrupt blob or storage unavailable (private mode / quota) — degrade to
    // empty; a live session_init still populates the menu once claude runs.
    mirrors[agent] = [];
  }
  return mirrors[agent]!.slice();
}

// rememberSlashCommands — cache + persist the given non-empty command set.
// Cheap idempotency guard: skip the write when the set is unchanged (same
// members, order-independent) so the per-turn remember call isn't a localStorage
// write every turn. Empty input is ignored (never wipe a good cache with a
// transient empty derive).
export function rememberSlashCommands(names: unknown, agent: 'claude' | 'codex' | 'muse' = 'claude'): void {
  const clean = sanitizeSlashCommands(names);
  if (clean.length === 0) return;
  if (sameSet(mirrors[agent] ?? readMirrorFromStorage(agent), clean)) return;
  mirrors[agent] = clean;
  try {
    localStorage.setItem(storageKey(agent), JSON.stringify(clean));
  } catch {
    // Storage full / unavailable — the module mirror still serves this session;
    // we just lose cross-reload persistence. Non-fatal.
  }
}

function readMirrorFromStorage(agent: 'claude' | 'codex' | 'muse'): string[] {
  try {
    const raw = localStorage.getItem(storageKey(agent));
    return raw ? sanitizeSlashCommands(JSON.parse(raw)) : [];
  } catch {
    return [];
  }
}

// sameSet — order-independent equality of two string arrays (a session can
// report the same commands in a different order across inits; that's not a real
// change worth a localStorage write).
function sameSet(a: string[], b: string[]): boolean {
  if (a === b) return true;
  if (a.length !== b.length) return false;
  const ba = new Set(a);
  for (const x of b) if (!ba.has(x)) return false;
  return true;
}

// Test-only surface: wipe the in-memory mirror so a unit test starts clean (the
// mirror persists across tests in the same module instance). Not imported by
// production code.
export const __slashCommandsCacheTest = {
  reset(): void {
    mirrors.claude = null;
    mirrors.codex = null;
  },
};
