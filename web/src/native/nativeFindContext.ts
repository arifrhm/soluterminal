import { createContext } from 'react';

// Shared find state for the native/Focus renderer. While an in-pane find is
// active with a non-empty query, every foldable view (thinking blocks, tool
// groups, collapsed error output, per-edit diffs) force-opens so its text is
// mounted in the DOM — otherwise the CSS-highlight range walk (useNativeFind)
// can't see it, and the match count would silently undercount folded content.
//
// This mirrors the terminal renderer, where xterm's SearchAddon searches the
// full scrollback regardless of what's currently on screen. `active` is derived
// from (find bar open && query non-empty), so merely opening the empty bar does
// NOT expand the whole transcript — expansion only happens once you type.
//
// Force-open overrides the user's per-view collapse toggle for the duration of
// the search; when find closes, every view reverts to its own `open` state.
export interface NativeFindState {
  active: boolean;
}

export const NativeFindContext = createContext<NativeFindState>({ active: false });
