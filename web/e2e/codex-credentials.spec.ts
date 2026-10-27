import fs from 'node:fs';
import path from 'node:path';
import { expect, test } from './fixtures';
import { installCredentialsKeysState } from '../tools/capture-credentials-keys.mjs';
import { dismissDaemonBanners } from '../tools/screens-lib.mjs';

for (const theme of ['dark', 'light']) {
  test(`Codex reconnect preserves the working account (${theme})`, async ({ page, context }) => {
    await page.addInitScript(installCredentialsKeysState, theme);
    let phase = 'waiting';
    let starts = 0;
    let device = false;
    let cancellations = 0;
    let pinWrites = 0;
    const json = (value: unknown) => ({ contentType: 'application/json', body: JSON.stringify(value) });
    const account = (name: string, active: boolean) => ({
      name, key: name, active, authKind: 'access_token', teamManaged: false, plan: 'pro',
      needsReauth: !active && phase !== 'complete',
      error: !active && phase !== 'complete' ? 'usage unavailable; saved login stale' : '',
      usage: {
        available: true, stale: !active, fetchedAtEpoch: 1789298322,
        fiveH: { available: false, utilPct: 0, resetEpoch: 0, resetInLabel: '', pacePct: 0 },
        sevenD: { available: true, utilPct: active ? 83 : 98, resetEpoch: 1789806320, resetInLabel: '5d21h', pacePct: 7 },
      },
    });
    await context.route('**/api/codex/accounts', (route) => route.fulfill(json({
      accounts: [account('working', true), account('personal', false)],
      active: 'working', liveAuthPresent: true, currentUnsaved: false,
    })));
    await context.route('**/api/codex/autoswitch', (route) => {
      if (route.request().method() === 'PUT') pinWrites += 1;
      return route.fulfill(json({
        enabled: true, pinned: '', switchThresholdPct: 85, softWarnPct: 90,
        pool: { working: true, personal: true }, state: 'ok', statusLine: 'on working',
      }));
    });
    await context.route('**/api/codex/accounts/personal/activate', (route) => route.fulfill({
      status: 422, body: 'saved Codex login needs sign-in; reconnect this account first',
    }));
    await context.route('**/api/codex/accounts/personal/reauth**', (route) => {
      if (route.request().method() === 'DELETE') {
        cancellations += 1;
        return route.fulfill(json({ cancelled: true }));
      }
      if (route.request().method() === 'POST') {
        starts += 1; phase = 'waiting';
        device = new URL(route.request().url()).searchParams.get('method') === 'device';
      }
      return route.fulfill(json({
        id: `fixture-login-${starts}`, name: 'personal', status: phase,
        ...(phase === 'waiting' ? device
          ? { authorizeUrl: 'https://auth.openai.com/codex/device', userCode: 'ABCD-EFGH' }
          : { authorizeUrl: 'https://auth.openai.com/authorize?state=fixture' } : {}),
        ...(phase === 'failed' ? { error: 'signed in to a different account or workspace; start again using this saved account' } : {}),
      }));
    });

    await page.goto('/');
    await dismissDaemonBanners(page);
    const section = page.locator('#sidebar [data-section-key="credentials"]');
    const personal = section.locator('.cred-row', { hasText: 'personal' });
    const working = section.locator('.cred-row', { hasText: 'working' });
    await expect(personal.getByRole('button', { name: 'Reconnect' })).toBeVisible();
    await expect(working).toHaveClass(/is-active/);
    await personal.hover();
    await personal.getByRole('button', { name: 'Pin and switch to Codex account' }).click();
    await expect(page.getByText(/Pin failed because the account couldn't switch/)).toBeVisible();
    await expect(working).toHaveClass(/is-active/);
    expect(pinWrites).toBe(0);
    const out = process.env.SOLU_CODEX_EVIDENCE_DIR;
    if (out) {
      fs.mkdirSync(out, { recursive: true });
      await page.screenshot({ path: path.join(out, `codex-stale-${theme}.png`), fullPage: true });
    }
    await personal.getByRole('button', { name: 'Reconnect' }).click();
    await expect(personal.getByRole('button', { name: 'Open sign-in' })).toBeVisible();
    await expect(personal.getByRole('button', { name: 'Cancel', exact: true })).toBeInViewport();
    const panel = personal.locator('.cred-codex-reauth');
    expect(await panel.evaluate((el) => el.scrollWidth <= el.clientWidth + 1)).toBe(true);
    if (out) await page.screenshot({ path: path.join(out, `codex-reconnect-${theme}.png`), fullPage: true });
    await personal.getByRole('button', { name: 'Use sign-in code' }).click();
    await expect(personal.getByLabel('Sign-in code')).toHaveText('ABCD-EFGH');
    await expect.poll(() => cancellations).toBe(1);
    await expect(working).toHaveClass(/is-active/);
    await expect(personal.getByRole('button', { name: 'Copy code' })).toBeInViewport();
    expect(await panel.evaluate((el) => el.scrollWidth <= el.clientWidth + 1)).toBe(true);
    if (out) await page.screenshot({ path: path.join(out, `codex-device-${theme}.png`), fullPage: true });
    phase = 'failed';
    await expect(personal.getByRole('alert')).toContainText('different account');
    await expect(working).toHaveClass(/is-active/);

    await personal.getByRole('button', { name: 'Reconnect' }).click();
    await expect(personal.getByRole('button', { name: 'Cancel', exact: true })).toBeVisible();
    await personal.getByRole('button', { name: 'Cancel', exact: true }).click();
    await expect.poll(() => cancellations).toBe(2);
    await expect(personal.getByRole('button', { name: 'Reconnect' })).toBeVisible();

    await personal.getByRole('button', { name: 'Reconnect' }).click();
    await expect(personal.getByRole('button', { name: 'Open sign-in' })).toBeVisible();
    phase = 'complete';
    await expect(personal.getByText('usage unavailable; saved login stale')).toHaveCount(0);
    await expect(working).toHaveClass(/is-active/);
    expect(starts).toBe(4);
  });
}
