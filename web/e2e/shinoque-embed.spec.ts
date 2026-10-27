import { expect, test } from './fixtures';
import { createServer } from 'node:http';

test('loopback parent receives ready and controls embedded first paint', async ({ page }) => {
  await page.route('**/shinoque-host', (route) => route.fulfill({
    contentType: 'text/html',
    body: `<script>
      window.embedMessages = [];
      window.addEventListener('message', event => {
        if (event.data && event.data.source === 'soluterminal') window.embedMessages.push(event.data);
      });
    </script><iframe title="SoluTerminal Code" src="/?embed=shinoque&theme=light" style="width:100%;height:700px"></iframe>`,
  }));
  await page.goto('/shinoque-host');
  const frame = page.frameLocator('iframe[title="SoluTerminal Code"]');
  await expect(frame.locator('.topbar')).toBeVisible();
  await expect.poll(() => page.evaluate(() => (window as Window & { embedMessages?: Array<{kind?: string}> }).embedMessages?.some((m) => m.kind === 'ready'))).toBe(true);
  await expect(frame.locator('html')).toHaveAttribute('data-theme', 'light');
  await expect(frame.locator('.topbar-app-icon')).toHaveCount(0);
  await page.evaluate(() => window.frames[0].postMessage({ source: 'shinoque', v: 1, kind: 'init', theme: 'dark' }, location.origin));
  await expect(frame.locator('html')).not.toHaveAttribute('data-theme', 'light');
  await page.evaluate(() => window.frames[0].postMessage({ source: 'shinoque', v: 1, kind: 'theme', theme: 'light' }, location.origin));
  await expect(frame.locator('html')).toHaveAttribute('data-theme', 'light');
  await page.locator('iframe').evaluate((iframe: HTMLIFrameElement) => { iframe.src = '/?theme=light'; });
  await expect(frame.locator('.topbar')).toBeVisible();
  await expect(frame.locator('.topbar-app-icon')).toHaveCount(0);
});
