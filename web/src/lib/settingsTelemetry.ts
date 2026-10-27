const STORAGE_KEY = 'solu.settings.telemetry.v1';
const MAX_COUNT = 1_000_000;
const MAX_KEYS = 64;

export interface SettingsTelemetry {
  version: 1;
  legacyTab: Record<string, number>;
  legacySection: Record<string, number>;
  legacyReveal: number;
  searchOpens: number;
  searchNavigations: number;
  firstAt: string;
  lastAt: string;
}

export type SettingsEventKind =
  | 'legacyTab'
  | 'legacySection'
  | 'legacyReveal'
  | 'searchOpens'
  | 'searchNavigations';

let cache: SettingsTelemetry | null = null;
let pendingWrite: number | null = null;

function freshAggregate(now = new Date().toISOString()): SettingsTelemetry {
  return {
    version: 1,
    legacyTab: {},
    legacySection: {},
    legacyReveal: 0,
    searchOpens: 0,
    searchNavigations: 0,
    firstAt: now,
    lastAt: now,
  };
}

function clampCount(value: unknown): number {
  if (typeof value !== 'number' || !Number.isFinite(value)) return 0;
  return Math.min(MAX_COUNT, Math.max(0, Math.floor(value)));
}

function clampRecord(value: unknown): Record<string, number> {
  if (value === null || typeof value !== 'object' || Array.isArray(value)) return {};
  const result: Record<string, number> = {};
  for (const [key, count] of Object.entries(value).slice(0, MAX_KEYS)) {
    result[key] = clampCount(count);
  }
  return result;
}

function copyAggregate(value: SettingsTelemetry): SettingsTelemetry {
  return {
    ...value,
    legacyTab: { ...value.legacyTab },
    legacySection: { ...value.legacySection },
  };
}

export function readSettingsTelemetry(): SettingsTelemetry {
  if (cache === null) {
    try {
      const raw = localStorage.getItem(STORAGE_KEY);
      if (raw) {
        const stored = JSON.parse(raw) as Partial<SettingsTelemetry>;
        if (stored.version === 1) {
          const now = new Date().toISOString();
          cache = {
            version: 1,
            legacyTab: clampRecord(stored.legacyTab),
            legacySection: clampRecord(stored.legacySection),
            legacyReveal: clampCount(stored.legacyReveal),
            searchOpens: clampCount(stored.searchOpens),
            searchNavigations: clampCount(stored.searchNavigations),
            firstAt: typeof stored.firstAt === 'string' ? stored.firstAt : now,
            lastAt: typeof stored.lastAt === 'string' ? stored.lastAt : now,
          };
        }
      }
    } catch {
      // Corrupt or blocked storage starts a fresh aggregate.
    }
    cache ??= freshAggregate();
  }
  return copyAggregate(cache);
}

function scheduleWrite(): void {
  if (pendingWrite !== null) return;
  pendingWrite = window.setTimeout(() => {
    pendingWrite = null;
    try {
      if (cache !== null) localStorage.setItem(STORAGE_KEY, JSON.stringify(cache));
    } catch {
      // Device-local telemetry is best effort in private/quota-limited storage.
    }
  }, 1000);
}

export function recordSettingsEvent(kind: SettingsEventKind, key?: string): void {
  const aggregate = readSettingsTelemetry();
  if (kind === 'legacyTab' || kind === 'legacySection') {
    if (!key) return;
    const counts = aggregate[kind];
    if (!(key in counts) && Object.keys(counts).length >= MAX_KEYS) return;
    counts[key] = Math.min(MAX_COUNT, (counts[key] ?? 0) + 1);
  } else {
    aggregate[kind] = Math.min(MAX_COUNT, aggregate[kind] + 1);
  }
  aggregate.lastAt = new Date().toISOString();
  cache = aggregate;
  scheduleWrite();
}

export function exportSettingsTelemetry(): string {
  return JSON.stringify(readSettingsTelemetry(), null, 2);
}

export function resetSettingsTelemetry(): void {
  if (pendingWrite !== null) {
    window.clearTimeout(pendingWrite);
    pendingWrite = null;
  }
  cache = null;
  try {
    localStorage.removeItem(STORAGE_KEY);
  } catch {
    // A blocked store is already effectively reset for this module.
  }
}
