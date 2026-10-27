// Registers the optional feature modules under ./private (see extensions.ts).
// The glob matches nothing in a tree without them.
import { registerExtensions, type WebExtension } from './extensions';

const modules = import.meta.glob<{ default?: WebExtension }>(
  './private/**/*.register.{ts,tsx}',
  { eager: true },
);

registerExtensions(
  Object.keys(modules)
    .sort()
    .map((path) => modules[path].default)
    .filter((ext): ext is WebExtension => !!ext),
);
