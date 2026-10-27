import { describe, expect, it, vi } from 'vitest';
import { createPreviewFetch } from './previewBoot';
import { isPreviewAcceptanceHost } from './previewAcceptanceShell';

describe('isolated acceptance preview', () => {
	it('blocks writes, mounted API escapes and external calls without network access', async () => {
		const nativeFetch = vi.fn<typeof fetch>();
		const base = 'https://preview.test/preview/candidate/';
		const wrapped = createPreviewFetch(nativeFetch, base, { offline: true });
		for (const method of ['POST', 'PUT', 'PATCH', 'DELETE']) {
			for (const url of ['/api/workflow/settings', base + 'api/workflow/settings']) {
				expect((await wrapped(url, { method })).status).toBe(403);
			}
		}
		expect((await wrapped(new Request(base + 'api/health', { method: 'POST' }))).status).toBe(403);
		expect((await wrapped('https://outside.test/api/health')).status).toBe(403);
		expect((await wrapped(base + 'api/health')).status).toBe(200);
		expect((await wrapped(base + 'api/missing')).status).toBe(404);
		expect(nativeFetch).not.toHaveBeenCalled();
	});
	it('scopes the shell to the non-loopback acceptance exposure host', () => {
		// Daemon browser-acceptance exposure binds a non-loopback IP.
		expect(isPreviewAcceptanceHost('172.22.149.245')).toBe(true);
		expect(isPreviewAcceptanceHost('10.0.0.7')).toBe(true);
		expect(isPreviewAcceptanceHost('192.168.1.20')).toBe(true);
		// Loopback, unspecified, wildcard and hostname previews keep the app.
		expect(isPreviewAcceptanceHost('127.0.0.1')).toBe(false);
		expect(isPreviewAcceptanceHost('0.0.0.0')).toBe(false);
		expect(isPreviewAcceptanceHost('localhost')).toBe(false);
		expect(isPreviewAcceptanceHost('preview.solu.dev')).toBe(false);
		expect(isPreviewAcceptanceHost('::1')).toBe(false);
		expect(isPreviewAcceptanceHost('')).toBe(false);
	});

	it('offline transport serves escaping API calls without touching the network', async () => {
		const nativeFetch = vi.fn<typeof fetch>(async () => {
			throw new Error('offline preview shim must not use the network');
		});
		const documentURL = 'https://preview.test/preview/pv_00000000000000000000000000/';
		const wrapped = createPreviewFetch(nativeFetch, documentURL, { offline: true });

		const health = await wrapped('/api/health');
		expect(health.status).toBe(200);
		await expect(health.json()).resolves.toMatchObject({ status: 'ok' });

		const panes = await wrapped('/api/panes');
		expect(panes.status).toBe(200);
		await expect(panes.json()).resolves.toEqual([]);

		const missing = await wrapped('/api/future-boot-poll');
		expect(missing.status).toBe(404);
		await expect(missing.json()).resolves.toMatchObject({ error: expect.any(String) });

		expect(nativeFetch).not.toHaveBeenCalled();
	});
});
