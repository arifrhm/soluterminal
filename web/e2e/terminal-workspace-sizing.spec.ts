import type { Page } from 'playwright/test';
import type { Terminal } from '@xterm/xterm';
import type { PaneInfo, Workspace } from '../src/types';
import { expect, test } from './fixtures';

type TerminalWindow = Window & { __soluTerms: Map<string, Terminal>; savedTerms: Map<string, Terminal> };

async function dimensions(page: Page, key: string) {
  return page.evaluate((sessionKey) => {
    const term = (window as TerminalWindow).__soluTerms.get(sessionKey)!;
    return { cols: term.cols, rows: term.rows };
  }, key);
}

test('parked Codex and Claude keep their geometry and restore after a font change without replay', async ({ page }, testInfo) => {
  await page.setViewportSize({ width: 1600, height: 1000 });
  await page.clock.install();
  const panes: PaneInfo[] = ['codex', 'claude', 'shell'].map((runtime) => ({
    id: `sizing_${runtime}`, title: runtime, cwd: '/tmp', cmd: [runtime],
    status: 'running', createdAt: '2026-10-05T00:00:00Z', cols: 80, rows: 30,
    pid: 1, lastActivityMs: 0, state: 'idle', hasCodex: runtime === 'codex', hasClaude: runtime === 'claude',
  }));
  const workspaces: Workspace[] = [
    { name: 'Agents sizing', layout: { type: 'split', direction: 'horizontal', ratio: 0.5,
      a: { type: 'pane', paneId: panes[0].id }, b: { type: 'pane', paneId: panes[1].id } },
      paneIds: panes.slice(0, 2).map((pane) => pane.id), activeAt: '2026-10-05T00:00:00Z' },
    { name: 'Other sizing', layout: { type: 'pane', paneId: panes[2].id },
      paneIds: [panes[2].id], activeAt: '2026-10-04T00:00:00Z' },
  ];
  await page.addInitScript(() => {
    localStorage.setItem('solu.activeWorkspace', 'Agents sizing');
    localStorage.setItem('solu.warmPanes', '1');
  });
  await page.route('**/api/panes', (route) => route.fulfill({ json: panes }));
  await page.route('**/api/workspaces**', (route) => route.fulfill({ json: workspaces }));
  const httpResizes: string[] = [];
  await page.route('**/api/panes/*/resize', (route) => {
    httpResizes.push(route.request().url());
    return route.fulfill({ json: {} });
  });
  const connections: string[] = [];
  const frames: { id: string; type: string; cols?: number; rows?: number; visible?: boolean; atMs: number }[] = [];
  const paints: { id: string; cols: number; rows: number; atMs: number }[] = [];
  await page.routeWebSocket(/\/api\/panes\/sizing_.*\/io/, (ws) => {
    const id = new URL(ws.url()).pathname.split('/').at(-2)!;
    connections.push(id);
    ws.send(Buffer.from(`${id === 'sizing_claude' ? '\x1b[?1049h' : ''}\r\n${id} transcript preserved\r\n`));
    let geometry = { cols: 80, rows: 30 };
    ws.onMessage((data) => {
      if (typeof data !== 'string') return;
      const frame = JSON.parse(data);
      frames.push({ id, ...frame, atMs: Date.now() });
      if (frame.type === 'resize') geometry = { cols: frame.cols, rows: frame.rows };
      if (frame.type === 'repaint') {
        paints.push({ id, ...geometry, atMs: Date.now() });
        ws.send(Buffer.from(`\x1b7\x1b[${geometry.rows};1H\x1b[2KContext 61% used · GPT-6-Sol high\x1b8`));
      }
    });
  });
  await page.goto('/');
  const agents = page.locator('.ws-row').filter({ hasText: 'Agents sizing' });
  const other = page.locator('.ws-row').filter({ hasText: 'Other sizing' });
  const codex = page.locator('[data-pane-id="sizing_codex"]');
  for (const pane of panes.slice(0, 2)) {
    await expect(page.locator(`[data-pane-id="${pane.id}"] .xterm`)).toBeVisible();
  }
  // The registry is keyed by routing scope + pane ID. Capture each rendered
  // pane's exact key so later reads also detect disposal/recreation or replay.
  const terminalKeys = await page.evaluate((paneIds) => {
    const entries = [...(window as TerminalWindow).__soluTerms.entries()];
    return Object.fromEntries(paneIds.map((id) => {
      const matches = entries.filter(([, term]) => term.element?.closest('[data-pane-id]')?.getAttribute('data-pane-id') === id);
      if (matches.length !== 1) throw new Error(`Expected one registered terminal for ${id}, got ${matches.length}`);
      return [id, matches[0][0]];
    }));
  }, panes.slice(0, 2).map((pane) => pane.id));
  await expect.poll(() => dimensions(page, terminalKeys.sizing_codex)).toMatchObject({ cols: expect.any(Number) });
  // Let the initial font and restore passes settle before measuring.
  await page.waitForTimeout(1200);
  const original = await Promise.all(panes.slice(0, 2).map((pane) => dimensions(page, terminalKeys[pane.id])));
  await page.evaluate(() => {
    const win = window as TerminalWindow;
    win.savedTerms = new Map(win.__soluTerms);
  });
  httpResizes.length = 0;
  frames.length = 0;
  await other.click();
  await expect(codex).toBeHidden();

  // A global font update reaches warm background terminals too. Their cell
  // grid must stay intact until their own workspace is measurable again.
  await page.getByRole('button', { name: 'Settings', exact: true }).click();
  await page.getByRole('tab', { name: 'Appearance', exact: true }).click();
  await page.locator('.settings-row').filter({ has: page.getByText('Font size', { exact: true }) }).locator('input').fill('16');
  await page.waitForTimeout(1100);
  expect(await dimensions(page, terminalKeys.sizing_codex)).toEqual(original[0]);
  expect(await dimensions(page, terminalKeys.sizing_claude)).toEqual(original[1]);
  expect(httpResizes).toEqual([]);
  for (const id of ['sizing_codex', 'sizing_claude']) {
    const hiddenAt = frames.findIndex((frame) => frame.id === id && frame.type === 'visibility' && frame.visible === false);
    expect(hiddenAt).toBeGreaterThanOrEqual(0);
    expect(frames.slice(hiddenAt + 1).filter((frame) => frame.id === id && frame.type === 'resize')).toEqual([]);
  }
  await page.keyboard.press('Escape');
  await agents.click();
  await expect(codex).toBeVisible();
  await expect.poll(async () => (await dimensions(page, terminalKeys.sizing_codex)).cols).toBeLessThan(original[0].cols);
  await expect.poll(() => frames.filter((frame) => frame.id === 'sizing_codex' && frame.type === 'repaint').length).toBeGreaterThan(0);
  await expect.poll(() => page.evaluate((key) => {
    const term = (window as TerminalWindow).__soluTerms.get(key)!;
    return Array.from({ length: term.buffer.active.length }, (_, i) => term.buffer.active.getLine(i)?.translateToString(true))
      .some((line) => line?.includes('Context 61% used · GPT-6-Sol high'));
  }, terminalKeys.sizing_codex)).toBe(true);
  await page.waitForTimeout(1000);
  for (const id of ['sizing_codex', 'sizing_claude']) {
    const lastResize = frames.filter((frame) => frame.id === id && frame.type === 'resize').at(-1)!;
    expect({ cols: lastResize.cols, rows: lastResize.rows }).toEqual(await dimensions(page, terminalKeys[id]));
  }

  // Rapid switching while a layout resize is pending must not deliver a
  // delayed resize or screen repair to a pane that has just been parked.
  frames.length = 0;
  await page.setViewportSize({ width: 1450, height: 900 });
  await other.click();
  await page.waitForTimeout(1100);
  for (const id of ['sizing_codex', 'sizing_claude']) {
    const hiddenAt = frames.findIndex((frame) => frame.id === id && frame.type === 'visibility' && frame.visible === false);
    expect(hiddenAt).toBeGreaterThanOrEqual(0);
    expect(frames.slice(hiddenAt + 1).filter((frame) => frame.id === id && ['resize', 'repaint'].includes(frame.type))).toEqual([]);
  }
  await agents.click();
  await expect(codex).toBeVisible();
  await page.waitForTimeout(1100);
  const settled = await dimensions(page, terminalKeys.sizing_codex);
  const paintsBeforeSameSizeRestore = frames.filter((frame) => frame.id === 'sizing_codex' && frame.type === 'repaint').length;
  // A renderer refresh cannot repair incorrect buffer cells. Restore at the
  // exact same dimensions must still request the authoritative screen paint.
  const restoreGeometry = await page.evaluate(async (key) => {
    const term = (window as TerminalWindow).__soluTerms.get(key)!;
    await new Promise<void>((resolve) => term.write(`\x1b7\x1b[${term.rows};1H\x1b[2KContext\x1b8`, resolve));
    const host = term.element!.closest<HTMLElement>('.pane-body')!;
    const screen = term.element!.querySelector<HTMLElement>('.xterm-screen')!;
    return {
      flex: host.style.flex,
      tallerHeight: host.getBoundingClientRect().height + Math.ceil(screen.getBoundingClientRect().height / term.rows),
    };
  }, terminalKeys.sizing_codex);
  await other.click();
  await expect(codex).toBeHidden();
  // Force a one-row late layout settle: the first restore paint uses the
  // taller host, then the final restore pass must repair the original grid.
  // Control browser timers so this race does not depend on runner load.
  // Freeze wall time before pausing: a slow protocol round trip must not
  // turn the sampled pause target into a time in the past. Restore normal
  // clock advancement for the controlled layout passes below.
  const restoreClockTime = await page.evaluate(() => Date.now());
  await page.clock.setFixedTime(restoreClockTime);
  await page.clock.pauseAt(restoreClockTime);
  await page.clock.setSystemTime(restoreClockTime);
  await codex.locator('.pane-body').evaluate((host, height) => { (host as HTMLElement).style.flex = `0 0 ${height}px`; }, restoreGeometry.tallerHeight);
  await agents.click();
  await expect(codex).toBeVisible();
  await page.clock.runFor(700);
  await expect.poll(() => frames.filter((frame) => frame.id === 'sizing_codex' && frame.type === 'repaint').length)
    .toBeGreaterThan(paintsBeforeSameSizeRestore);
  expect((await dimensions(page, terminalKeys.sizing_codex)).rows).toBe(settled.rows + 1);
  expect(paints.filter((paint) => paint.id === 'sizing_codex').at(-1)?.rows).toBe(settled.rows + 1);
  await expect.poll(async () => {
    await page.clock.runFor(1);
    return page.evaluate((key) => {
      const term = (window as TerminalWindow).__soluTerms.get(key)!;
      return term.buffer.active.getLine(term.buffer.active.baseY + term.rows - 1)?.translateToString(true);
    }, terminalKeys.sizing_codex);
  }).toBe('Context 61% used · GPT-6-Sol high');
  await codex.locator('.pane-body').evaluate((host, flex) => { (host as HTMLElement).style.flex = flex; }, restoreGeometry.flex);
  await page.clock.runFor(1000);
  await page.clock.resume();
  expect(await dimensions(page, terminalKeys.sizing_codex)).toEqual(settled);
  try {
    await expect.poll(() => page.evaluate((key) => {
      const term = (window as TerminalWindow).__soluTerms.get(key)!;
      return term.buffer.active.getLine(term.buffer.active.baseY + term.rows - 1)?.translateToString(true);
    }, terminalKeys.sizing_codex)).toBe('Context 61% used · GPT-6-Sol high');
  } catch (error) {
    const terminal = await page.evaluate((key) => {
      const term = (window as TerminalWindow).__soluTerms.get(key)!;
      return { cols: term.cols, rows: term.rows, baseY: term.buffer.active.baseY,
        tail: Array.from({ length: 3 }, (_, i) => term.buffer.active.getLine(term.buffer.active.baseY + term.rows - 3 + i)?.translateToString(true)) };
    }, terminalKeys.sizing_codex);
    await testInfo.attach('restore-geometry-diagnostics', {
      body: JSON.stringify({ settled, terminal, frames, paints }, null, 2), contentType: 'application/json',
    });
    throw error;
  }
  expect(connections.sort()).toEqual(panes.map((pane) => pane.id).sort());
  expect(await page.evaluate((keys) => {
    const win = window as TerminalWindow;
    return ['sizing_codex', 'sizing_claude'].every((id) => {
      const term = win.__soluTerms.get(keys[id])!;
      return term === win.savedTerms.get(keys[id]) && Array.from({ length: term.buffer.active.length }, (_, i) =>
        term.buffer.active.getLine(i)?.translateToString(true)).some((line) => line?.includes(`${id} transcript preserved`));
    });
  }, terminalKeys)).toBe(true);
  await page.screenshot({ path: testInfo.outputPath('workspace-restored.png') });
});
