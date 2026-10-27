// Persistent opt-out for the CLI setup banners (Claude/Muse install + sign-in
// nudges). Session dismissal lives in each banner; this is the durable "don't
// show again" layer. The Credentials CLI rows always show install status, so a
// hidden banner never strands the user without a reinstall path — and each row
// offers "Remind me" to clear the flag.

const KEYS = {
  claude: 'solu.hideClaudeSetupBanner',
  muse: 'solu.hideMuseSetupBanner',
} as const;

export type SetupBannerKind = keyof typeof KEYS;

function storage(): Storage | null {
  try {
    return window.localStorage;
  } catch {
    return null;
  }
}

export function setupBannerHidden(kind: SetupBannerKind): boolean {
  try {
    return storage()?.getItem(KEYS[kind]) === '1';
  } catch {
    return false;
  }
}

export function hideSetupBanner(kind: SetupBannerKind): void {
  try {
    storage()?.setItem(KEYS[kind], '1');
  } catch {
    // Private mode etc: the banner's session dismissal still applies.
  }
}

export function unhideSetupBanner(kind: SetupBannerKind): void {
  try {
    storage()?.removeItem(KEYS[kind]);
  } catch {
    // Nothing persisted — nothing to clear.
  }
}
