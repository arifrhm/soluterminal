// Tracks when this page last saw a REAL user gesture (pointer, key, touch,
// wheel). Consumed by the pane auto-claim paths: a standing claim intent
// ("Take over" tapped earlier) must only re-assert itself on reconnect while
// a human is actually driving this page — an idle-but-open laptop or a
// pocketed phone whose sockets cycle on relay timeouts must NOT keep
// re-taking panes from the device the user is really working on.
//
// Listeners are capture-phase and passive, registered once at module load;
// the cost is a timestamp write per gesture.

let lastGestureAt = 0;

if (typeof window !== 'undefined') {
  const stamp = () => {
    lastGestureAt = Date.now();
  };
  for (const ev of ['pointerdown', 'keydown', 'touchstart', 'wheel'] as const) {
    window.addEventListener(ev, stamp, { capture: true, passive: true });
  }
}

// A generous window: someone reading output between keystrokes is still
// "driving". The daemon has its own, independent arbitration — this gate
// only stops the client from ASKING when nobody is here.
const RECENT_GESTURE_MS = 120_000;

export function userRecentlyActive(): boolean {
  return lastGestureAt !== 0 && Date.now() - lastGestureAt <= RECENT_GESTURE_MS;
}
