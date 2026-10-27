export function isSoluDebugEnabled(): boolean {
  if (import.meta.env.DEV) return true;
  try {
    return localStorage.getItem('solu.debug') === '1';
  } catch {
    return false;
  }
}

export function debugLog(...args: Parameters<typeof console.log>): void {
  if (isSoluDebugEnabled()) {
    // eslint-disable-next-line no-console
    console.log(...args);
  }
}

export function debugTable(tabularData: unknown, properties?: string[]): void {
  if (isSoluDebugEnabled()) {
    // eslint-disable-next-line no-console
    console.table(tabularData, properties);
  }
}

const LIFECYCLE_KEY = 'solu.lifecycle.events';
const LIFECYCLE_MAX = 300;

type LifecycleEntry = {
  t: string;
  name: string;
  detail?: unknown;
};

// In-memory ring. lifecycleLog used to read+JSON.parse+push+stringify+
// write the full localStorage blob on EVERY call — synchronous
// main-thread (and, in WebView2, disk-backed) I/O that fired dozens of
// times per workspace switch and on every resize/visibility event.
// Entries now accumulate in memory; localStorage persistence happens
// only when the debug flag is on (so a post-crash dump is available
// while actively debugging) and is debounced to one write per second.
let lifecycleEntries: LifecycleEntry[] = [];
let lifecyclePersistTimer: number | null = null;

function readPersistedLifecycleEntries(): LifecycleEntry[] {
  try {
    const raw = localStorage.getItem(LIFECYCLE_KEY);
    if (!raw) return [];
    const parsed = JSON.parse(raw);
    return Array.isArray(parsed) ? parsed : [];
  } catch {
    return [];
  }
}

function persistLifecycleEntries(): void {
  try {
    localStorage.setItem(LIFECYCLE_KEY, JSON.stringify(lifecycleEntries.slice(-LIFECYCLE_MAX)));
  } catch {
    // best effort only
  }
}

function schedulePersist(): void {
  if (!isSoluDebugEnabled()) return;
  if (lifecyclePersistTimer !== null) return;
  lifecyclePersistTimer = window.setTimeout(() => {
    lifecyclePersistTimer = null;
    persistLifecycleEntries();
  }, 1000);
}

export function lifecycleLog(name: string, detail?: unknown): void {
  lifecycleEntries.push({ t: new Date().toISOString(), name, detail });
  if (lifecycleEntries.length > LIFECYCLE_MAX) {
    lifecycleEntries = lifecycleEntries.slice(-LIFECYCLE_MAX);
  }
  schedulePersist();
  if (isSoluDebugEnabled()) {
    // eslint-disable-next-line no-console
    console.log('[life]', name, detail ?? '');
  }
}

export function installLifecycleDebugGlobal(): void {
  if (typeof window === 'undefined') return;
  // Seed the in-memory ring from any previously persisted entries so a
  // dump right after reload still shows the pre-reload trail.
  lifecycleEntries = readPersistedLifecycleEntries();
  // Crash/exit forensics: the ring lives in memory now, so flush it to
  // localStorage at the moments that historically mattered for post-
  // mortems — page teardown and unhandled errors — regardless of the
  // debug flag. One synchronous write at crash time costs nothing; the
  // per-call write on the hot path was the thing being removed.
  window.addEventListener('pagehide', persistLifecycleEntries);
  window.addEventListener('error', () => {
    lifecycleEntries.push({ t: new Date().toISOString(), name: 'window.error' });
    persistLifecycleEntries();
  });
  window.addEventListener('unhandledrejection', () => {
    lifecycleEntries.push({ t: new Date().toISOString(), name: 'window.unhandledrejection' });
    persistLifecycleEntries();
  });
  const api = {
    dump: () => lifecycleEntries.slice(),
    clear: () => {
      lifecycleEntries = [];
      persistLifecycleEntries();
    },
    enableConsole: () => {
      try { localStorage.setItem('solu.debug', '1'); } catch { /* ignore */ }
    },
  };
  (window as unknown as { __SOLU_LIFECYCLE__?: typeof api }).__SOLU_LIFECYCLE__ = api;
}
