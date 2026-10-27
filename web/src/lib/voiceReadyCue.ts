// The "mic is listening" cue. A cold mic takes ~0.1 s to open, and anything
// said before it does is lost; a soft tick at the moment recording really
// starts tells the user when to speak. Shinoque plays the same tick.

let ctx: AudioContext | null = null;

/** Plays a short, quiet tick. Silently does nothing where audio is unavailable. */
export function playReadyTick(): void {
  try {
    const Ctor =
      (window as unknown as { AudioContext?: typeof AudioContext; webkitAudioContext?: typeof AudioContext })
        .AudioContext ??
      (window as unknown as { webkitAudioContext?: typeof AudioContext }).webkitAudioContext;
    if (!Ctor) return;
    ctx ??= new Ctor();
    if (ctx.state === 'suspended') void ctx.resume();
    const now = ctx.currentTime;
    const osc = ctx.createOscillator();
    const gain = ctx.createGain();
    osc.type = 'sine';
    osc.frequency.setValueAtTime(1320, now);
    gain.gain.setValueAtTime(0.0001, now);
    gain.gain.exponentialRampToValueAtTime(0.06, now + 0.005);
    gain.gain.exponentialRampToValueAtTime(0.0001, now + 0.06);
    osc.connect(gain).connect(ctx.destination);
    osc.start(now);
    osc.stop(now + 0.07);
  } catch {
    /* no audio output: the visual state still shows recording */
  }
}

type Phase = 'idle' | 'starting' | 'recording' | 'draining' | 'recovery' | 'transcribing';

/**
 * Returns a session observer that cues once per recording, on the transition
 * into "recording" (the hook reports that only after the recorder started).
 */
export function makeReadyCue(enabled: () => boolean, play: () => void = playReadyTick): (phase: Phase) => void {
  let cued = false;
  return (phase) => {
    if (phase === 'idle') cued = false;
    if (phase === 'recording' && !cued) { cued = true; if (enabled()) play(); }
  };
}
