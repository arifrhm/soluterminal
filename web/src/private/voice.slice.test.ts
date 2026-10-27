import { beforeEach, describe, expect, it, vi } from 'vitest';
import { useStore } from '../store';

const originalSendToFocused = useStore.getState().sendToFocused;

beforeEach(() => {
  useStore.getState().registerVoiceControl(null);
  useStore.setState({
    voiceAutoSend: false,
    voiceDeferCleanup: true,
    voiceLanguage: 'en',
    voiceAlertMode: 'off',
    voiceHistory: [],
    voiceSession: null,
    voiceControl: null,
    sendToFocused: originalSendToFocused,
  });
  localStorage.clear();
});

describe('voice slice', () => {
  it('has the current voice defaults', () => {
    const state = useStore.getState();

    expect(state.voiceAutoSend).toBe(false);
    expect(state.voiceDeferCleanup).toBe(true);
    expect(state.voiceLanguage).toBe('en');
    expect(state.voiceHistory).toEqual([]);
    expect(state.voiceSession).toBeNull();
    expect(state.voiceControl).toBeNull();
  });

  it('updates and persists voice input settings without changing their encodings', () => {
    useStore.getState().setVoiceAutoSend(true);
    expect(useStore.getState().voiceAutoSend).toBe(true);
    expect(localStorage.getItem('solu.voiceAutoSend')).toBe('1');

    useStore.getState().setVoiceAutoSend(false);
    expect(useStore.getState().voiceAutoSend).toBe(false);
    expect(localStorage.getItem('solu.voiceAutoSend')).toBe('0');

    useStore.getState().setVoiceDeferCleanup(false);
    expect(useStore.getState().voiceDeferCleanup).toBe(false);
    expect(localStorage.getItem('solu.voiceDeferCleanup')).toBe('0');

    useStore.getState().setVoiceDeferCleanup(true);
    expect(useStore.getState().voiceDeferCleanup).toBe(true);
    expect(localStorage.getItem('solu.voiceDeferCleanup')).toBe('1');

    useStore.getState().setVoiceLanguage('zh');
    expect(useStore.getState().voiceLanguage).toBe('zh');
    expect(localStorage.getItem('solu.voiceLanguage')).toBe('zh');

    useStore.getState().setVoiceLanguage('');
    expect(useStore.getState().voiceLanguage).toBe('');
    expect(localStorage.getItem('solu.voiceLanguage')).toBe('');
  });

  it('updates and persists the alert mode through voice.ts', () => {
    useStore.getState().setVoiceAlertMode('sonia');

    expect(useStore.getState().voiceAlertMode).toBe('sonia');
    expect(localStorage.getItem('solu.voiceAlertMode')).toBe('sonia');
  });

  it('prepends history, keeps duplicates, and caps state and persistence at 20 entries', () => {
    useStore.getState().addVoiceHistoryEntry({ text: 'same', paneTitle: 'pane', delivered: true });
    useStore.getState().addVoiceHistoryEntry({ text: 'same', paneTitle: 'pane', delivered: true });

    const duplicateHistory = useStore.getState().voiceHistory;
    expect(duplicateHistory).toHaveLength(2);
    expect(duplicateHistory.map((entry) => entry.text)).toEqual(['same', 'same']);
    expect(typeof duplicateHistory[0].id).toBe('number');

    for (let i = 0; i < 21; i += 1) {
      useStore.getState().addVoiceHistoryEntry({
        text: `entry-${i}`,
        paneTitle: `pane-${i}`,
        delivered: i % 2 === 0,
      });
    }

    const history = useStore.getState().voiceHistory;
    const persisted = JSON.parse(localStorage.getItem('solu.voiceHistory') ?? 'null') as unknown[];
    expect(history).toHaveLength(20);
    expect(history[0]).toMatchObject({ text: 'entry-20', paneTitle: 'pane-20', delivered: true });
    expect(persisted).toHaveLength(20);
    expect(persisted).toEqual(history);
  });

  it('clears voice history from state and localStorage', () => {
    useStore.getState().addVoiceHistoryEntry({ text: 'recover me', paneTitle: 'pane', delivered: false });
    expect(localStorage.getItem('solu.voiceHistory')).not.toBeNull();

    useStore.getState().clearVoiceHistory();

    expect(useStore.getState().voiceHistory).toEqual([]);
    expect(localStorage.getItem('solu.voiceHistory')).toBeNull();
  });

  it('reinserts a known history entry through sendToFocused and rejects unknown ids', () => {
    const sendToFocused = vi.fn(() => true);
    useStore.setState({ sendToFocused });
    useStore.getState().addVoiceHistoryEntry({ text: 'send again', paneTitle: 'pane', delivered: true });
    const id = useStore.getState().voiceHistory[0].id;

    expect(useStore.getState().reinsertVoiceHistory(id)).toBe(true);
    expect(sendToFocused).toHaveBeenCalledOnce();
    expect(sendToFocused).toHaveBeenCalledWith('send again');

    sendToFocused.mockClear();
    expect(useStore.getState().reinsertVoiceHistory(999999)).toBe(false);
    expect(sendToFocused).not.toHaveBeenCalled();
  });

  it('propagates a failed send when reinserting a known history entry', () => {
    const sendToFocused = vi.fn(() => false);
    useStore.setState({ sendToFocused });
    useStore.getState().addVoiceHistoryEntry({ text: 'cannot send', paneTitle: 'pane', delivered: false });
    const id = useStore.getState().voiceHistory[0].id;

    expect(useStore.getState().reinsertVoiceHistory(id)).toBe(false);
    expect(sendToFocused).toHaveBeenCalledWith('cannot send');
  });

  it('updates history state when persistence throws', () => {
    const setItem = vi.spyOn(Storage.prototype, 'setItem').mockImplementation((key) => {
      if (key === 'solu.voiceHistory') throw new DOMException('quota exceeded', 'QuotaExceededError');
    });

    expect(() => useStore.getState().addVoiceHistoryEntry({
      text: 'still retained',
      paneTitle: 'pane',
      delivered: true,
    })).not.toThrow();
    expect(useStore.getState().voiceHistory[0]).toMatchObject({ text: 'still retained' });

    setItem.mockRestore();
  });

  it('keeps voice controls in a LIFO stack and reveals the previous control on unregister', () => {
    const a = { start: vi.fn(), stop: vi.fn() };
    const b = { start: vi.fn(), stop: vi.fn() };

    useStore.getState().registerVoiceControl(a);
    expect(useStore.getState().voiceControl).toBe(a);
    useStore.getState().registerVoiceControl(b);
    expect(useStore.getState().voiceControl).toBe(b);
    useStore.getState().unregisterVoiceControl(b);
    expect(useStore.getState().voiceControl).toBe(a);
  });

  it('moves a re-registered voice control to the top of the stack', () => {
    const a = { start: vi.fn(), stop: vi.fn() };
    const b = { start: vi.fn(), stop: vi.fn() };

    useStore.getState().registerVoiceControl(a);
    useStore.getState().registerVoiceControl(b);
    useStore.getState().registerVoiceControl(a);
    expect(useStore.getState().voiceControl).toBe(a);

    useStore.getState().unregisterVoiceControl(a);
    expect(useStore.getState().voiceControl).toBe(b);
  });

  it('clears the entire voice control stack when null is registered', () => {
    const a = { start: vi.fn(), stop: vi.fn() };
    const b = { start: vi.fn(), stop: vi.fn() };

    useStore.getState().registerVoiceControl(a);
    useStore.getState().registerVoiceControl(b);
    useStore.getState().registerVoiceControl(null);
    expect(useStore.getState().voiceControl).toBeNull();

    useStore.getState().unregisterVoiceControl(a);
    expect(useStore.getState().voiceControl).toBeNull();
  });

  it('leaves the current voice control unchanged when an unknown control is unregistered', () => {
    const active = { start: vi.fn(), stop: vi.fn() };
    const unknown = { start: vi.fn(), stop: vi.fn() };

    useStore.getState().registerVoiceControl(active);
    useStore.getState().unregisterVoiceControl(unknown);

    expect(useStore.getState().voiceControl).toBe(active);
  });

  it('round-trips voice session phase changes and clearing', () => {
    const recording = { paneId: 'p1', phase: 'recording' as const };
    const transcribing = { paneId: 'p1', phase: 'transcribing' as const };

    useStore.getState().setVoiceSession(recording);
    expect(useStore.getState().voiceSession).toEqual(recording);
    useStore.getState().setVoiceSession(transcribing);
    expect(useStore.getState().voiceSession).toEqual(transcribing);
    useStore.getState().setVoiceSession(null);
    expect(useStore.getState().voiceSession).toBeNull();
  });

  it('round-trips an ARIA voice session with cancellation armed', () => {
    const session = { paneId: null, phase: 'recording' as const, aboutToCancel: true };

    useStore.getState().setVoiceSession(session);

    expect(useStore.getState().voiceSession).toEqual(session);
  });
});

