// Recording feedback — a live elapsed timer + an ambient "equalizer" so a
// hold-to-talk recording never feels like a silent void. Lifted verbatim from
// the ARIA comms composer (the reference design).
//
// IMPORTANT — the bars are DECORATIVE, not a real amplitude meter. soluvoice
// owns the MediaStream internally and surfaces no audio level (and we
// deliberately don't open a SECOND getUserMedia just for eye candy). The
// staggered CSS animation signals "actively listening"; the TIMER is the real,
// accurate signal — the same honest approach WhatsApp/Slack use where raw
// levels aren't available.

import { useEffect, useRef, useState } from 'react';

/** Elapsed ms since `active` last went true; 0 while inactive. Ticks ~5×/s —
 *  enough to feel live for a seconds-scale counter without burning a rAF. */
export function useElapsedMs(active: boolean): number {
  const [ms, setMs] = useState(0);
  const startRef = useRef(0);
  useEffect(() => {
    if (!active) {
      setMs(0);
      return;
    }
    startRef.current = Date.now();
    setMs(0);
    const id = window.setInterval(() => {
      setMs(Date.now() - startRef.current);
    }, 200);
    return () => window.clearInterval(id);
  }, [active]);
  return ms;
}

/** "0:03", "1:07" — minutes:seconds, seconds zero-padded. */
export function formatElapsed(ms: number): string {
  const total = Math.max(0, Math.floor(ms / 1000));
  const m = Math.floor(total / 60);
  const s = total % 60;
  return `${m}:${s.toString().padStart(2, '0')}`;
}

/** Ambient recording equalizer (decorative — see file header). */
export function EqualizerBars({ className = '' }: { className?: string }): React.ReactElement {
  return (
    <span className={`eq-bars ${className}`.trim()} aria-hidden="true">
      {[0, 1, 2, 3, 4].map((i) => (
        <span key={i} className="eq-bar" style={{ animationDelay: `${i * 0.11}s` }} />
      ))}
    </span>
  );
}

/** The combined chip: rippling bars + a live mm:ss timer. */
export function RecordingMeter({
  elapsedMs,
  className = '',
}: {
  elapsedMs: number;
  className?: string;
}): React.ReactElement {
  return (
    <span className={`recording-meter ${className}`.trim()}>
      <EqualizerBars />
      <span className="recording-time">{formatElapsed(elapsedMs)}</span>
    </span>
  );
}
