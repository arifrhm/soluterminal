// An open microphone, plus the stream the recorder should record.
//
// With a lead-in, the mic runs through a Web Audio delay line and the
// recorder records the delayed copy. Each clip then begins with
// `leadInMs` of audio from just before the press: silence on a freshly
// opened mic, the real pre-press audio on a warm one. Words that start at
// sample 0 are the ones Whisper misreads most ("mic" -> "like").

export interface Capture {
  /** The raw mic stream (owns the device and the OS indicator). */
  mic: MediaStream;
  /** What MediaRecorder records: the delayed copy, or `mic` itself. */
  record: MediaStream;
  /** Lead-in actually applied (0 when the delay line was unavailable). */
  leadInMs: number;
  context?: AudioContext;
  delayed?: AudioNode;
  close: () => void;
}

const MIC_CONSTRAINTS: MediaTrackConstraints = {
  // Explicit: the browser default `audio: true` often leaves these off on
  // mobile, and each one helps Whisper on laptop mics and in noisy rooms.
  echoCancellation: true,
  noiseSuppression: true,
  autoGainControl: true,
};

type AudioContextCtor = new () => AudioContext;

function audioContextCtor(): AudioContextCtor | null {
  if (typeof window === 'undefined') return null;
  const w = window as unknown as {
    AudioContext?: AudioContextCtor;
    webkitAudioContext?: AudioContextCtor;
  };
  return w.AudioContext ?? w.webkitAudioContext ?? null;
}

/** Opens the mic. Throws whatever getUserMedia throws. */
export async function openCapture(leadInMs: number, requireGraph = false): Promise<Capture> {
  const mic = await navigator.mediaDevices.getUserMedia({ audio: MIC_CONSTRAINTS });
  const stopMic = () => {
    for (const t of mic.getTracks()) t.stop();
  };
  const Ctor = leadInMs > 0 || requireGraph ? audioContextCtor() : null;
  if (!Ctor) {
    if (requireGraph) {
      stopMic();
      throw new Error('Continuous recording unavailable in this browser');
    }
    return { mic, record: mic, leadInMs: 0, close: stopMic };
  }
  let context: AudioContext | undefined;
  try {
    const ctx = new Ctor();
    context = ctx;
    if (ctx.state === 'suspended') {
      // A key or pointer press started this, so resume is allowed; don't
      // wait on it — the graph starts flowing as soon as it resumes.
      void ctx.resume().catch(() => {});
    }
    const source = ctx.createMediaStreamSource(mic);
    const delay = ctx.createDelay(Math.max(1, leadInMs / 1000 + 0.1));
    delay.delayTime.value = leadInMs / 1000;
    const dest = ctx.createMediaStreamDestination();
    dest.channelCount = 1;
    source.connect(delay);
    delay.connect(dest);
    return {
      mic,
      context: ctx,
      delayed: delay,
      record: dest.stream,
      leadInMs,
      close: () => {
        try {
          source.disconnect();
        } catch {
          /* ignore */
        }
        for (const t of dest.stream.getTracks()) t.stop();
        void ctx.close().catch(() => {});
        stopMic();
      },
    };
  } catch (err) {
    void context?.close().catch(() => {});
    if (requireGraph) {
      stopMic();
      throw err;
    }
    // No Web Audio (or it refused this stream): record the mic directly.
    return { mic, record: mic, leadInMs: 0, close: stopMic };
  }
}

/** True while the device behind a capture is still usable. */
export function captureLive(c: Capture | null): c is Capture {
  return !!c && c.mic.getTracks().some((t) => t.readyState === 'live');
}