describe('voice slice hydration', () => {
  it('uses the persisted voice defaults when localStorage keys are absent', async () => {
    vi.resetModules();
    localStorage.clear();

    const { useStore: fresh } = await import('../store');
    const state = fresh.getState();

    expect(state.voiceAutoSend).toBe(false);
    expect(state.voiceDeferCleanup).toBe(true);
    expect(state.voiceLanguage).toBe('en');
    expect(state.voiceAlertMode).toBe('chime');
    expect(state.voiceHistory).toEqual([]);
  });

  it('hydrates compatible settings and filters and caps persisted history', async () => {
    vi.resetModules();
    localStorage.clear();
    localStorage.setItem('solu.voiceAutoSend', '1');
    localStorage.setItem('solu.voiceDeferCleanup', '0');
    localStorage.setItem('solu.voiceLanguage', '');
    localStorage.setItem('solu.voiceAlertMode', 'ryan');
    const entries = Array.from({ length: 22 }, (_, id) => ({
      id,
      text: `entry-${id}`,
      paneTitle: `pane-${id}`,
      delivered: id % 2 === 0,
    }));
    localStorage.setItem('solu.voiceHistory', JSON.stringify([...entries, { id: 'x' }, null]));

    const { useStore: fresh } = await import('../store');
    const state = fresh.getState();

    expect(state.voiceAutoSend).toBe(true);
    expect(state.voiceDeferCleanup).toBe(false);
    expect(state.voiceLanguage).toBe('');
    expect(state.voiceAlertMode).toBe('ryan');
    expect(state.voiceHistory).toEqual(entries.slice(0, 20));
  });

  it('falls back to voice defaults when localStorage reads throw', async () => {
    vi.resetModules();
    localStorage.clear();
    const originalGetItem = Storage.prototype.getItem;
    const getItem = vi.spyOn(Storage.prototype, 'getItem').mockImplementation(function (this: Storage, key) {
      if (key.startsWith('solu.voice')) throw new DOMException('storage unavailable', 'SecurityError');
      return originalGetItem.call(this, key);
    });

    const { useStore: fresh } = await import('../store');
    const state = fresh.getState();

    expect(state.voiceAutoSend).toBe(false);
    expect(state.voiceDeferCleanup).toBe(true);
    expect(state.voiceLanguage).toBe('en');
    expect(state.voiceAlertMode).toBe('chime');
    expect(state.voiceHistory).toEqual([]);

    getItem.mockRestore();
  });

  it('uses an empty history when persisted JSON is corrupt', async () => {
    vi.resetModules();
    localStorage.clear();
    localStorage.setItem('solu.voiceHistory', '{oops');

    const { useStore: fresh } = await import('../store');

    expect(fresh.getState().voiceHistory).toEqual([]);
  });
});

it('upserts one checkpoint per session and evicts oldest entries by UTF-8 bytes', () => {
  useStore.setState({ voiceHistory: [] });
  const store = useStore.getState();
  store.addVoiceHistoryEntry({ sessionId: 'same', text: 'one', raw: 'um one', paneTitle: 'test', delivered: false });
  store.addVoiceHistoryEntry({ sessionId: 'same', text: 'one\n\ntwo', raw: 'um one\n\num two', paneTitle: 'test', delivered: true, partial: true });
  expect(useStore.getState().voiceHistory).toHaveLength(1);
  expect(useStore.getState().voiceHistory[0]).toMatchObject({ text: 'one\n\ntwo', partial: true, delivered: true });
  for (let i=0;i<10;i++) store.addVoiceHistoryEntry({ sessionId: String(i), text: '界'.repeat(50000), paneTitle:'test', delivered:false });
  expect(new TextEncoder().encode(JSON.stringify(useStore.getState().voiceHistory)).length).toBeLessThanOrEqual(1024*1024);
  expect(useStore.getState().voiceHistory[0].sessionId).toBe('9');
});
