import { expect, test } from './fixtures';

test('release readiness renders typed blockers and activation deadlocks fail closed', async ({ page }) => {
  const legacyReason = 'legacy bridge exited before it recorded a typed code';
  const skewAction = 'Start a runtime built from the release-cut source (or update SoluBrowse to its compatible contract), then Re-land.';
  await page.route('**/api/workflow/release-check', (route) => route.fulfill({
    status: 200,
    contentType: 'application/json',
    body: JSON.stringify({
      allowed: false,
      cut: {
        id: 'rc_release_diagnostics', version: '0.0.990', repoKey: '/home/user/project/.git',
        sourceSha: 'ba629081b01fc92beabaa848c69ec5186aeb6a7e', declaredBy: 'api', status: 'open',
        createdAt: '2026-09-07T08:00:00Z', updatedAt: '2026-09-07T08:01:00Z',
      },
      blockers: [{
        taskId: 'wft_browser', title: 'Re-land browser-bound candidate', status: 'completed',
        landOutcome: 'needs_merge', landBlocker: 'browser_accept_stale',
        why: 'required browser acceptance is unavailable', infraCode: 'runtime_version_skew',
        infraReason: 'SoluBrowse protocol 2024-11-05 is incompatible with 2025-03-26',
        conflictPaths: ['web/src/App.tsx'], blockedByTaskIds: ['wft_activation'], clearAction: skewAction,
      }, {
        taskId: 'wft_legacy', title: 'Legacy browser verdict', status: 'completed',
        landOutcome: 'needs_merge', why: 'legacy browser infrastructure verdict',
        infraCode: 'unknown_legacy', infraReason: legacyReason,
        clearAction: 'Repair browser infrastructure, then Re-land.',
      }],
      deadlocks: [{
        code: 'release_runtime_activation_cycle', blockerTaskId: 'wft_browser',
        activationTaskIds: ['wft_activation'],
        cause: 'The compatible runtime is waiting for this blocked release.',
        breakAction: 'Activate the compatible runtime outside this release cycle.',
      }],
      waived: [], included: [], warnings: [],
    }),
  }));

  await page.goto('/');
  await page.getByRole('button', { name: /unreleased commits/ }).click();
  const dialog = page.getByRole('dialog', { name: 'Release readiness' });
  await expect(dialog.locator('.vp-verdict')).toHaveText('BLOCKED — 2 task(s) in scope');
  await expect(dialog.locator('.vp-verdict')).not.toHaveClass(/release-verdict-safe/);
  await expect(dialog.getByText('runtime_version_skew', { exact: true })).toBeVisible();
  await expect(dialog.getByText(/SoluBrowse protocol 2024-11-05 is incompatible with 2025-03-26/)).toBeVisible();
  await expect(dialog.getByText(/conflict files:/)).toContainText('web/src/App.tsx');
  await expect(dialog.getByText(/waiting on:/)).toContainText('wft_activation');
  await expect(dialog.getByText(`Clear by: ${skewAction}`, { exact: true })).toBeVisible();
  await expect(dialog.getByText('unknown_legacy', { exact: true })).toBeVisible();
  await expect(dialog.getByText(new RegExp(legacyReason))).toBeVisible();

  const deadlock = dialog.getByRole('alert');
  await expect(deadlock).toContainText('Release/merge cycle detected');
  await expect(deadlock).toContainText('release_runtime_activation_cycle');
  await expect(deadlock).toContainText('blocker wft_browser');
  await expect(deadlock).toContainText('Activation tasks: wft_activation');
  await expect(deadlock).toContainText('The compatible runtime is waiting for this blocked release.');
  await expect(deadlock).toContainText('Break the cycle: Activate the compatible runtime outside this release cycle.');
});
