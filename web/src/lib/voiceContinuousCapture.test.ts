// @vitest-environment node
import { expect, it, vi } from 'vitest';
import { PCMSegmenter, encodeWav } from '../../vendor/soluvoice/src/continuousCapture';
import { PCMResampler, pcmWorkletSource } from '../../vendor/soluvoice/src/pcmWorklet';
import type { CaptureSegment } from 'soluvoice';
import vm from 'node:vm';

const decode = async (blob: Blob) => {
  const buffer = await blob.arrayBuffer();
  const view = new DataView(buffer);
  expect(new TextDecoder().decode(buffer.slice(0, 4))).toBe('RIFF');
  expect(new TextDecoder().decode(buffer.slice(8, 12))).toBe('WAVE');
  expect(view.getUint32(4, true) + 8).toBe(buffer.byteLength);
  expect(view.getUint32(24, true)).toBe(16000);
  expect(view.getUint16(22, true)).toBe(1);
  expect(view.getUint32(40, true)).toBe(buffer.byteLength - 44);
  return new Int16Array(buffer.slice(44));
};
it('writes an independent 60-second WAV below the multipart budget', async () => {
  const samples = new Int16Array(960000).fill(1234);
  const blob = encodeWav(samples);
  expect(blob.size).toBe(1920044);
  expect(Buffer.from((await decode(blob)).buffer).equals(Buffer.from(samples.buffer))).toBe(true);
});
it('conserves exact samples across nonaligned forced rollovers without deduplication', async () => {
  const parts: CaptureSegment[] = [];
  const segmenter = new PCMSegmenter((p) => parts.push(p));
  const samples = Int16Array.from({ length: 16000 * 181 + 37 }, (_, i) => 1000 + (i % 30000));
  for (let i = 0; i < samples.length; i += 1023) segmenter.push(samples.slice(i, i + 1023));
  segmenter.flush();
  expect(parts.map((p) => p.frames)).toEqual([960000, 960000, 960000, 16037]);
  let offset = 0;
  for (const part of parts) {
    const pcm = await decode(part.blob);
    const expected = samples.subarray(offset, offset + pcm.length);
    expect(Buffer.from(pcm.buffer, pcm.byteOffset, pcm.byteLength).equals(Buffer.from(expected.buffer, expected.byteOffset, expected.byteLength))).toBe(true);
    offset += pcm.length;
  }
  expect(offset).toBe(samples.length);
});
it('prefers the first quiet boundary after 55 seconds', () => {
  const parts: CaptureSegment[] = [];
  const segmenter = new PCMSegmenter((p) => parts.push(p));
  segmenter.push(new Int16Array(880000).fill(9000));
  segmenter.push(new Int16Array(3201));
  expect(parts[0].frames).toBe(883200);
  segmenter.flush();
  expect(parts[1].frames).toBe(1);
});
it.each([44100, 48000])(
  'resamples %s Hz continuously and suppresses aliased high-frequency input',
  (rate) => {
    const resampler = new PCMResampler(rate);
    const out: number[] = [];
    for (let i = 0; i < rate * 2; i++) {
      const v = resampler.push(Math.sin((2 * Math.PI * 1000 * i) / rate) * 0.5);
      if (v !== undefined) out.push(v);
    }
    expect(out.length).toBe(32000);
    const rms = Math.sqrt(out.slice(1000).reduce((n, x) => n + x * x, 0) / 31000);
    expect(rms).toBeGreaterThan(11000);
    expect(rms).toBeLessThan(12000);
    const filter = new PCMResampler(rate);
    const high: number[] = [];
    for (let i = 0; i < rate; i++) {
      const v = filter.push(Math.sin((2 * Math.PI * 12000 * i) / rate));
      if (v !== undefined) high.push(v);
    }
    expect(Math.max(...high.slice(1000).map(Math.abs))).toBeLessThan(1000);
  },
);
function processor() {
  let Ctor: new () => {
    port: { onmessage: (message: { data: unknown }) => void };
    process: (inputs: Float32Array[][]) => boolean;
  };
  const messages: { type: string; id?: number; pcm?: Int16Array; frame?: number }[] = [];
  const sandbox = {
    sampleRate: 48000,
    AudioWorkletProcessor: class {
      port = {
        postMessage: (message: (typeof messages)[number]) => messages.push(message),
        onmessage: vi.fn(),
      };
    },
    registerProcessor: (_name: string, ctor: typeof Ctor) => {
      Ctor = ctor;
    },
  };
  vm.runInNewContext(pcmWorkletSource, sandbox);
  return { node: new Ctor!(), messages };
}
it('downmixes stereo, stops at an acknowledged final frame and bounds stalled-consumer credit', () => {
  const { node, messages } = processor();
  node.port.onmessage({ data: { type: 'start' } });
  const left = new Float32Array(128).fill(0.5),
    right = new Float32Array(128).fill(-0.5);
  for (let i = 0; i < 10; i++) node.process([[left, right]]);
  expect(messages.filter((m) => m.pcm).every((m) => Array.from(m.pcm!).every((x) => x === 0))).toBe(
    true,
  );
  node.port.onmessage({ data: { type: 'stop', tailFrames: 4800 } });
  for (let i = 0; i < 120; i++) node.process([[left, right]]);
  const flushed = messages.find((m) => m.type === 'flushed');
  expect(flushed?.frame).toBe(5226);
  const stalled = processor();
  stalled.node.port.onmessage({ data: { type: 'start' } });
  for (let i = 0; i < 1000; i++) stalled.node.process([[left]]);
  expect(
    stalled.messages.filter((m) => m.pcm).reduce((n, m) => n + m.pcm!.byteLength, 0),
  ).toBeLessThanOrEqual(32768);
  expect(stalled.messages.filter((m) => m.type === 'interrupted')).toHaveLength(1);
  expect(stalled.messages.find((m) => m.type === 'interrupted')?.frame).toBe(32384);
});
