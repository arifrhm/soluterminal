import { isTauri } from '../voice';

// soluvoice words a refused microphone for a browser tab ("Allow in site
// settings"). The desktop app has no site settings: there, Windows decides
// whether desktop apps may use the microphone.

const BROWSER_MIC_BLOCKED = /^Mic blocked\. Allow in site settings/;

export const DESKTOP_MIC_BLOCKED =
  'Microphone blocked. In Windows Settings › Privacy & security › Microphone, turn on ' +
  'Microphone access and "Let desktop apps access your microphone" so SoluTerminal can use it, then try again.';

/** Rewrites the recorder's mic-refused message for the desktop app. */
export function voiceErrorForHost(message: string, desktop: boolean = isTauri()): string {
  if (desktop && BROWSER_MIC_BLOCKED.test(message)) return DESKTOP_MIC_BLOCKED;
  return message;
}
