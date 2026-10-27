import type { VoiceEvent } from '../voice';

// Short synthesized alert chimes — the non-spoken alternative to the voice
// clips. Each event has its own shape so you can tell them apart without
// looking: "needs approval" is a bright rising double, "reply ready / done"
// a soft falling pair, schedules a single mid note. WebAudio oscillators, no
// assets, so it works in a browser tab as well as the desktop app.

type Note = { freq: number; at: number; dur: number; gain?: number };

export const chimePatterns: Record<VoiceEvent, Note[]> = {
  approval: [
    { freq: 880, at: 0, dur: 0.11 },
    { freq: 1318.5, at: 0.12, dur: 0.16 },
    { freq: 880, at: 0.34, dur: 0.11 },
    { freq: 1318.5, at: 0.46, dur: 0.2 },
  ],
  'reply-ready': [
    { freq: 659.25, at: 0, dur: 0.16, gain: 0.8 },
    { freq: 523.25, at: 0.15, dur: 0.28, gain: 0.8 },
  ],
  'schedule-soon': [{ freq: 587.33, at: 0, dur: 0.22, gain: 0.7 }],
  'schedule-fired': [{ freq: 698.46, at: 0, dur: 0.22, gain: 0.7 }],
  'update-available': [
    { freq: 523.25, at: 0, dur: 0.12, gain: 0.6 },
    { freq: 659.25, at: 0.12, dur: 0.12, gain: 0.6 },
    { freq: 783.99, at: 0.24, dur: 0.2, gain: 0.6 },
  ],
};

let ctx: AudioContext | null = null;

function audioContext(): AudioContext | null {
  if (ctx) return ctx;
  const Ctor = (window as unknown as { AudioContext?: typeof AudioContext; webkitAudioContext?: typeof AudioContext }).AudioContext
    ?? (window as unknown as { webkitAudioContext?: typeof AudioContext }).webkitAudioContext;
  if (!Ctor) return null;
  try { ctx = new Ctor(); } catch { return null; }
  return ctx;
}

// Browsers keep an AudioContext suspended until a user gesture. Unlock it on
// the first pointer/key press so a chime can play later while you're looking
// at another window.
if (typeof window !== 'undefined') {
  const unlock = () => {
    const c = audioContext();
    if (c && c.state === 'suspended') void c.resume().catch(() => {});
    window.removeEventListener('pointerdown', unlock, true);
    window.removeEventListener('keydown', unlock, true);
  };
  window.addEventListener('pointerdown', unlock, true);
  window.addEventListener('keydown', unlock, true);
}

export function playChime(event: VoiceEvent, volume = 0.35): void {
  const c = audioContext();
  if (!c) return;
  if (c.state === 'suspended') void c.resume().catch(() => {});
  const start = c.currentTime + 0.02;
  for (const n of chimePatterns[event] ?? []) {
    const osc = c.createOscillator();
    const g = c.createGain();
    osc.type = 'sine';
    osc.frequency.value = n.freq;
    const peak = volume * (n.gain ?? 1);
    const t0 = start + n.at;
    g.gain.setValueAtTime(0.0001, t0);
    g.gain.exponentialRampToValueAtTime(peak, t0 + 0.012);
    g.gain.exponentialRampToValueAtTime(0.0001, t0 + n.dur);
    osc.connect(g).connect(c.destination);
    osc.start(t0);
    osc.stop(t0 + n.dur + 0.02);
  }
}
