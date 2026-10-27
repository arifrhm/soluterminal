// modelInfo.ts — small, framework-free helpers for turning a raw Claude model id
// (as it arrives on a native pane's session_init, e.g. "claude-opus-5" or
// "claude-fable-5-1[1m]") into the two things the bottom-bar vitals need: a human
// label ("Opus 5") and a context-window capacity (200k, or 1M for the [1m]
// models). Kept out of any component so it's unit-testable and reusable.

// prettyModelName — "claude-fable-5-1" → "Fable 5.1". Strips a leading provider
// prefix and any "[1m]"/"(1m)" context-window marker, then formats the trailing
// "<family>-<major>-<minor>" as "Family Major.Minor". Falls back to the cleaned id
// when it doesn't match the version pattern, so unknown/aliased models still render
// something sane rather than blank.
export function prettyModelName(id?: string): string {
  if (!id) return '';
  let s = id.trim();
  s = s.replace(/[[(]\s*1m\s*[\])]/i, '').trim(); // drop "[1m]" / "(1m)"
  s = s.replace(/^(claude-|anthropic[./])/i, ''); // drop provider prefix
  // Codex/OpenAI model ids commonly use dotted versions and meaningful suffixes
  // (for example gpt-5.3-codex). Preserve that identity instead of applying the
  // Claude dash-version parser, which would truncate it to "Gpt 5".
  if (/^gpt-/i.test(s)) return s.replace(/^gpt-/i, 'GPT ');
  // Major-only families exist ("claude-opus-5", "claude-sonnet-5"), so the
  // minor is optional — but capped at 2 digits so a date suffix
  // ("…-5-20260101") can never masquerade as a minor version.
  const m = s.match(/^([a-z]+(?:-[a-z]+)*)-(\d+)(?:-(\d{1,2})\b)?/i);
  if (!m) return s;
  const family = m[1]
    .split('-')
    .map((p) => p.charAt(0).toUpperCase() + p.slice(1))
    .join(' ');
  return m[3] ? `${family} ${m[2]}.${m[3]}` : `${family} ${m[2]}`;
}

// modelIdForAgent rejects model ids that positively belong to the other agent.
// Unknown ids are allowed because both providers can expose custom aliases, but
// a Claude-family id can never be authoritative for Codex (and vice versa). This
// is a final UI boundary against stale/mixed replay data; the daemon and vitals
// cache also keep provider histories separate.
export function modelIdForAgent(
  id: string | undefined,
  agent: 'claude' | 'codex' | 'muse' | null,
): string | undefined {
  const model = id?.trim();
  if (!model || !agent) return model || undefined;
  const normalized = model.toLowerCase();
  const claudeFamily = /(^|[\s./:_-])(claude|anthropic|opus|sonnet|haiku|fable|mythos)(?=$|[\s./:_-])/i;
  const openAIFamily = /(^|[\s./:_-])(gpt|codex|o[1-9]|muse|spark)(?=$|[\s./:_-])/i;
  if ((agent === 'codex' || agent === 'muse') && claudeFamily.test(normalized)) return undefined;
  if (agent === 'claude' && openAIFamily.test(normalized)) return undefined;
  return model;
}

// The prompt-token capacities we model. 200k is the floor (Haiku, older models);
// the modern Claude families ship a 1M window. NOTE: this is NOT the same as
// daemon/aria_stream.go's ariaContextWindow — ARIA calls the API directly WITHOUT
// the 1M beta, so it's legitimately 200k. Native panes run `claude` (Claude Code),
// which gives the model its full native window, so they're 1M for the families
// below. Don't "sync" the two — they intentionally diverge.
export const CONTEXT_LIMIT_DEFAULT = 200_000;
export const CONTEXT_LIMIT_1M = 1_000_000;

