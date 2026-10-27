#!/usr/bin/env node

// This capture deliberately extends screens-lib's fixture map with method-keyed
// mutations and status-bearing responses. The shared harness keeps its legacy
// bare-path behavior for GET fixtures; POST/DELETE keys are explicit so a
// mutation can never be shadowed by an older read fixture.

import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import {
	captureHarnessUrl,
	createServer,
	exitBrowserUnavailable,
	launchChromium,
	resolvePlaywright,
} from './screens-lib.mjs';
import { updateDeferredResponses } from './capture-screens.mjs';

function arg(name) {
	const index = process.argv.indexOf(name);
	return index >= 0 ? process.argv[index + 1] : '';
}

const dist = path.resolve(arg('--dist') || path.join(path.dirname(fileURLToPath(import.meta.url)), '..', 'dist'));
const out = path.resolve(arg('--out') || path.join(dist, '..', '..', 'docs', 'evidence', 'update-rollback'));
const buildVersion = JSON.parse(fs.readFileSync(path.join(dist, 'webdist.json'), 'utf8')).webVersion;
const holdExpiresMs = Date.parse('2026-09-09T12:00:00+08:00');
const rollbackStatus = {
	current: '0.0.1053', latest: '0.0.1053', available: false, applying: false,
	lastCheckedMs: Date.parse('2026-09-06T12:00:00+08:00'),
	notes: '- Verified rollback recovery\n- Exact-version update holds',
	holdVersion: '0.0.1053', holdReason: 'rollback from a workflow-regressing release', holdExpiresMs,
};
const responses = {
	...updateDeferredResponses(buildVersion),
	'/api/health': { status: 'ok', version: '0.0.1053', webVersion: buildVersion, backendID: 'wsl', panes: 0, uptimeSec: 3600 },
	'/api/update/settings': { autoApply: 'off', shellAutoApply: 'off' },
	'/api/update/status': rollbackStatus,
	'/api/update/rollback': {
		current: '0.0.1053',
		targets: [{ version: '0.0.1052', ranAtMs: Date.parse('2026-09-05T09:00:00+08:00'), eligible: true, reason: '' }],
		hold: { version: '0.0.1053', fromVersion: '0.0.1052', reason: rollbackStatus.holdReason, createdAtMs: Date.parse('2026-09-06T12:00:00+08:00'), expiresAtMs: holdExpiresMs },
		lastAttempt: null,
	},
	'POST /api/update/rollback': {
		__fixtureResponse: true,
		status: 409,
		body: {
			code: 'rollback_incompatible', store: 'workflow', requiredReader: 3,
			targetReader: 2, targetVersion: '0.0.1052',
			message: 'Rolling back would leave the older daemon unable to read existing workflow state, so the downgrade was declined rather than attempted.',
		},
	},
	'DELETE /api/update/rollback/hold': { __fixtureResponse: true, status: 200, body: { ...rollbackStatus, holdVersion: undefined, holdReason: undefined, holdExpiresMs: undefined } },
};

const playwright = await resolvePlaywright();
if (!playwright) exitBrowserUnavailable('Playwright module not found');
fs.mkdirSync(out, { recursive: true });
const browser = await launchChromium(playwright, { headless: true });
const server = createServer(dist, { apiResponses: responses, healthVersion: buildVersion });
await new Promise((resolve, reject) => {
	server.once('error', reject);
	server.listen(0, '127.0.0.1', resolve);
});
const address = server.address();
if (!address || typeof address === 'string') throw new Error('fixture server did not allocate a loopback port');

async function pageFor(theme) {
	const context = await browser.newContext({ viewport: { width: 1440, height: 1000 } });
	const page = await context.newPage();
	await page.addInitScript((selectedTheme) => {
		const fixedNow = Date.parse('2026-09-06T12:00:00+08:00');
		const NativeDate = Date;
		class FixedDate extends NativeDate {
			constructor(...args) { super(...(args.length ? args : [fixedNow])); }
			static now() { return fixedNow; }
		}
		window.Date = FixedDate;
		localStorage.setItem('solu.appTheme', selectedTheme);
		localStorage.setItem('solu.seenWelcome', '1');
		localStorage.setItem('solu.seenChecklist', '1');
		window.EventSource = class { addEventListener() {} close() {} };
	}, theme);
	await page.goto(captureHarnessUrl(`http://127.0.0.1:${address.port}/`), { waitUntil: 'load' });
	await page.locator('button.statusbar-version').waitFor({ state: 'visible' });
	await page.locator('button.statusbar-version').click();
	await page.getByRole('dialog', { name: 'Update status' }).waitFor();
	return { context, page };
}

try {
	for (const theme of ['light', 'dark']) {
		{
			const { context, page } = await pageFor(theme);
			await page.getByText('Resume updates now', { exact: true }).waitFor();
			await page.screenshot({ fullPage: true, path: path.join(out, `hold-banner-${theme}.png`) });
			await context.close();
		}
		{
			const { context, page } = await pageFor(theme);
			await page.getByRole('button', { name: 'Details' }).click();
			await page.getByRole('button', { name: 'Roll back this update' }).click();
			await page.getByRole('button', { name: 'Confirm rollback' }).waitFor();
			await page.screenshot({ fullPage: true, path: path.join(out, `rollback-confirm-${theme}.png`) });
			if (theme === 'dark') {
				await page.getByRole('button', { name: 'Confirm rollback' }).click();
				await page.getByText('Rollback declined — nothing was changed.', { exact: true }).waitFor();
				if (await page.locator('.update-overlay').count()) throw new Error('declined rollback mounted the update overlay');
				await page.screenshot({ fullPage: true, path: path.join(out, 'rollback-declined-dark.png') });
			}
			await context.close();
		}
	}
} finally {
	await browser.close();
	await new Promise((resolve) => server.close(resolve));
}

process.stdout.write(`${JSON.stringify({ shots: [
	'rollback-confirm-light.png', 'rollback-confirm-dark.png',
	'hold-banner-light.png', 'hold-banner-dark.png', 'rollback-declined-dark.png',
] })}\n`);
