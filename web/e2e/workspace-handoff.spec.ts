import { expect, test } from './fixtures';
import type { BrowserContext, Locator, Page } from 'playwright/test';

async function fixtures(context: BrowserContext, beforeSave?: (name: string) => Promise<void>) {
  const pane = (id: string) => ({
    id,
    title: id,
    cwd: '/tmp',
    cmd: ['sh'],
    status: 'running',
    createdAt: '2026-10-12T00:00:00Z',
    cols: 80,
    rows: 30,
    pid: 1,
    lastActivityMs: 0,
    state: 'idle',
  });
  const panes = [pane('handoff_shell'), pane('other_shell')];
  const workspace = (name: string, id?: string) => ({
    name,
    paneIds: id ? [id] : [],
    panes: id ? [panes.find((p) => p.id === id)] : [],
    activeAt: '2026-10-12T00:00:00Z',
    layout: id ? { type: 'pane', paneId: id } : null,
  });
  const workspaces = [
    workspace('Handoff project', 'handoff_shell'),
    workspace('Other project', 'other_shell'),
    workspace('Empty project'),
  ];
  await context.route('**/api/panes', (route) => route.fulfill({ json: panes }));
  await context.route('**/api/workspaces**', async (route) => {
    if (route.request().method() === 'POST') {
      const value = route.request().postDataJSON();
      await beforeSave?.(value.name);
      const target = workspaces.find((entry) => entry.name === value.name);
      if (target) Object.assign(target, value);
      await route.fulfill({ json: value });
    } else await route.fulfill({ json: workspaces });
  });
  await context.routeWebSocket(/\/api\/panes\/(handoff_shell|other_shell)\/io/, (socket) => {
    socket.send(Buffer.from('\r\nThe terminal keeps running\r\n'));
    socket.onMessage(() => {});
  });
  await context.route('**/workspace-handoff-host*', (route) => {
    const params = new URL(route.request().url()).searchParams;
    const identity = params.get('window');
    const requested = params.get('workspace');
    return route.fulfill({
      contentType: 'text/html',
      body: `<script>
      addEventListener('message', event => {
        if (event.data?.kind !== 'ready') return;
        event.source.postMessage({ source: 'shinoque', v: 1, kind: 'init', theme: 'dark' }, location.origin);
        event.source.postMessage({ source: 'shinoque', v: 1, kind: 'visibility', visible: true }, location.origin);
      });
    </script><iframe title="Code" src="/?embed=shinoque&embedWindow=${identity}${requested ? '&ws=' + encodeURIComponent(requested) : ''}" style="width:100%;height:700px"></iframe>`,
    });
  });
}
const row = (page: Page, name: string) =>
  page.frameLocator('iframe').locator('.ws-row').filter({ hasText: name });
const terminal = (page: Page, id: string) =>
  page.frameLocator('iframe').locator(`[data-pane-id="${id}"] .xterm`);
async function visibility(frame: Locator, visible: boolean) {
  await frame.evaluate((frame: HTMLIFrameElement, visible) => {
    frame.contentWindow!.postMessage(
      { source: 'shinoque', v: 1, kind: 'visibility', visible },
      location.origin,
    );
    frame.style.display = visible ? 'block' : 'none';
  }, visible);
}

test('embedded windows move a hidden workspace and retain the old terminal view', async ({
  page,
  context,
}) => {
  await fixtures(context);
  await page.goto('/workspace-handoff-host?window=one&workspace=Handoff%20project');
  const first = page.frameLocator('iframe');
  await expect(terminal(page, 'handoff_shell')).toBeVisible();
  const original = await terminal(page, 'handoff_shell').elementHandle();
  const secondPage = await context.newPage();
  try {
    await secondPage.goto(new URL('/workspace-handoff-host?window=two', page.url()).href);
    const second = secondPage.frameLocator('iframe');
    await expect(row(secondPage, 'Handoff project')).toBeVisible();
    await visibility(page.locator('iframe'), false);
    await row(secondPage, 'Handoff project').click();
    await expect(terminal(secondPage, 'handoff_shell')).toBeVisible();
    await expect(
      first.getByRole('heading', {
        name: 'Workspace moved to another window',
        includeHidden: true,
      }),
    ).toHaveCount(1);
    expect(await original!.evaluate((element) => element.isConnected)).toBe(true);
    await visibility(page.locator('iframe'), true);
    await expect(
      first.getByRole('heading', { name: 'Workspace moved to another window' }),
    ).toBeVisible();
    await page.locator('iframe').evaluate((frame: HTMLIFrameElement) => {
      frame.contentWindow!.dispatchEvent(
        new KeyboardEvent('keydown', { key: 'n', altKey: true, bubbles: true }),
      );
    });
    await expect(first.getByRole('dialog')).toHaveCount(0);
    await first.getByRole('button', { name: 'Move here', exact: true }).click();
    await expect(terminal(page, 'handoff_shell')).toBeVisible();
    await expect(
      second.getByRole('heading', { name: 'Workspace moved to another window' }),
    ).toBeVisible();
    expect(await original!.evaluate((element) => element.isConnected)).toBe(true);
  } finally {
    await secondPage.close();
  }
});

