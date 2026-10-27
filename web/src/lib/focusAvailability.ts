import { useDaemonHealth } from './useDaemonHealth';

// Focus (native renderer) was retired in 2026-10. The daemon reports whether it
// is available (`focusEnabled` on /api/health, on only with
// SOLU_NATIVE_RENDERER=1); every Focus entry point — new-session chips, the
// header renderer toggle, the palette switch, Focus settings — hides when it
// is off. A pane that is still native keeps its way BACK to Terminal.
// Missing field (an older daemon) reads as off: the retirement is the intent.
export function useFocusEnabled(): boolean {
  return useDaemonHealth().health?.focusEnabled === true;
}
