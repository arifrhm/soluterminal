import { act, renderHook } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { useVoice } from 'soluvoice';

// soluvoice 0.1.5: a warm mic between dictations and a lead-in delay line, so
// the first word of a dictation isn't clipped. jsdom has no MediaRecorder,
// getUserMedia or Web Audio, so all three are faked here.

class FakeTrack {
  readyState: 'live' | 'ended' = 'live';
  stop = vi.fn(() => { this.readyState = 'ended'; });
}

class FakeStream {
  tracks = [new FakeTrack()];
  getTracks() { return this.tracks; }
  getAudioTracks() { return this.tracks; }
}

class FakeRecorder {
  static last: FakeRecorder | null = null;
  state: 'inactive' | 'recording' = 'inactive';
  ondataavailable: ((e: { data: Blob }) => void) | null = null;
  onstop: (() => void) | null = null;
  stopCalls = 0;
  constructor(public stream: unknown) { FakeRecorder.last = this; }
  start() { this.state = 'recording'; }
  stop() {
    this.stopCalls++;
    this.state = 'inactive';
    this.ondataavailable?.({ data: new Blob(['x']) });
    this.onstop?.();
  }
  static isTypeSupported() { return true; }
}

let getUserMedia: ReturnType<typeof vi.fn>;

beforeEach(() => {
  vi.useFakeTimers();
  getUserMedia = vi.fn(async () => new FakeStream());
  Object.defineProperty(navigator, 'mediaDevices', { value: { getUserMedia }, configurable: true });
  vi.stubGlobal('MediaRecorder', FakeRecorder);
});

afterEach(() => {
  vi.useRealTimers();
  vi.unstubAllGlobals();
  delete (window as unknown as { AudioContext?: unknown }).AudioContext;
});

function setup(opts: { keepWarmMs?: number; leadInMs?: number }) {
  const transcribe = vi.fn(async () => ({ text: 'run the tests', cleanup: 'ok' }));
  const onText = vi.fn();
  const hook = renderHook(() => useVoice({ transcribe, onText, minRecordMs: 0, ...opts }));
  return { hook, transcribe, onText };
}

async function dictate(hook: ReturnType<typeof setup>['hook'], holdMs = 500) {
  await act(async () => { hook.result.current.start(); });
  await act(async () => { await vi.advanceTimersByTimeAsync(holdMs); });
  await act(async () => { hook.result.current.stop(); });
  await act(async () => { await vi.advanceTimersByTimeAsync(400); });
}

describe('soluvoice warm mic', () => {
  it('reuses the open mic for the next dictation within the warm window', async () => {
    const { hook, onText } = setup({ keepWarmMs: 30_000 });
    await dictate(hook);
    await dictate(hook);
    expect(getUserMedia).toHaveBeenCalledTimes(1);
    expect(onText).toHaveBeenCalledTimes(2);
    expect(onText.mock.calls[0][1]).toMatchObject({ warm: false });
    expect(onText.mock.calls[1][1]).toMatchObject({ warm: true });
  });

  it('closes the mic once the warm window passes', async () => {
    const { hook } = setup({ keepWarmMs: 30_000 });
    await dictate(hook);
    const track = (await getUserMedia.mock.results[0].value as FakeStream).tracks[0];
    expect(track.stop).not.toHaveBeenCalled();
    await act(async () => { await vi.advanceTimersByTimeAsync(30_000); });
    expect(track.stop).toHaveBeenCalled();
    await dictate(hook);
    expect(getUserMedia).toHaveBeenCalledTimes(2);
  });

  it('with warm off (the default) opens the mic on press and closes it when the clip ends', async () => {
    const { hook, onText } = setup({ keepWarmMs: 0 });
    expect(getUserMedia).not.toHaveBeenCalled();
    await dictate(hook);
    const track = (await getUserMedia.mock.results[0].value as FakeStream).tracks[0];
    expect(track.stop).toHaveBeenCalled();
    // The next press opens a fresh mic: nothing from between dictations.
    await dictate(hook);
    expect(getUserMedia).toHaveBeenCalledTimes(2);
    expect(onText.mock.calls.map((c) => c[1].warm)).toEqual([false, false]);
  });

  it('closes a warm mic as soon as the page is hidden', async () => {
    const { hook } = setup({ keepWarmMs: 30_000 });
    await dictate(hook);
    const track = (await getUserMedia.mock.results[0].value as FakeStream).tracks[0];
    Object.defineProperty(document, 'visibilityState', { value: 'hidden', configurable: true });
    document.dispatchEvent(new Event('visibilitychange'));
    expect(track.stop).toHaveBeenCalled();
    Object.defineProperty(document, 'visibilityState', { value: 'visible', configurable: true });
  });

  it('closes the mic on unmount', async () => {
    const { hook } = setup({ keepWarmMs: 30_000 });
    await dictate(hook);
    const track = (await getUserMedia.mock.results[0].value as FakeStream).tracks[0];
    hook.unmount();
    expect(track.stop).toHaveBeenCalled();
  });
});

