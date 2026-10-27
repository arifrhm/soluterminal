import { captureLive, openCapture, type Capture } from './capture.js';
import { pcmWorkletSource } from './pcmWorklet.js';
import type { CaptureProgress, CaptureSegment } from './types.js';

export const PCM_RATE = 16000;
export const PCM_TRANSPORT_BYTES = 32768 + 32000;
/** Each blob is a complete RIFF/WAVE file, never a MediaRecorder fragment. */
export function encodeWav(samples: Int16Array): Blob {
  const buffer = new ArrayBuffer(44 + samples.length * 2);
  const view = new DataView(buffer);
  const word = (offset: number, value: string) => {
    for (let i = 0; i < value.length; i++) view.setUint8(offset + i, value.charCodeAt(i));
  };
  word(0, 'RIFF');
  view.setUint32(4, buffer.byteLength - 8, true);
  word(8, 'WAVE');
  word(12, 'fmt ');
  view.setUint32(16, 16, true);
  view.setUint16(20, 1, true);
  view.setUint16(22, 1, true);
  view.setUint32(24, PCM_RATE, true);
  view.setUint32(28, PCM_RATE * 2, true);
  view.setUint16(32, 2, true);
  view.setUint16(34, 16, true);
  word(36, 'data');
  view.setUint32(40, samples.length * 2, true);
  for (let i = 0; i < samples.length; i++) view.setInt16(44 + i * 2, samples[i], true);
  return new Blob([buffer], { type: 'audio/wav' });
}

/** Segmenter owns at most 60 seconds PCM, in addition to the emitted container. */
export class PCMSegmenter {
  private batches: Int16Array[] = [];
  private frames = 0;
  private total = 0;
  private quiet = 0;
  private index = 0;
  constructor(private emit: (segment: CaptureSegment) => void) {}
  get progress(): CaptureProgress {
    return {
      capturedMs: this.total / 16,
      activeBytes: this.frames * 2 + PCM_TRANSPORT_BYTES,
      segments: this.index,
    };
  }
  push(pcm: Int16Array) {
    let begin = 0;
    for (let i = 0; i < pcm.length; i++) {
      this.frames++;
      this.total++;
      this.quiet = Math.abs(pcm[i]) < 184 ? this.quiet + 1 : 0; // -45 dBFS
      if (this.frames >= 960000 || (this.frames >= 880000 && this.quiet >= 3200)) {
        this.batches.push(pcm.slice(begin, i + 1));
        begin = i + 1;
        this.flush();
      }
    }
    if (begin < pcm.length) this.batches.push(pcm.slice(begin));
  }
  flush() {
    if (!this.frames) return;
    const samples = new Int16Array(this.frames);
    let offset = 0;
    for (const b of this.batches) {
      samples.set(b, offset);
      offset += b.length;
    }
    const frames = this.frames;
    this.batches = [];
    this.frames = 0;
    this.quiet = 0;
    this.emit({
      index: this.index++,
      blob: encodeWav(samples),
      frames,
      durationMs: frames / 16,
      endFrame: this.total,
    });
  }
}

export interface ContinuousCaptureOptions {
  leadInMs?: number;
  keepWarmMs?: number;
  onSegment: (segment: CaptureSegment) => void;
  onProgress?: (progress: CaptureProgress) => void;
  onInterrupted?: (message: string) => void;
}
export interface ContinuousCapture {
  start: () => Promise<void>;
  stop: () => Promise<void>;
  cancel: () => void;
  dispose: () => void;
  subscribe: (listener: (progress: CaptureProgress) => void) => () => void;
}

let warmGraph: {
  capture: Capture;
  node: AudioWorkletNode;
  timer: ReturnType<typeof setTimeout>;
} | null = null;
export function closeWarmCapture(): void {
  if (!warmGraph) return;
  clearTimeout(warmGraph.timer);
  warmGraph.node.port.close();
  warmGraph.node.disconnect();
  warmGraph.capture.close();
  warmGraph = null;
}
if (typeof window !== 'undefined') {
  window.addEventListener('pagehide', closeWarmCapture);
  document.addEventListener('visibilitychange', () => {
    if (document.visibilityState === 'hidden') closeWarmCapture();
  });
}

