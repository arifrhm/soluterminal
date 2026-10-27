// metaBarFields.ts — the set of toggleable "vitals" the native pane's bottom status
// bar (NativeMetaBar) can show in its right cluster, plus the default set. A global,
// persisted user preference (solu.nativeMetaBar) gates each one. Kept in its own
// module so the store (which owns the persisted pref) and the component (which
// renders it) share ONE source of truth for the shape + defaults.

export interface NativeMetaBarFields {
  model: boolean; // pretty model name, e.g. "Opus 4.8"
  context: boolean; // context-window used %, e.g. "ctx 42%"
  tokens: boolean; // ↑input ↓output token counts for the latest provider usage update
  cost: boolean; // cumulative session cost, e.g. "$0.18"
  cache: boolean; // ⚡ cached-input tokens
  resets: boolean; // rate-limit reset time
  cwd: boolean; // working directory (dim, truncated)
}

// Order the toggles render in (Preferences menu) AND the order the vitals paint in
// the bar: identity → context → traffic → cost → limits → location. One list so the
// two never drift.
export const NATIVE_METABAR_ORDER: ReadonlyArray<[keyof NativeMetaBarFields, string]> = [
  ['model', 'Model'],
  ['context', 'Context %'],
  ['tokens', 'Tokens'],
  ['cost', 'Cost'],
  ['cache', 'Cache'],
  ['resets', 'Resets'],
  ['cwd', 'Working dir'],
];

// Defaults: model/context/tokens/cost + the working dir on; cache/resets off
// (opt-in via the Preferences gear menu). cwd is on by default because the folder
// path was always shown in the old header — keeping it off was a regression.
export const NATIVE_METABAR_DEFAULTS: NativeMetaBarFields = {
  model: true,
  context: true,
  tokens: true,
  cost: true,
  cache: false,
  resets: false,
  cwd: true,
};

// sanitizeMetaBarFields — coerce a parsed localStorage blob to a complete, typed
// fields object: keep only known boolean keys, fall back to the default for any
// missing/non-boolean key. So a partial or stale blob (e.g. saved before a new
// field existed) still yields every flag, and a corrupt blob falls back wholesale.
export function sanitizeMetaBarFields(raw: unknown): NativeMetaBarFields {
  if (!raw || typeof raw !== 'object') return { ...NATIVE_METABAR_DEFAULTS };
  const out = { ...NATIVE_METABAR_DEFAULTS };
  for (const k of Object.keys(NATIVE_METABAR_DEFAULTS) as (keyof NativeMetaBarFields)[]) {
    const v = (raw as Record<string, unknown>)[k];
    if (typeof v === 'boolean') out[k] = v;
  }
  return out;
}