describe('soluvoice lead-in', () => {
  function fakeAudioContext() {
    const destStream = new FakeStream();
    class Ctx {
      state = 'running';
      createMediaStreamSource() { return { connect: vi.fn(), disconnect: vi.fn() }; }
      createDelay() { return { delayTime: { value: 0 }, connect: vi.fn() }; }
      createMediaStreamDestination() { return { stream: destStream, channelCount: 2 }; }
      resume() { return Promise.resolve(); }
      close = vi.fn(() => Promise.resolve());
    }
    (window as unknown as { AudioContext: unknown }).AudioContext = Ctx;
    return destStream;
  }

  it('records the delayed copy and waits the lead-in before stopping', async () => {
    const dest = fakeAudioContext();
    const { hook, onText } = setup({ leadInMs: 300 });
    await act(async () => { hook.result.current.start(); });
    expect(FakeRecorder.last?.stream).toBe(dest);
    await act(async () => { await vi.advanceTimersByTimeAsync(500); });
    await act(async () => { hook.result.current.stop(); });
    expect(FakeRecorder.last?.stopCalls).toBe(0);
    await act(async () => { await vi.advanceTimersByTimeAsync(299); });
    expect(FakeRecorder.last?.stopCalls).toBe(0);
    await act(async () => { await vi.advanceTimersByTimeAsync(1); });
    expect(FakeRecorder.last?.stopCalls).toBe(1);
    await act(async () => { await vi.advanceTimersByTimeAsync(10); });
    expect(onText).toHaveBeenCalledTimes(1);
  });

  it('a cancel stops at once and sends nothing', async () => {
    fakeAudioContext();
    const { hook, transcribe } = setup({ leadInMs: 300 });
    await act(async () => { hook.result.current.start(); });
    await act(async () => { await vi.advanceTimersByTimeAsync(500); });
    await act(async () => { hook.result.current.stop(true); });
    expect(FakeRecorder.last?.stopCalls).toBe(1);
    expect(transcribe).not.toHaveBeenCalled();
  });

  it('with warm off, the lead-in comes from a mic opened after the press', async () => {
    fakeAudioContext();
    const { hook } = setup({ leadInMs: 300, keepWarmMs: 0 });
    expect(getUserMedia).not.toHaveBeenCalled();
    await dictate(hook);
    const first = (await getUserMedia.mock.results[0].value as FakeStream).tracks[0];
    expect(first.stop).toHaveBeenCalled();
    await act(async () => { hook.result.current.start(); });
    expect(getUserMedia).toHaveBeenCalledTimes(2);
  });

  it('records the mic directly when Web Audio is missing', async () => {
    const { hook } = setup({ leadInMs: 300 });
    await act(async () => { hook.result.current.start(); });
    const mic = await getUserMedia.mock.results[0].value;
    expect(FakeRecorder.last?.stream).toBe(mic);
    await act(async () => { hook.result.current.stop(); });
    expect(FakeRecorder.last?.stopCalls).toBe(1);
  });
});
