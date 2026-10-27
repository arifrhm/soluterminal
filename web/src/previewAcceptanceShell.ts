// Mounted acceptance uses a non-loopback IP; ordinary previews keep their backend.
export function isPreviewAcceptanceHost(hostname: string): boolean {
	const host = hostname.trim().replace(/^\[|\]$/g, '').toLowerCase();
	if (host === '' || host.includes('%')) return false;
	const ipv4 = host.split('.');
	if (ipv4.length === 4 && ipv4.every((part) => /^\d{1,3}$/.test(part) && Number(part) <= 255)) {
		return ipv4[0] !== '127' && ipv4[0] !== '0';
	}
	if (!host.includes(':')) return false;
	return host !== '::1' && host !== '::';
}
