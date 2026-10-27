import { test, expect } from './fixtures';

for (const position of ['top-center', 'bottom-right', 'top-right', 'bottom-left']) {
  for (const width of [640, 1440]) {
    test(`preview notice origin and action ${position}/${width}`, async ({ page }, info) => {
      await page.setViewportSize({ width, height: 900 });
      await page.addInitScript((position) => {
        localStorage.setItem('solu.toastPosition', position);
        const Native = window.EventSource;
        const sources: EventSource[] = [];
        Object.assign(window, { __previewSources: sources });
        window.EventSource = class extends Native {
          constructor(...args: ConstructorParameters<typeof EventSource>) {
            super(...args);
            sources.push(this);
          }
        };
      }, position);
      const preview = { contentType: 'text/html', body: '<h1>Notice preview</h1>' };
      await page.route('http://127.0.0.1:5173/**', (route) => route.fulfill(preview));
      await page.route('http://localhost:5173/**', (route) => route.fulfill(preview));
      await page.route('**/api/proxy/5173*/**', (route) => route.fulfill(preview));
      await page.goto('/');
      await page.waitForFunction(() =>
        (window as unknown as { __previewSources: EventSource[] }).__previewSources.some((s) => s.onmessage),
      );
      await page.evaluate(() => {
        const data = JSON.stringify({
          type: 'port-up', url: 'http://localhost:5173', text: 'Vite · Dashboard', paneId: 'preview-pane',
          port: {port:5173,addr:'127.0.0.1',proto:'tcp',pid:42,process:'Vite',cmdline:'node vite',cwd:'/app',paneId:'preview-pane',firstAt:'2026-10-25T00:00:00Z',webStatus:'ready',scheme:'http',previewPath:'/api/proxy/5173~preview-fixture/'},
        });
        for (const source of (window as unknown as { __previewSources: EventSource[] }).__previewSources) {
          source.dispatchEvent(new MessageEvent('message', { data }));
        }
      });
      const notice = page.getByRole('button', { name: /Open preview: Preview detected.*Vite · Dashboard/ });
      await expect(notice).toBeVisible();
      await page.screenshot({ path: info.outputPath(`notice-${position}-${width}.png`) });
      await notice.click();
      await expect(page.frameLocator('.browser-pane iframe').getByRole('heading', { name: 'Notice preview' })).toBeVisible();
      await expect(notice).toHaveCount(0);
    });
  }
}
