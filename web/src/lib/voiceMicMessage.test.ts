import { expect, it } from 'vitest';
import { DESKTOP_MIC_BLOCKED, voiceErrorForHost } from './voiceMicMessage';

const BROWSER = 'Mic blocked. Allow in site settings, then try again.';

it('points the desktop app at Windows microphone settings', () => {
  expect(voiceErrorForHost(BROWSER, true)).toBe(DESKTOP_MIC_BLOCKED);
  expect(DESKTOP_MIC_BLOCKED).toContain('Windows Settings › Privacy & security › Microphone');
  expect(DESKTOP_MIC_BLOCKED).toContain('Let desktop apps access your microphone');
});

it('keeps the browser wording in a browser', () => {
  expect(voiceErrorForHost(BROWSER, false)).toBe(BROWSER);
});

it('leaves other messages alone on the desktop', () => {
  expect(voiceErrorForHost('Mic error: device busy', true)).toBe('Mic error: device busy');
});
