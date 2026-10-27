import { expect, test } from './fixtures';
import type { PaneInfo, Workspace } from '../src/types';
import type { Terminal } from '@xterm/xterm';

for (const failMove of [false, true]) {
  test(`pane workspace move ${failMove ? 'keeps source on failure' : 'persists and follows the existing session'}`, async ({ page }, testInfo) => {
    const pane = { id: 'move-session', title: 'Session to move', cwd: '/tmp', cmd: ['bash'], status: 'running',
      pid: 21, cols: 100, rows: 30, state: 'idle', createdAt: '2026-10-24T00:00:00Z', lastActivityMs: 0 } as PaneInfo;
    const other = { ...pane, id: 'destination-session', title: 'Destination session', pid: 22 };
    const workspaces: Workspace[] = [
      { name: 'Source', paneIds: [pane.id], panes: [pane], layout: { type: 'pane', paneId: pane.id }, activeAt: '2026-10-24T00:00:00Z' },
      { name: 'Destination', paneIds: [other.id], panes: [other], layout: { type: 'pane', paneId: other.id }, activeAt: '2026-10-23T00:00:00Z' },
    ];
    let moves = 0;
    const lifecycle: string[] = [];
    await page.route('**/api/panes', route => {
      if (route.request().method() !== 'GET') lifecycle.push(route.request().method());
      return route.fulfill({ json: [pane, other] });
    });
    await page.route('**/api/panes/*/restart', route => { lifecycle.push('restart'); return route.abort(); });
    await page.route('**/api/workspaces**', async route => {
      const request = route.request();
      if (new URL(request.url()).pathname.endsWith('/move-pane')) {
        moves++;
        expect(request.postDataJSON()).toMatchObject({ paneId: pane.id, destination: 'Destination', paneMoveVersion: 0 });
        if (failMove) return route.fulfill({ status: 500, body: 'Storage unavailable' });
        workspaces[0] = { ...workspaces[0], paneIds: [], panes: [], layout: null, paneMoveVersion: 1 };
        workspaces[1] = { ...workspaces[1], paneIds: [other.id, pane.id], panes: [other, pane], paneMoveVersion: 1,
          layout: { type: 'split', direction: 'horizontal', ratio: 0.5, a: { type: 'pane', paneId: other.id }, b: { type: 'pane', paneId: pane.id } } };
        return route.fulfill({ json: { source: workspaces[0], destination: workspaces[1] } });
      }
      if (request.method() === 'POST') {
        const saved = request.postDataJSON() as Workspace;
        const index = workspaces.findIndex(w => w.name === saved.name);
        if (index >= 0) {
          if ((saved.paneMoveVersion ?? 0) !== (workspaces[index].paneMoveVersion ?? 0)) return route.fulfill({ status: 409 });
          workspaces[index] = saved;
        }
        return route.fulfill({ json: saved });
      }
      return route.fulfill({ json: workspaces });
    });
    await page.routeWebSocket(/\/api\/panes\/.*\/io/, ws => {
      ws.send(JSON.stringify({ type: 'ownership', owned: true, owner: 'you' }));
      ws.send(Buffer.from('\r\nRetained transcript before moving\r\n'));
    });
    await page.goto('/?ws=Source');
    const terminal = page.locator(`[data-pane-id="${pane.id}"]`);
    await expect(terminal.locator('.xterm')).toBeVisible();
    await expect.poll(() => page.evaluate(id => {
      const registry = (window as unknown as { __soluTerms: Map<string, Terminal> }).__soluTerms;
      const entry = [...registry.entries()].find(([key]) => JSON.parse(key)[1] === id);
      if (!entry) return '';
      const buffer = entry[1].buffer.active;
      return Array.from({ length: buffer.length }, (_, i) => buffer.getLine(i)?.translateToString(true) ?? '').join('\n');
    }, pane.id)).toContain('Retained transcript before moving');
    await page.evaluate(id => {
      const win = window as unknown as { __soluTerms: Map<string, Terminal>; __movedTerminal?: Terminal };
      win.__movedTerminal = [...win.__soluTerms.entries()].find(([key]) => JSON.parse(key)[1] === id)?.[1];
    }, pane.id);
    await terminal.getByRole('button', { name: 'Pane menu', exact: true }).click();
    await page.getByRole('menuitem', { name: 'Move to workspace…', exact: true }).click();
    const dialog = page.getByRole('dialog', { name: 'Move pane to workspace' });
    await expect(dialog).toBeVisible();
    await dialog.getByRole('combobox', { name: 'Destination workspace' }).selectOption('Destination');
    await page.screenshot({ path: testInfo.outputPath('move-destination-picker.png') });
    await dialog.getByRole('button', { name: 'Move', exact: true }).click();
    await expect.poll(() => moves).toBe(1);
    await expect(terminal.locator('.xterm')).toBeVisible();
    expect(await page.evaluate(id => {
      const win = window as unknown as { __soluTerms: Map<string, Terminal>; __movedTerminal?: Terminal };
      return [...win.__soluTerms.entries()].find(([key]) => JSON.parse(key)[1] === id)?.[1] === win.__movedTerminal;
    }, pane.id)).toBe(true);
    if (failMove) {
      await expect(page.locator('.ws-row.active .ws-name')).toHaveText('Source');
      await expect(page.getByText(/Couldn't move pane/)).toBeVisible();
      expect(workspaces[0].paneIds).toEqual([pane.id]);
    } else {
      await expect(page.locator('.ws-row.active .ws-name')).toHaveText('Destination');
      await expect(terminal).toHaveClass(/focused/);
      expect(workspaces[0].paneIds).toEqual([]);
      await page.reload();
      await expect(page.locator('.ws-row.active .ws-name')).toHaveText('Destination');
      await expect(terminal.locator('.xterm')).toBeVisible();
    }
    expect(lifecycle).toEqual([]);
    await page.screenshot({ path: testInfo.outputPath('move-result.png') });
  });
}