// isNativelyOneMillion — Claude families whose context window is 1M by DEFAULT in
// Claude Code (no beta marker needed): Opus 4.6+, Opus 5.x, Sonnet 4.6, and the
// Fable/Mythos 5 family. Haiku and pre-4.6 models stay at the 200k default.
// Claude Code reports these native windows with plain ids such as "claude-opus-5"
// on session_init, without a "[1m]" suffix. Marker-only detection would therefore
// divide by 200k and over-report context usage.
function isNativelyOneMillion(id: string): boolean {
  const s = id.toLowerCase();
  if (/opus-(?:4-[6789]|5(?:\b|-))/.test(s)) return true; // Opus 4.6+ / 5.x
  if (/sonnet-4-6/.test(s)) return true;
  if (/(fable|mythos)-5/.test(s)) return true;
  // GLM-5.x (Z.ai via the GLM Coding Plan): the `glm` wrapper runs claude on the
  // 1M context (glm-5.2[1m] + CLAUDE_CODE_AUTO_COMPACT_WINDOW=1000000), but
  // session_init reports the id WITHOUT the "[1m]" suffix (e.g. "glm-5.2"), so —
  // exactly like Opus 4.8 above — the marker check misses and it'd default to 200k
  // and over-report ctx% ~5×. Match glm-5+ explicitly. (glm-4.7 is the small/fast
  // helper model, not the session model, so it's intentionally excluded.)
  if (/glm-[5-9]/.test(s)) return true;
  return false;
}

// contextLimitForModel — the model's context-window size in tokens. Resolved from
// the model FAMILY (the authoritative signal), with two fallbacks: an explicit
// "[1m]" marker (the Opus 4.6-era beta alias) always means 1M, and an occupancy
// that has already crossed the 200k default proves a larger window even for an
// unrecognised id (occupancy above a window is impossible). Never under-reports the
// window — under-reporting would pin a 1M session at a bogus ">100%".
export function contextLimitForModel(id?: string, occupancyTokens = 0): number {
  if (id) {
    if (/\b1m\b/i.test(id)) return CONTEXT_LIMIT_1M;
    if (isNativelyOneMillion(id)) return CONTEXT_LIMIT_1M;
  }
  if (occupancyTokens > CONTEXT_LIMIT_DEFAULT) return CONTEXT_LIMIT_1M;
  return CONTEXT_LIMIT_DEFAULT;
}

// ---------------------------------------------------------------------------
// The /model picker's curated options. IDs verified live against claude 2.1.258
// (each resolves as `--model <id>` and echoes back on session_init). There is
// deliberately NO "default" option: `--resume` makes the session's stored model
// sticky, so omitting the flag doesn't return a session to the settings default
// — it silently keeps whatever the session last ran. Only explicit ids are
// honest choices here.
// ---------------------------------------------------------------------------
export interface ModelOption {
  id: string; // the exact --model value
  label: string; // human name shown in the picker
  hint: string; // one-line positioning
}

export const MODEL_OPTIONS: ModelOption[] = [
  { id: 'claude-fable-5-1', label: 'Fable 5.1', hint: 'most capable' },
  { id: 'claude-opus-5', label: 'Opus 5', hint: 'flagship workhorse' },
  { id: 'claude-sonnet-5', label: 'Sonnet 5', hint: 'fast, everyday coding' },
  { id: 'claude-haiku-4-5', label: 'Haiku 4.5', hint: 'fastest, cheapest' },
];

// sameModelId — do two model ids name the same model? Normalizes case and the
// "[1m]"/"(1m)" context-window marker (a 1M-window Fable is still Fable) so the
// picker can mark the CURRENT row from session_init's reported id.
export function sameModelId(a?: string, b?: string): boolean {
  const norm = (s?: string) =>
    (s ?? '')
      .trim()
      .toLowerCase()
      .replace(/[[(]\s*1m\s*[\])]/, '')
      .trim();
  const na = norm(a);
  const nb = norm(b);
  return na !== '' && na === nb;
}
