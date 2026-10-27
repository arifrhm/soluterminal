import { SITE_INSTALLER_SIZE, SITE_INSTALLER_URL } from './siteLinks';

export interface SiteInstaller {
  url: string;
  size: string;
}

export const DEFAULT_SITE_INSTALLER: SiteInstaller = { url: SITE_INSTALLER_URL, size: SITE_INSTALLER_SIZE };

// The cloud may name a different installer (SITE_INSTALLER_URL /
// SITE_INSTALLER_SIZE on the server). Accept only an https .exe that is not on
// a release host, so a bad value can't send visitors to GitHub or back to the
// download page (which would loop through the account step).
export function resolveSiteInstaller(url?: unknown, size?: unknown): SiteInstaller {
  return {
    url: validInstallerUrl(url) ?? DEFAULT_SITE_INSTALLER.url,
    size: typeof size === 'string' && /^\d{1,4}(\.\d)? MB$/.test(size.trim()) ? size.trim() : DEFAULT_SITE_INSTALLER.size,
  };
}

function validInstallerUrl(value: unknown): string | null {
  if (typeof value !== 'string' || !value.trim()) return null;
  try {
    const url = new URL(value.trim());
    if (url.protocol !== 'https:') return null;
    if (/github|onrender\.com$/i.test(url.hostname)) return null;
    if (!/\.exe$/i.test(url.pathname)) return null;
    return url.toString();
  } catch {
    return null;
  }
}

/**
 * The website's download page (direct download, checksum and install steps):
 * the folder the installer is served from, so one cloud setting moves both.
 */
export function siteDownloadPage(installerUrl: string): string {
  try {
    return new URL('./', installerUrl).toString();
  } catch {
    return new URL('./', DEFAULT_SITE_INSTALLER.url).toString();
  }
}
