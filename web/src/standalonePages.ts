// Pages that replace the whole app shell at one exact path, such as the
// owner-only metrics page. Like the UI slots in extensions.ts, they live under
// ./private: each *.page.ts default-exports a StandalonePage, and a tree
// without ./private has none, so every path falls through to the app.
import type { ComponentType } from 'react';

export interface StandalonePage {
  /** Exact path, without a trailing slash (for example "/admin/metrics"). */
  path: string;
  /** Loads the page lazily, so the app bundle does not carry it. */
  load: () => Promise<{ default: ComponentType }>;
}

const modules = import.meta.glob<{ default?: StandalonePage }>('./private/**/*.page.ts', { eager: true });

const pages: StandalonePage[] = Object.keys(modules)
  .sort()
  .map((key) => modules[key].default)
  .filter((p): p is StandalonePage => !!p && typeof p.path === 'string' && typeof p.load === 'function');

export function standalonePageFor(pathname: string, registered: StandalonePage[] = pages): StandalonePage | null {
  const path = pathname.length > 1 ? pathname.replace(/\/+$/, '') : pathname;
  return registered.find((p) => p.path === path) ?? null;
}
