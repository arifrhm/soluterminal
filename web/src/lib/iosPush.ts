// iOS delivers web push only to a site added to the Home Screen (iOS 16.4+),
// in every iOS browser since they all run WebKit. A Safari tab cannot
// subscribe, so the bell explains how to get push instead of failing quietly.

export function isIosDevice(): boolean {
  if (typeof navigator === 'undefined') return false;
  const ua = navigator.userAgent;
  // iPadOS reports a Mac platform; touch points tell it apart.
  return /iPad|iPhone|iPod/.test(ua) || (navigator.platform === 'MacIntel' && navigator.maxTouchPoints > 1);
}

export function isStandaloneDisplay(): boolean {
  if (typeof window === 'undefined') return false;
  try {
    if (window.matchMedia('(display-mode: standalone)').matches) return true;
  } catch { /* older browser */ }
  // iOS Safari reports Home Screen launches through a non-standard flag.
  return !!(window.navigator as unknown as { standalone?: boolean }).standalone;
}

export function needsHomeScreenForPush(): boolean {
  return isIosDevice() && !isStandaloneDisplay();
}

export const IOS_PUSH_HINT = 'On iPhone, add SoluTerminal to your Home Screen to get push alerts.';
