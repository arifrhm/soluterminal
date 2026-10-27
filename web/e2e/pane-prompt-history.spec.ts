import { expect, test } from './fixtures';
import type { PaneInfo, Workspace } from '../src/types';

for (const theme of ['dark', 'light']) {
  test(`pane header history restores, expands, copies and updates (${theme})`, async ({ page, context }, testInfo) => {
    await page.setViewportSize({ width: 1440, height: 900 });
    await context.grantPermissions(['clipboard-read', 'clipboard-write']);
    await page.addInitScript((theme) => {
      localStorage.setItem('solu.appTheme', theme);
      localStorage.setItem('solu.showPromptHistory', '1');
      const Native = window.EventSource;
      const sources: EventSource[] = [];
      (window as unknown as { __promptSources: EventSource[] }).__promptSources = sources;
      window.EventSource = class extends Native {
        constructor(...args: ConstructorParameters<typeof EventSource>) {
          super(...args);
          sources.push(this);
        }
      };
    }, theme);
    const pane: PaneInfo = {
      id: 'history_session', title: 'Prompt history regression', cwd: '/tmp', cmd: ['codex'],
      status: 'running', pid: 11, cols: 90, rows: 25, state: 'idle', hasCodex: true,
      createdAt: '2026-10-07T00:00:00Z', lastActivityMs: 0,
      lastPrompts: ['Please check the pane header history', 'Earlier request to implement a feature'],
    };
    const other: PaneInfo = { ...pane, id: 'history_other', title: 'Other workspace', pid: 12,
      lastPrompts: ['Only the other workspace prompt'] };
    const workspaces: Workspace[] = [
      { name: 'Prompt history', layout: { type: 'pane', paneId: pane.id }, paneIds: [pane.id], activeAt: '2026-10-07T00:00:00Z' },
      { name: 'Other history', layout: { type: 'pane', paneId: other.id }, paneIds: [other.id], activeAt: '2026-10-06T00:00:00Z' },
    ];
    await page.route('**/api/panes', route => route.fulfill({ json: [pane, other] }));
    await page.route('**/api/workspaces**', route => route.fulfill({ json: workspaces }));
    await page.routeWebSocket(/\/api\/panes\/history_.*\/io/, ws => {
      ws.send(Buffer.from('\r\nSession transcript remains available\r\n'));
    });
    await page.goto('/?ws=Prompt%20history');
    const terminal = page.locator(`[data-pane-id="${pane.id}"]`);
    const strip = terminal.locator('.pane-last-prompt');
    await expect(strip).toBeVisible();
    await expect(strip.locator('.pane-last-prompt-text')).toHaveText(pane.lastPrompts![0]);
    await strip.click();
    await expect(strip).toHaveAttribute('aria-expanded', 'true');
    await expect(strip.locator('.pane-last-prompt-text')).toHaveText(pane.lastPrompts!);
    await strip.getByRole('button', { name: 'Copy this prompt' }).nth(1).click();
    await expect.poll(() => page.evaluate(() => navigator.clipboard.readText()))
      .toBe(pane.lastPrompts![1]);
    await expect(strip).toHaveAttribute('aria-expanded', 'true');
    const screenshot = testInfo.outputPath(`prompt-history-${theme}.png`);
    await page.screenshot({ path: screenshot });
    await testInfo.attach('expanded prompt history', { path: screenshot, contentType: 'image/png' });
    await strip.locator('.pane-last-prompt-icon').click();

    await page.getByRole('button', { name: 'Settings', exact: true }).click();
    await page.getByRole('tab', { name: 'Terminal & panes', exact: true }).click();
    const toggle = page.getByRole('checkbox', { name: 'Show prompt history strip', exact: true });
    await toggle.uncheck();
    await expect(strip).toHaveCount(0);
    await toggle.check();
    await expect(strip).toHaveCount(1);
    await page.keyboard.press('Escape');

    pane.lastPrompts = ['A newly submitted prompt', ...pane.lastPrompts!];
    await page.evaluate(({ paneId, prompts }) => {
      for (const source of (window as unknown as { __promptSources: EventSource[] }).__promptSources) {
        source.dispatchEvent(new MessageEvent('message', {
          data: JSON.stringify({ type: 'pane-prompt', paneId, prompts }),
        }));
      }
    }, { paneId: pane.id, prompts: pane.lastPrompts });
    await expect(strip.locator('.pane-last-prompt-text')).toHaveText('A newly submitted prompt');

    // No SSE event for this update: the normal daemon DTO poll must heal it.
    pane.lastPrompts = ['Recovered after an event-stream gap', ...pane.lastPrompts];
    await expect(strip.locator('.pane-last-prompt-text'))
      .toHaveText('Recovered after an event-stream gap', { timeout: 10_000 });
    await page.locator('.ws-row').filter({ hasText: 'Other history' }).click();
    await expect(page.locator(`[data-pane-id="${other.id}"] .pane-last-prompt-text`))
      .toHaveText('Only the other workspace prompt');
    await page.locator('.ws-row').filter({ hasText: 'Prompt history' }).click();
    await expect(strip.locator('.pane-last-prompt-text')).toHaveText('Recovered after an event-stream gap');
    await page.reload();
    await expect(strip.locator('.pane-last-prompt-text')).toHaveText('Recovered after an event-stream gap');
  });
}
