import { afterEach, describe, expect, it, vi } from 'vitest';
import { api, ApiError } from './api';
import { useStore } from './store';

// Regression coverage for browser acceptance DEF-1 / DEF-2: when the daemon
// answers API reads with 403 "cross-origin request rejected" (static preview
// origin, stale shell origin), fire-and-forget refresh paths must settle as
// handled states instead of escaping as unhandled promise rejections.
// refreshPanes / refreshWorkspaces back every `void st.refresh*()` call site
// in App boot, SSE reconnect, and the 60s fallback poll; releaseUpdateHold
// backs the VersionPopover hold banner and DevicesTab "Resume updates now".
const CROSS_ORIGIN = new ApiError(403, 'cross-origin request rejected');

afterEach(() => {
	vi.restoreAllMocks();
});

describe('rejection safety under cross-origin daemon refusal', () => {
	it('refreshPanes settles instead of rejecting', async () => {
		vi.spyOn(api, 'listPanes').mockRejectedValue(CROSS_ORIGIN);
		await expect(useStore.getState().refreshPanes()).resolves.toBeUndefined();
	});

	it('refreshWorkspaces settles instead of rejecting', async () => {
		vi.spyOn(api, 'listWorkspaceSummaries').mockRejectedValue(CROSS_ORIGIN);
		await expect(useStore.getState().refreshWorkspaces()).resolves.toBeUndefined();
	});

	it('releaseUpdateHold settles with a toast instead of rejecting', async () => {
		vi.spyOn(api, 'releaseUpdateHold').mockRejectedValue(CROSS_ORIGIN);
		const toast = vi.fn();
		useStore.setState({ toast: toast as never });
		await expect(useStore.getState().releaseUpdateHold()).resolves.toBeUndefined();
		expect(toast).toHaveBeenCalled();
	});
});
