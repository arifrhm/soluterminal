import { mkdir, writeFile, readFile } from 'node:fs/promises';
import { createHash } from 'node:crypto';
import path from 'node:path';
import type { PaneInfo, TurnRecord, Workspace } from '../src/types';
import { expect, test } from './fixtures';

const now = Date.parse('2026-10-06T13:00:00Z');
const baseTurn: TurnRecord = { n: 8, startedMs: now - 74_000, endedMs: now - 60_000, files: 0, added: 0, removed: 0, source: 'codex:synthetic-root', sourceTurnId: 'turn-8', outcome: 'completed' };

test('provider outcomes, completion clock, unread and retained diff navigation', async ({ page }, testInfo) => {
  test.setTimeout(90_000);
  await page.setViewportSize({ width: 1500, height: 950 });
  await page.clock.install({ time: new Date(now) });
  const agent: PaneInfo = { id: 't3_agent', title: 'Owner · terminal accuracy', cwd: '/fixture/terminal-accuracy', cmd: ['codex'], status: 'running', createdAt: '2026-10-06T12:00:00Z', cols: 80, rows: 30, pid: 1, state: 'idle', hasCodex: true, lastActivityMs: now - 60_000, turn: { active: false, count: 8, historyRevision: 1, last: { ...baseTurn } } };
  const shell: PaneInfo = { ...agent, id: 't3_shell', title: 'Shell', cmd: ['bash'], hasCodex: false, turn: undefined };
  const workspaces: Workspace[] = [{ name: 'Terminal accuracy', layout: { type: 'split', direction: 'horizontal', ratio: 0.5, a: { type: 'pane', paneId: shell.id }, b: { type: 'pane', paneId: agent.id } }, paneIds: [shell.id, agent.id], activeAt: '2026-10-06T12:00:00Z' }];
  let records = [{ ...baseTurn }, { ...baseTurn, n: 3, sourceTurnId: 'turn-3', startedMs: now - 140_000, endedMs: now - 120_000, files: 1, added: 3 }];
  let pending = false;
  await page.addInitScript(({ end }) => { localStorage.setItem('solu.activeWorkspace', 'Terminal accuracy'); localStorage.setItem('solu.paneLastViewed', JSON.stringify({ t3_agent: end + 1 })); }, { end: baseTurn.endedMs });
  await page.route('**/api/panes', (route) => route.fulfill({ json: [shell, agent] }));
  await page.route('**/api/workspaces**', (route) => route.fulfill({ json: workspaces }));
  await page.route('**/api/panes/t3_agent/turns', (route) => route.fulfill({ json: { turns: records } }));
  await page.route('**/api/panes/t3_agent/turns/*/diff', (route) => {
    const n = Number(new URL(route.request().url()).pathname.split('/').at(-2));
    const record = records.find((r) => r.n === n)!;
    if (pending && n === 3) return route.fulfill({ status: 404, body: 'Diff capture pending' });
    return route.fulfill({ json: { repo: agent.cwd, branch: `Turn ${n} · main`, staged: false, files: [{ path: `turn-${n}.txt`, status: 'M', added: 3, removed: 0, secret: false }], diff: `diff --git a/turn-${n}.txt b/turn-${n}.txt\n--- a/turn-${n}.txt\n+++ b/turn-${n}.txt\n@@ -1 +1 @@\n-before\n+after`, truncated: false, hiddenSecret: 0, turn: n, startedMs: record.startedMs, endedMs: record.endedMs, overlapPanes: ['Other owner'] } });
  });
  await page.route('**/api/git/diff**', (route) => route.fulfill({ json: { repo: agent.cwd, branch: 'main', staged: new URL(route.request().url()).searchParams.get('staged') === 'true', files: [], diff: '', truncated: false, hiddenSecret: 0 } }));
  await page.routeWebSocket(/\/api\/panes\/t3_.*\/io/, (ws) => {
    ws.send(Buffer.from(ws.url().includes('t3_agent') ? '\r\nSynthetic provider fixture\r\nWorked for 14s · 20:59\r\n' : '\r\n$ observing the owner\r\n'));
  });
  const captures: { file: string; sha256: string }[] = [];
  const capture = async (name: string) => {
    const dir = process.env.SOLU_T3_CAPTURE_DIR;
    const output = dir ? path.join(dir, `${name}.png`) : testInfo.outputPath(`${name}.png`);
    await mkdir(path.dirname(output), { recursive: true });
    await page.screenshot({ path: output });
    captures.push({ file: path.basename(output), sha256: createHash('sha256').update(await readFile(output)).digest('hex') });
  };
  await page.goto('/');
  const owner = page.locator('[data-pane-id="t3_agent"]');
  const chip = owner.locator('.pane-turn-chip');
  await expect(owner.locator('.xterm')).toBeVisible();
  await expect(chip).toHaveText('done 1m ago');
  await capture('completed-provider-time');
  await page.clock.fastForward(120_000);
  await expect(chip).toHaveText('done 3m ago');
  await page.locator('[data-pane-id="t3_shell"] .pane-header').click();
  agent.turn = { ...agent.turn!, uncertain: true };
  await expect(chip).toHaveCount(0);
  agent.turn = { ...agent.turn!, uncertain: false, last: { ...baseTurn, outcome: 'interrupted' } };
  await expect(chip).toContainText('interrupted');
  await expect(chip).not.toContainText('done');
  await capture('interrupted-provider-turn');
  agent.turn = { ...agent.turn!, last: { ...baseTurn, outcome: 'failed' } };
  await expect(chip).toContainText('failed');
  await capture('failed-provider-turn');
  agent.turn = { ...agent.turn!, active: true, startedMs: now + 125_000 };
  await expect(chip).toHaveCount(0);
  const ended = await page.evaluate(() => Date.now());
  records = [{ ...baseTurn, startedMs: ended - 8000, endedMs: ended, files: 1, added: 3 }, records[1]];
  agent.turn = { active: false, count: 8, historyRevision: 2, last: records[0] };
  await expect(chip).toContainText('new');
  await expect(chip).toContainText('Δ 1 file');
  await capture('finished-unread');
  await chip.click();
  await expect(page.getByText('Turn 8 · 1 of 2 retained')).toBeVisible();
  await expect(page.getByText('turn-8.txt', { exact: true }).first()).toBeVisible();
  pending = true;
  await page.getByText('‹ Prev', { exact: true }).click();
  await expect(page.getByText('Turn 3 · 2 of 2 retained')).toBeVisible();
  await expect(page.getByText(/Diff capture pending/).first()).toBeVisible();
  await capture('older-capture-pending');
  pending = false;
  agent.turn = { ...agent.turn!, historyRevision: 3 };
  await expect(page.getByText('turn-3.txt', { exact: true }).first()).toBeVisible();
  await expect(page.getByText(/Also working|Other owner/).first()).toBeVisible();
  await capture('retained-diff-ready');
  await page.getByText('All uncommitted', { exact: true }).click();
  await expect(page.getByRole('tablist', { name: 'Diff source' })).toBeVisible();
  await page.getByText('Staged', { exact: true }).click();
  if (process.env.SOLU_T3_CAPTURE_DIR) {
    await writeFile(path.join(process.env.SOLU_T3_CAPTURE_DIR, 'browser-manifest.json'), JSON.stringify({ fixture: 'synthetic provider DTOs; production web bundle', scope: 'UI acceptance only; no live-provider or deployment claim', completedAt: new Date().toISOString(), captures }, null, 2) + '\n');
  }
});
