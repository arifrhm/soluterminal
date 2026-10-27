import { isEmbedded } from './embed';

// The only embed-specific chrome policy. Add or remove entries here when the
// host changes which parts of SoluTerminal it owns.
export type EmbedChrome =
  | 'appIcon' | 'install' | 'desktopInstaller' | 'shellUpdate'
  | 'phonePairing' | 'welcome' | 'whatsNew' | 'hostingSwitch';

const hiddenInEmbed = new Set<EmbedChrome>([
  'appIcon', 'install', 'desktopInstaller', 'shellUpdate',
  'phonePairing', 'welcome', 'whatsNew', 'hostingSwitch',
]);

export function showEmbedChrome(surface: EmbedChrome): boolean {
  return !isEmbedded() || !hiddenInEmbed.has(surface);
}
