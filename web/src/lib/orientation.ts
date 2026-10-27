type OrientationLock = 'portrait-primary' | 'portrait';

type ScreenOrientationWithLock = ScreenOrientation & {
  lock?: (orientation: OrientationLock) => Promise<void>;
};

async function lockPortrait(orientation: ScreenOrientationWithLock): Promise<void> {
  try {
    await orientation.lock?.('portrait-primary');
  } catch {
    try {
      await orientation.lock?.('portrait');
    } catch {
      // Unsupported outside installed/fullscreen contexts on many browsers.
    }
  }
}

export function installPortraitOrientationLock(): () => void {
  if (typeof window === 'undefined') return () => {};

  const orientation = window.screen.orientation as ScreenOrientationWithLock | undefined;
  if (!orientation?.lock) return () => {};

  void lockPortrait(orientation);

  const relock = () => {
    void lockPortrait(orientation);
  };
  const relockWhenVisible = () => {
    if (document.visibilityState === 'visible') relock();
  };

  document.addEventListener('visibilitychange', relockWhenVisible);
  window.addEventListener('orientationchange', relock);

  return () => {
    document.removeEventListener('visibilitychange', relockWhenVisible);
    window.removeEventListener('orientationchange', relock);
  };
}
