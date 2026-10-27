import { useEffect, useState } from 'react';
import { isMobileViewport, isProbablyMobileDevice } from '../lib/mobile';

export function useIsMobileView(breakpoint = 800) {
  const [isMobileView, setIsMobileView] = useState(
    isMobileViewport(breakpoint),
  );

  useEffect(() => {
    const onResize = () => setIsMobileView(isMobileViewport(breakpoint));
    window.addEventListener('resize', onResize);
    window.visualViewport?.addEventListener('resize', onResize);
    return () => {
      window.removeEventListener('resize', onResize);
      window.visualViewport?.removeEventListener('resize', onResize);
    };
  }, [breakpoint]);

  return isMobileView;
}

export function useIsMobileDevice(breakpoint = 800) {
  const [isMobileDevice, setIsMobileDevice] = useState(
    isProbablyMobileDevice(breakpoint),
  );

  useEffect(() => {
    const onResize = () => setIsMobileDevice(isProbablyMobileDevice(breakpoint));
    window.addEventListener('resize', onResize);
    window.visualViewport?.addEventListener('resize', onResize);
    return () => {
      window.removeEventListener('resize', onResize);
      window.visualViewport?.removeEventListener('resize', onResize);
    };
  }, [breakpoint]);

  return isMobileDevice;
}