test('different named workspaces remain usable concurrently in two Code windows', async ({
  page,
  context,
}) => {
  await fixtures(context);
  await page.goto('/workspace-handoff-host?window=one&workspace=Handoff%20project');
  const secondPage = await context.newPage();
  try {
    await secondPage.goto(
      new URL('/workspace-handoff-host?window=two&workspace=Other%20project', page.url()).href,
    );
    await expect(terminal(page, 'handoff_shell')).toBeVisible();
    await expect(terminal(secondPage, 'other_shell')).toBeVisible();
    await row(secondPage, 'Handoff project').click();
    await expect(
      secondPage
        .frameLocator('iframe')
        .getByText('Workspace is open in another window', { exact: true }),
    ).toBeVisible();
    // Declining the transfer keeps both existing Code workspaces usable.
    await secondPage
      .frameLocator('iframe')
      .getByRole('button', { name: 'Cancel', exact: true })
      .click();
    await expect(terminal(page, 'handoff_shell')).toBeVisible();
    await expect(terminal(secondPage, 'other_shell')).toBeVisible();
    // Keyboard blur alone does not yield a visible workspace.
    await expect(
      page
        .frameLocator('iframe')
        .getByRole('heading', { name: 'Workspace moved to another window' }),
    ).toHaveCount(0);
  } finally {
    await secondPage.close();
  }
});

test('a genuinely empty workspace can move while its old Code surface is hidden', async ({
  page,
  context,
}) => {
  await fixtures(context);
  await page.goto('/workspace-handoff-host?window=one&workspace=Empty%20project');
  await expect(row(page, 'Empty project')).toBeVisible();
  await visibility(page.locator('iframe'), false);
  const secondPage = await context.newPage();
  try {
    await secondPage.goto(new URL('/workspace-handoff-host?window=two', page.url()).href);
    await row(secondPage, 'Empty project').click();
    await expect(
      page
        .frameLocator('iframe')
        .getByRole('heading', { name: 'Workspace moved to another window', includeHidden: true }),
    ).toHaveCount(1);
    await expect(
      secondPage
        .frameLocator('iframe')
        .getByRole('heading', { name: 'Workspace moved to another window' }),
    ).toHaveCount(0);
  } finally {
    await secondPage.close();
  }
});

test('the outgoing pane is inert while its final handoff save is pending', async ({
  page,
  context,
}) => {
  let block = false;
  let release!: () => void;
  await fixtures(context, async (name) => {
    if (!block || name !== 'Handoff project') return;
    block = false;
    await new Promise<void>((resolve) => {
      release = resolve;
    });
  });
  await page.goto('/workspace-handoff-host?window=one&workspace=Handoff%20project');
  await expect(terminal(page, 'handoff_shell')).toBeVisible();
  const original = await terminal(page, 'handoff_shell').elementHandle();
  const secondPage = await context.newPage();
  try {
    await secondPage.goto(new URL('/workspace-handoff-host?window=two', page.url()).href);
    await row(secondPage, 'Handoff project').click();
    block = true;
    await secondPage
      .frameLocator('iframe')
      .getByRole('button', { name: 'Move here', exact: true })
      .click();
    const first = page.frameLocator('iframe');
    await expect(first.getByRole('status').filter({ hasText: 'Moving workspace…' })).toBeVisible();
    await expect(first.locator('[inert] .grid-area')).toHaveCount(1);
    await page.locator('iframe').evaluate((frame: HTMLIFrameElement) => {
      frame.contentWindow!.dispatchEvent(
        new KeyboardEvent('keydown', { key: 'n', altKey: true, bubbles: true }),
      );
    });
    await expect(first.getByRole('dialog')).toHaveCount(0);
    expect(await original!.evaluate((element) => element.isConnected)).toBe(true);
    release();
    await expect(terminal(secondPage, 'handoff_shell')).toBeVisible();
    await expect(
      first.getByRole('heading', { name: 'Workspace moved to another window' }),
    ).toBeVisible();
  } finally {
    release?.();
    await secondPage.close();
  }
});
