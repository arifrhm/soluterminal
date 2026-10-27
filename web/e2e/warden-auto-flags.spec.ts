import { expect, test } from './fixtures';

test('legacy Warden settings show automatic flags off and preserve tuning on saves', async ({ page }) => {
  let settings: Record<string, unknown> = {
    enabled: true,
    model: 'claude-sonnet-5',
    triageAfterMin: 27,
    maxRunsPerHour: 4,
    // An older settings file has no autoFlags field.
  };
  const patches: Record<string, unknown>[] = [];
  await page.route('**/api/warden/settings', async (route) => {
    if (route.request().method() === 'PUT') {
      const patch = route.request().postDataJSON() as Record<string, unknown>;
      patches.push(patch);
      settings = { ...settings, ...patch };
    }
    await route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(settings) });
  });
  await page.goto('/');
  await page.locator('.topbar button[aria-label="Settings"]').click();
  const dialog = page.getByRole('dialog', { name: 'Settings' });
  await dialog.getByRole('tab', { name: 'Warden' }).click();
  const automatic = dialog.getByRole('checkbox', { name: 'Automatic pane flags' });
  const master = dialog.getByRole('checkbox', { name: 'Enable the Warden' });
  await expect(master).toBeChecked();
  await expect(automatic).not.toBeChecked();
  await automatic.check();
  await expect.poll(() => patches.length).toBe(1);
  expect(patches[0]).toEqual({ autoFlags: true });
  await expect(automatic).toBeChecked();
  await page.reload();
  await page.locator('.topbar button[aria-label="Settings"]').click();
  const reopened = page.getByRole('dialog', { name: 'Settings' });
  await reopened.getByRole('tab', { name: 'Warden' }).click();
  const saved = reopened.getByRole('checkbox', { name: 'Automatic pane flags' });
  await expect(saved).toBeChecked();
  await expect(reopened.getByRole('checkbox', { name: 'Enable the Warden' })).toBeChecked();
  await saved.uncheck();
  await expect.poll(() => patches.length).toBe(2);
  expect(patches[1]).toEqual({ autoFlags: false });
  await expect(saved).not.toBeChecked();
  expect(settings).toMatchObject({ enabled: true, model: 'claude-sonnet-5', triageAfterMin: 27, maxRunsPerHour: 4, autoFlags: false });
});

test('an explicit false automatic-flag setting renders off', async ({ page }) => {
  await page.route('**/api/warden/settings', (route) => route.fulfill({
    status: 200,
    contentType: 'application/json',
    body: JSON.stringify({ enabled: true, autoFlags: false, triageAfterMin: 10, maxRunsPerHour: 6 }),
  }));
  await page.goto('/');
  await page.locator('.topbar button[aria-label="Settings"]').click();
  const dialog = page.getByRole('dialog', { name: 'Settings' });
  await dialog.getByRole('tab', { name: 'Warden' }).click();
  await expect(dialog.getByRole('checkbox', { name: 'Automatic pane flags' })).not.toBeChecked();
});
