import { useEffect, useState } from 'react';
import { useStore } from '../store';

// Refresh age from the immutable completion epoch. Background timer throttling
// must not leave the chip/diff clock stale when the workspace becomes visible.
export function useTurnClock(selection: number | string | undefined): number {
  const workspace = useStore((s) => s.activeWorkspace);
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    let timer: ReturnType<typeof setInterval> | undefined;
    const refresh = () => setNow(Date.now());
    const visibility = () => {
      if (timer !== undefined) clearInterval(timer);
      timer = undefined;
      if (document.visibilityState !== 'hidden') {
        refresh();
        timer = setInterval(refresh, 5_000);
      }
    };
    visibility();
    document.addEventListener('visibilitychange', visibility);
    window.addEventListener('pageshow', refresh);
    window.addEventListener('focus', refresh);
    return () => {
      if (timer !== undefined) clearInterval(timer);
      document.removeEventListener('visibilitychange', visibility);
      window.removeEventListener('pageshow', refresh);
      window.removeEventListener('focus', refresh);
    };
  }, [workspace, selection]);
  return now;
}
