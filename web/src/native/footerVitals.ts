import type { AgentKind } from '../lib/agentKind';
import type { NativeEvent } from './nativeEvents';
import { contextLimitForModel } from './modelInfo';

function boundedFraction(used: number, limit: number): number | undefined {
  if (!Number.isFinite(used) || !Number.isFinite(limit) || used <= 0 || limit <= 0) return undefined;
  return Math.min(1, Math.max(0, used / limit));
}

// Claude reports uncached input, cache reads, and cache writes separately, so
// all three make up the prompt occupying its context window. Codex reports an
// authoritative total plus its model's window; its input count already includes
// cached tokens, so summing the cache count again would inflate the meter.
export function contextUsageFraction(
  usage: NativeEvent | undefined,
  agent: AgentKind | null,
  model?: string,
): number | undefined {
  if (!usage) return undefined;
  if (agent === 'codex' || agent === 'muse') {
    return boundedFraction(
      usage.totalTokens || usage.inputTokens || 0,
      usage.contextWindowTokens || 0,
    );
  }

  const occupied =
    (usage.inputTokens || 0) +
    (usage.cachedInputTokens || 0) +
    (usage.cacheCreationTokens || 0);
  return boundedFraction(occupied, contextLimitForModel(model, occupied));
}

// A time is enough for today's reset. Longer-window resets need a weekday and
// date; otherwise a weekly limit can misleadingly look as though it resets today.
export function formatRateLimitReset(
  epochSeconds: number | undefined,
  nowMs = Date.now(),
  locale?: string,
): string {
  if (!epochSeconds || !Number.isFinite(epochSeconds)) return '';
  const reset = new Date(epochSeconds * 1000);
  if (Number.isNaN(reset.getTime())) return '';
  const now = new Date(nowMs);
  const sameDay =
    reset.getFullYear() === now.getFullYear() &&
    reset.getMonth() === now.getMonth() &&
    reset.getDate() === now.getDate();
  return reset.toLocaleString(locale, sameDay
    ? { hour: 'numeric', minute: '2-digit' }
    : { weekday: 'short', month: 'short', day: 'numeric', hour: 'numeric', minute: '2-digit' });
}
