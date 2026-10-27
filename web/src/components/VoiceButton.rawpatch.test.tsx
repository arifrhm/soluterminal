import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { api, currentRoutingScope } from '../api';
import { useStore, terminalSessionKey } from '../store';
import { terminalRawThenPatch } from './VoiceButton';

// Raw-then-patch on the Alt+S → terminal path: inject raw immediately, fetch the
// cleanup (capped), then erase + re-inject the cleaned text — but only while the
// input line is still the untouched raw. These tests drive the module-level
// helper directly (the full VoiceButton needs MediaRecorder, absent in jsdom).

const PANE = 'pane-1';
const DEL = '\x7f';
// Long enough to clear VOICE_POLISH_MIN_CHARS (25).
const RAW = 'um so can you use state to store the counter value';
const CLEAN = 'So can you use useState to store the counter value?';

function setSnapshot(text: string | undefined) {
  useStore.setState({
    paneSnapshots: text === undefined ? {} : { [PANE]: () => text },
  });
}

describe('terminalRawThenPatch', () => {
  let inject: ReturnType<typeof vi.spyOn>;
  let cleanup: ReturnType<typeof vi.spyOn>;

  beforeEach(() => {
    inject = vi.spyOn(api, 'injectPane').mockResolvedValue(undefined as never);
    cleanup = vi.spyOn(api, 'cleanupVoice');
    setSnapshot(`~/proj $ ${RAW}`); // line shows the untouched raw by default
  });
  afterEach(() => {
    vi.restoreAllMocks();
    useStore.setState({ paneSnapshots: {} });
  });

  it('injects raw first, then erases and re-injects the cleaned text', async () => {
    cleanup.mockResolvedValue({ text: CLEAN, cleanup: 'ok' } as never);
    await terminalRawThenPatch(RAW, PANE, { title: 't' });

    expect(inject).toHaveBeenCalledTimes(2);
    // 1) raw shown immediately
    expect(inject.mock.calls[0]).toEqual([PANE, RAW]);
    // 2) exactly one DEL per rune of raw, then the cleaned text — in one write
    expect(inject.mock.calls[1]).toEqual([PANE, DEL.repeat([...RAW].length) + CLEAN]);
    expect(cleanup).toHaveBeenCalledWith(RAW, expect.objectContaining({ context: `~/proj $ ${RAW}` }));
  });

  it('patches through TUI chrome rendered below the input (Claude Code case)', async () => {
    cleanup.mockResolvedValue({ text: CLEAN, cleanup: 'ok' } as never);
    // Bordered input + a hint line after it — the raw is NOT the literal tail of
    // the buffer, which is exactly what broke the first cut (endsWith guard).
    setSnapshot(`╭──────────────╮\n│ > ${RAW}\n╰──────────────╯\n  ? for shortcuts`);
    await terminalRawThenPatch(RAW, PANE, undefined);

    expect(inject).toHaveBeenCalledTimes(2);
    expect(inject.mock.calls[1]).toEqual([PANE, DEL.repeat([...RAW].length) + CLEAN]);
  });

  it('does NOT patch once the raw has left the input region (submitted/cleared)', async () => {
    cleanup.mockResolvedValue({ text: CLEAN, cleanup: 'ok' } as never);
    // The raw scrolled away; the visible region is other output + a fresh prompt.
    setSnapshot('some command output line\nanother output line\n~/proj $ ');
    await terminalRawThenPatch(RAW, PANE, undefined);

    expect(cleanup).toHaveBeenCalledTimes(1); // it still fetched…
    expect(inject).toHaveBeenCalledTimes(1); // …but only the raw was ever injected
    expect(inject.mock.calls[0]).toEqual([PANE, RAW]);
  });

  it('keeps the raw and never calls cleanup when the text is too short', async () => {
    await terminalRawThenPatch('too short', PANE, undefined);
    expect(cleanup).not.toHaveBeenCalled();
    expect(inject).toHaveBeenCalledTimes(1);
    expect(inject.mock.calls[0]).toEqual([PANE, 'too short']);
  });

  it('never patches text containing a newline (a stray \\n would submit)', async () => {
    const multiline = `first long enough line here\nsecond line also present`;
    await terminalRawThenPatch(multiline, PANE, undefined);
    expect(cleanup).not.toHaveBeenCalled();
    expect(inject).toHaveBeenCalledTimes(1);
    expect(inject.mock.calls[0]).toEqual([PANE, multiline]);
  });

  it('keeps the raw when cleanup errors (unpaired / failure)', async () => {
    cleanup.mockRejectedValue(new Error('503'));
    await terminalRawThenPatch(RAW, PANE, undefined);
    expect(inject).toHaveBeenCalledTimes(1); // raw only, no patch
    expect(inject.mock.calls[0]).toEqual([PANE, RAW]);
  });

  describe('voice history says what happened', () => {
    const last = () => useStore.getState().voiceHistory[0];
    beforeEach(() => useStore.getState().clearVoiceHistory());

    it('records a cleaned dictation with who cleaned it, what was heard and the mic start', async () => {
      cleanup.mockResolvedValue({ text: CLEAN, cleanup: 'ok', by: 'local' } as never);
      await terminalRawThenPatch(RAW, PANE, { title: 't' }, undefined, { startDelayMs: 420, warmMic: false });
      expect(last()).toMatchObject({
        text: CLEAN, raw: RAW, polish: 'cleaned', cleanedBy: 'local', startDelayMs: 420, warmMic: false,
      });
    });

    it('labels a dictation cleanup left alone, instead of looking uncleaned', async () => {
      cleanup.mockResolvedValue({ text: RAW, cleanup: 'ok', by: 'local' } as never);
      await terminalRawThenPatch(RAW, PANE, undefined);
      expect(last()).toMatchObject({ text: RAW, polish: 'unchanged', cleanedBy: 'local' });
      expect(last().raw).toBeUndefined();
    });

    it('labels a short dictation', async () => {
      await terminalRawThenPatch('too short', PANE, undefined, undefined, { startDelayMs: 0, warmMic: true });
      expect(last()).toMatchObject({ text: 'too short', polish: 'short', warmMic: true });
    });

    it('labels a polish dropped because the raw left the input', async () => {
      cleanup.mockResolvedValue({ text: CLEAN, cleanup: 'ok', by: 'cloud' } as never);
      setSnapshot('some command output line\n~/proj $ ');
      await terminalRawThenPatch(RAW, PANE, undefined);
      expect(last()).toMatchObject({ text: RAW, polish: 'kept-raw', cleanedBy: 'cloud' });
    });

    it('labels a failed cleanup', async () => {
      cleanup.mockRejectedValue(new Error('503'));
      await terminalRawThenPatch(RAW, PANE, undefined);
      expect(last()).toMatchObject({ text: RAW, polish: 'failed' });
    });
  });

  it('uses only the captured terminal snapshot and cancels a delayed polish', async () => {
    const scope = currentRoutingScope();
    let valid = true;
    let resolve!: (result: { text: string; cleanup: string }) => void;
    cleanup.mockImplementation(() => new Promise(r => { resolve = r; }));
    useStore.setState({ paneSnapshots: {
      [PANE]: () => 'Wrong backend alias',
      [terminalSessionKey(scope, PANE)]: () => `~/proj $ ${RAW}`,
    } });
    const pending = terminalRawThenPatch(RAW, PANE, undefined, { scope, valid: () => valid });
    await vi.waitFor(() => expect(cleanup).toHaveBeenCalledTimes(1));
    expect(cleanup).toHaveBeenCalledWith(RAW, expect.objectContaining({ context: `~/proj $ ${RAW}` }));
    valid = false;
    resolve({ text: CLEAN, cleanup: 'ok' });
    await pending;
    expect(inject).toHaveBeenCalledTimes(1);
    expect(inject).toHaveBeenCalledWith(PANE, RAW, scope);
  });
});
