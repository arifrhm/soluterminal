import { expect, test } from './fixtures';

// Zone (i) of the Agents sidebar section must settle into an honest error when
// the daemon refuses the autonomy and agents reads (the static preview answers
// every /api read with 403 "cross-origin request rejected"). Before the
// loadError/agentsError fallbacks this pane sat on "Loading…" forever: a failed
// first load never settled, so the only thing on screen was a load that could
// not finish. This drives the real built bundle and the rendered chip + pane.
test('Agents section settles into an honest error when the autonomy and agents reads are refused', async ({ page }) => {
  await page.addInitScript(() => localStorage.setItem('solu.sidebarOpenMiddle.v2', JSON.stringify(['agents'])));
  const refused = {
    status: 403,
    contentType: 'application/json',
    body: JSON.stringify({ error: 'cross-origin request rejected' }),
  };
  await page.route('**/api/autonomy', (route) => route.fulfill(refused));
  await page.route('**/api/agents', (route) => route.fulfill(refused));
  await page.route('**/api/agents/templates', (route) => route.fulfill(refused));

  await page.goto('/');

  const chip = page.locator('.agents-summary-chip');
  await expect(chip).toBeVisible();
  // The chip names the failure instead of showing an empty count summary.
  await expect(chip).toContainText('Autonomy overview unavailable');

  await chip.click();
  const overview = page.locator('.agents-overview-empty');
  await expect(overview).toHaveText('Could not load the autonomy overview — the daemon did not answer. Retrying…');
  await expect(overview).not.toHaveText('Loading…');

  await expect(page.locator('.agents-empty-body')).toContainText('Could not load your agents — the daemon did not answer. Retrying…');
});
