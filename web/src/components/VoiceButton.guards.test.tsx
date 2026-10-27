import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { api } from '../api';
import { useStore } from '../store';
import { notePaneTyped, resetPaneTypedForTests } from '../lib/voiceGuards';
import { terminalRawThenPatch } from './VoiceButton';

// The polish must never rewrite what the user already typed or sent, and must
// still find the raw when a narrow pane wraps it mid-word.

const PANE = 'pane-guard';
const RAW = 'please rename the use state hook to use session state in app';
const CLEAN = 'Please rename the useState hook to useSessionState in app.';

describe('terminalRawThenPatch guards', () => {
  let inject: ReturnType<typeof vi.spyOn>;

  beforeEach(() => {
    resetPaneTypedForTests();
    inject = vi.spyOn(api, 'injectPane').mockResolvedValue(undefined as never);
    useStore.setState({ paneSnapshots: { [PANE]: () => `~/app $ ${RAW}` } });
  });
  afterEach(() => {
    vi.restoreAllMocks();
    useStore.setState({ paneSnapshots: {} });
  });

  it('keeps the raw when the user typed (e.g. pressed Enter) before the polish landed', async () => {
    vi.spyOn(api, 'cleanupVoice').mockImplementation(async () => {
      // The user submits the raw while cleanup is in flight.
      notePaneTyped(PANE);
      return { text: CLEAN, cleanup: 'ok' } as never;
    });
    await terminalRawThenPatch(RAW, PANE, undefined);
    expect(inject).toHaveBeenCalledTimes(1);
    expect(inject.mock.calls[0]).toEqual([PANE, RAW]);
    expect(useStore.getState().voiceHistory[0]?.text).toBe(RAW);
  });

  it('typing in another pane does not block the polish', async () => {
    vi.spyOn(api, 'cleanupVoice').mockImplementation(async () => {
      notePaneTyped('some-other-pane');
      return { text: CLEAN, cleanup: 'ok' } as never;
    });
    await terminalRawThenPatch(RAW, PANE, undefined);
    expect(inject).toHaveBeenCalledTimes(2);
  });

  it('still patches a raw that the pane wrapped mid-word', async () => {
    vi.spyOn(api, 'cleanupVoice').mockResolvedValue({ text: CLEAN, cleanup: 'ok' } as never);
    const cut = 30;
    useStore.setState({ paneSnapshots: { [PANE]: () => `~/app $ ${RAW.slice(0, cut)}\n${RAW.slice(cut)}` } });
    await terminalRawThenPatch(RAW, PANE, undefined);
    expect(inject).toHaveBeenCalledTimes(2);
    expect(inject.mock.calls[1]?.[1]).toBe('\x7f'.repeat([...RAW].length) + CLEAN);
  });

  it('sends at most 4 KB of terminal context for cleanup', async () => {
    const cleanup = vi.spyOn(api, 'cleanupVoice').mockResolvedValue({ text: CLEAN, cleanup: 'ok' } as never);
    const long = 'x'.repeat(20_000) + `\n~/app $ ${RAW}`;
    useStore.setState({ paneSnapshots: { [PANE]: () => long } });
    await terminalRawThenPatch(RAW, PANE, undefined);
    const ctx = (cleanup.mock.calls[0]?.[1] as { context?: string }).context ?? '';
    expect(ctx.length).toBeLessThanOrEqual(4000);
    expect(ctx.endsWith(RAW)).toBe(true);
  });
});
