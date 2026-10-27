import { describe, expect, it } from 'vitest';
import { desktopInstallerUrl } from './DesktopInstallerLink';

describe('desktop download link', () => {
  it('points at the website download page, never a release host', () => {
    expect(desktopInstallerUrl()).toBe('https://soluterminal.com/download/');
    expect(desktopInstallerUrl()).not.toMatch(/github|soluterminal-releases|onrender/);
  });
});
