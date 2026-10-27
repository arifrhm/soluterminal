import type { Port } from '../types';

// What a fresh browser tile opens. A dev server started from one of the
// user's panes is the likeliest thing they want to see. With none, the tile
// shows its local start page (an empty URL) instead of a public site, since
// most public sites refuse to load inside a tile.
export function defaultBrowserUrl(_ports: readonly Port[]): string {
  // The start page offers workspace services with their complete routing
  // identity. A numeric port alone cannot safely choose a backend or an app.
  return '';
}
