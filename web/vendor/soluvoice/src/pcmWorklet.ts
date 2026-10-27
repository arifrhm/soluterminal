/** Stateful low-pass resampler. Its state spans render blocks and WAV boundaries. */
export class PCMResampler {
  private history = new Float64Array(63);
  private taps = new Float64Array(63);
  private cursor = 0;
  private phase = 0;
  constructor(private inputRate: number) {
    const cutoff = Math.min(0.45, 7200 / inputRate);
    let sum = 0;
    for (let i = 0; i < 63; i++) {
      const x = i - 31;
      const sinc = x === 0 ? 2 * cutoff : Math.sin(2 * Math.PI * cutoff * x) / (Math.PI * x);
      this.taps[i] = sinc * (0.54 - 0.46 * Math.cos((2 * Math.PI * i) / 62));
      sum += this.taps[i];
    }
    for (let i = 0; i < 63; i++) this.taps[i] /= sum;
  }
  push(value: number): number | undefined {
    this.history[this.cursor] = value;
    this.cursor = (this.cursor + 1) % 63;
    this.phase += 16000;
    if (this.phase < this.inputRate) return;
    this.phase -= this.inputRate;
    let sample = 0;
    for (let i = 0; i < 63; i++) sample += this.taps[i] * this.history[(this.cursor + i) % 63];
    return Math.round(Math.max(-1, Math.min(1, sample)) * 32767);
  }
}

// Kept executable in tests: the generated module uses these exact classes.
declare const sampleRate: number;
declare class AudioWorkletProcessor {
  port: MessagePort;
}
declare function registerProcessor(name: string, ctor: unknown): void;
const WorkletBase =
  typeof AudioWorkletProcessor === 'undefined'
    ? class {
        port!: MessagePort;
      }
    : AudioWorkletProcessor;
export class PCMProcessor extends WorkletBase {
  private resampler = new PCMResampler(sampleRate);
  private ring = new Int16Array(16000);
  private read = 0;
  private count = 0;
  private frame = 0;
  private sent = 0;
  private credits = new Map<number, number>();
  private creditBytes = 0;
  private active = false;
  private ready = false;
  private starting = false;
  private finalFrame: number | null = null;
  private interrupted = false;
  constructor() {
    super();
    this.port.onmessage = ({ data }) => {
      if (data.type === 'ack') {
        this.creditBytes -= this.credits.get(data.id) ?? 0;
        this.credits.delete(data.id);
        this.drain();
      } else if (data.type === 'start') {
        this.active = true;
        this.frame = 0;
        this.sent = 0;
        this.count = 0;
        this.read = 0;
        this.finalFrame = null;
        this.interrupted = false;
        this.starting = true;
      } else if (data.type === 'stop' && this.active && this.finalFrame === null) {
        this.finalFrame = this.frame + data.tailFrames;
      } else if (data.type === 'cancel') {
        this.active = false;
        this.count = 0;
      }
    };
  }
  private drain() {
    while ((this.count >= 1024 || (!this.active && this.count > 0)) && this.creditBytes < 32768) {
      const n = Math.min(this.count, 1024, (32768 - this.creditBytes) / 2);
      const pcm = new Int16Array(n);
      for (let i = 0; i < n; i++) pcm[i] = this.ring[(this.read + i) % 16000];
      this.read = (this.read + n) % 16000;
      this.count -= n;
      const id = this.sent;
      this.sent += n;
      this.credits.set(id, n * 2);
      this.creditBytes += n * 2;
      this.port.postMessage({ type: 'pcm', id, pcm, endFrame: this.sent }, [pcm.buffer]);
    }
    if (!this.active && this.count === 0 && this.finalFrame !== null) {
      this.port.postMessage({ type: 'flushed', frame: this.frame, interrupted: this.interrupted });
      this.finalFrame = null;
    }
  }
  process(inputs: Float32Array[][]): boolean {
    const channels = inputs[0];
    if (!channels?.[0]?.length) return true;
    if (this.starting) {
      this.starting = false;
      this.port.postMessage({ type: 'started' });
    }
    if (!this.ready) {
      this.ready = true;
      this.port.postMessage({ type: 'ready' });
    }
    for (let i = 0; i < channels[0].length; i++) {
      let mono = 0;
      for (const ch of channels) mono += ch[i] ?? 0;
      const sample = this.resampler.push(mono / channels.length);
      if (sample === undefined || !this.active) continue;
      if (this.finalFrame !== null && this.frame >= this.finalFrame) {
        this.active = false;
        break;
      }
      if (this.count === 16000) {
        this.active = false;
        this.interrupted = true;
        this.finalFrame = this.frame;
        this.port.postMessage({ type: 'interrupted', frame: this.frame });
        break;
      }
      this.ring[(this.read + this.count) % 16000] = sample;
      this.count++;
      this.frame++;
    }
    this.drain();
    return true;
  }
}
export const pcmWorkletSource = `const WorkletBase = AudioWorkletProcessor;\n${PCMResampler.toString()}\n${PCMProcessor.toString()}\nregisterProcessor('solu-pcm', PCMProcessor);`;
