import { describe, expect, it, vi, beforeEach } from 'vitest';
import { makeReadyCue } from './voiceReadyCue';
import { readVoiceKeepWarmMs } from '../private/voice.slice';

describe('ready cue', () => {
  it('ticks once when recording really starts, not on later session updates', () => {
    const play = vi.fn();
    const cue = makeReadyCue(() => true, play);
    cue('recording');
    cue('recording'); // slide-to-cancel arm/disarm re-publishes the phase
    cue('transcribing');
    cue('idle');
    cue('recording');
    expect(play).toHaveBeenCalledTimes(2);
  });

  it('stays quiet when the setting is off', () => {
    const play = vi.fn();
    const cue = makeReadyCue(() => false, play);
    cue('recording');
    expect(play).not.toHaveBeenCalled();
  });
});

describe('ready window setting', () => {
  beforeEach(() => localStorage.clear());

  it('is off unless the user turned it on', () => {
    expect(readVoiceKeepWarmMs()).toBe(0);
  });

  it('keeps an earlier "off" choice off', () => {
    localStorage.setItem('solu.voiceKeepMicWarm', '0');
    expect(readVoiceKeepWarmMs()).toBe(0);
  });

  it('keeps an earlier explicit "on" choice at 2 minutes', () => {
    localStorage.setItem('solu.voiceKeepMicWarm', '1');
    expect(readVoiceKeepWarmMs()).toBe(120_000);
  });

  it('reads a saved choice, keeps an earlier 30 s, and treats nonsense as off', () => {
    localStorage.setItem('solu.voiceKeepWarmMs', '120000');
    expect(readVoiceKeepWarmMs()).toBe(120_000);
    localStorage.setItem('solu.voiceKeepWarmMs', '30000');
    expect(readVoiceKeepWarmMs()).toBe(30_000);
    localStorage.setItem('solu.voiceKeepWarmMs', '999');
    expect(readVoiceKeepWarmMs()).toBe(0);
  });
});