export function createContinuousCapture(options: ContinuousCaptureOptions): ContinuousCapture {
  let capture: Capture | null = null;
  let node: AudioWorkletNode | null = null;
  let generation = 0;
  let active = false;
  let requestedAt = 0;
  let startDelayMs = 0;
  let warm = false;
  let stopping: Promise<void> | null = null;
  let finish: (() => void) | undefined;
  let watchdog: ReturnType<typeof setTimeout> | undefined;
  let rejectReady: ((error: Error) => void) | undefined;
  let segmenter = new PCMSegmenter(options.onSegment);
  const listeners = new Set<(progress: CaptureProgress) => void>();
  const progress = () => {
    const p = { ...segmenter.progress, startDelayMs, warm };
    options.onProgress?.(p);
    listeners.forEach((fn) => fn(p));
  };
  const close = () => {
    clearTimeout(watchdog);
    node?.port.close();
    node?.disconnect();
    node = null;
    capture?.close();
    capture = null;
  };
  const cancel = () => {
    generation++;
    active = false;
    rejectReady?.(new Error('Recording cancelled'));
    rejectReady = undefined;
    close();
    finish?.();
    finish = undefined;
    stopping = null;
  };
  const interrupt = (message: string) => {
    if (!active) return;
    options.onInterrupted?.(message);
    // Unexpected suspension/track loss cannot deliver a tail. Flush precisely
    // the PCM already acknowledged on the main thread, never invent a gap.
    active = false;
    segmenter.flush();
    progress();
    close();
    finish?.();
    finish = undefined;
  };
  return {
    async start() {
      if (active) return;
      const token = ++generation;
      requestedAt = performance.now();
      startDelayMs = 0;
      warm = false;
      active = true;
      stopping = null;
      segmenter = new PCMSegmenter(options.onSegment);
      try {
        if (warmGraph) {
          if (
            captureLive(warmGraph.capture) &&
            warmGraph.capture.leadInMs === (options.leadInMs ?? 300)
          ) {
            warm = true;
            clearTimeout(warmGraph.timer);
            capture = warmGraph.capture;
            node = warmGraph.node;
            warmGraph = null;
          } else closeWarmCapture();
        }
        if (!captureLive(capture)) {
          close();
          const opened = await openCapture(options.leadInMs ?? 300, true);
          if (token !== generation) {
            opened.close();
            return;
          }
          capture = opened;
          const ctx = opened.context!;
          if (!ctx.audioWorklet || typeof AudioWorkletNode === 'undefined')
            throw new Error('Continuous recording unavailable in this browser');
          const url = URL.createObjectURL(
            new Blob([pcmWorkletSource], { type: 'text/javascript' }),
          );
          try {
            await ctx.audioWorklet.addModule(url);
          } finally {
            URL.revokeObjectURL(url);
          }
          if (token !== generation) return;
          node = new AudioWorkletNode(ctx, 'solu-pcm', { outputChannelCount: [1] });
          opened.delayed!.connect(node);
          node.connect(ctx.destination); // processor outputs silence
        }
        const ctx = capture!.context!;
        for (const track of capture!.mic.getTracks())
          track.onended = () => interrupt('Microphone disconnected');
        ctx.onstatechange = () => {
          if (ctx.state === 'suspended' && active) interrupt('Microphone paused');
        };
        await ctx.resume();
        if (token !== generation) return;
        await new Promise<void>((resolve, reject) => {
          rejectReady = reject;
          watchdog = setTimeout(() => reject(new Error('Microphone did not become ready')), 10000);
          node!.port.onmessage = ({ data }) => {
            if (token !== generation) return;
            if (data.type === 'started') {
              startDelayMs = Math.max(0, performance.now() - requestedAt);
              clearTimeout(watchdog);
              rejectReady = undefined;
              progress();
              resolve();
            }
            if (data.type === 'pcm') {
              if (active) {
                segmenter.push(data.pcm);
                progress();
              }
              node?.port.postMessage({ type: 'ack', id: data.id });
            }
            if (data.type === 'interrupted')
              options.onInterrupted?.(`Recording interrupted at frame ${data.frame}`);
            if (data.type === 'flushed') {
              clearTimeout(watchdog);
              active = false;
              segmenter.flush();
              progress();
              const keep = options.keepWarmMs ?? 0;
              if (
                keep > 0 &&
                !data.interrupted &&
                capture &&
                node &&
                document.visibilityState !== 'hidden'
              ) {
                closeWarmCapture();
                warmGraph = { capture, node, timer: setTimeout(closeWarmCapture, keep) };
                node.port.onmessage = null;
                capture.context!.onstatechange = null;
                for (const track of capture.mic.getTracks()) track.onended = closeWarmCapture;
                capture = null;
                node = null;
              } else close();
              finish?.();
              finish = undefined;
            }
          };
          // A reused graph already processed audio; the start acknowledgement
          // is emitted from its port after resume, with no session PCM stored warm.
          node!.port.postMessage({ type: 'start' });
        });
      } catch (error) {
        if (token === generation) {
          active = false;
          close();
        }
        throw error;
      }
    },
    stop() {
      if (stopping) return stopping;
      if (!active || !node) {
        cancel();
        return Promise.resolve();
      }
      stopping = new Promise<void>((resolve) => {
        finish = resolve;
      });
      node.port.postMessage({
        type: 'stop',
        tailFrames: Math.round((capture?.leadInMs ?? 0) * 16),
      });
      watchdog = setTimeout(
        () => interrupt('Microphone stopped responding; available audio preserved'),
        2500,
      );
      return stopping;
    },
    cancel,
    dispose: cancel,
    subscribe(fn) {
      listeners.add(fn);
      return () => {
        listeners.delete(fn);
      };
    },
  };
}
