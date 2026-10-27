import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { api, ApiError } from '../api';
import { useStore } from '../store';
import type { UpdateStatus } from '../types';
import { VersionPopover } from './VersionPopover';

const realRollbackUpdate = useStore.getState().rollbackUpdate;
const realReleaseUpdateHold = useStore.getState().releaseUpdateHold;

function renderRollback(status: UpdateStatus) {
	const anchor = document.createElement('button');
	document.body.appendChild(anchor);
	render(<VersionPopover anchor={anchor} status={status} currentFallback="2.0.0" onClose={() => {}} />);
	return anchor;
}

afterEach(() => {
	cleanup();
	document.body.replaceChildren();
	useStore.setState({
		rollbackDecline: null, deferredUpdate: null, updateApplyRequestInFlight: false, updateStatus: null,
		rollbackUpdate: realRollbackUpdate, releaseUpdateHold: realReleaseUpdateHold,
	});
	vi.restoreAllMocks();
	delete (window as unknown as { __SOLU_TAURI__?: unknown }).__SOLU_TAURI__;
	delete (window as unknown as { __TAURI_INTERNALS__?: unknown }).__TAURI_INTERNALS__;
});

describe('VersionPopover rollback recovery', () => {
	it('does not post until explicit confirmation and states versions and consequences', async () => {
		vi.spyOn(api, 'getRollbackTargets').mockResolvedValue({
			current: '2.0.0', targets: [{ version: '1.9.0', ranAtMs: 1, eligible: true }], hold: null, lastAttempt: null,
		});
		const rollback = vi.fn().mockResolvedValue(undefined);
		useStore.setState({ rollbackUpdate: rollback as never });
		renderRollback({ current: '2.0.0', available: false, workflowInflight: 2 });
		fireEvent.click(screen.getByRole('button', { name: /Details/ }));
		const action = await screen.findByRole('button', { name: 'Roll back this update' });
		fireEvent.click(action);
		expect(rollback).not.toHaveBeenCalled();
		expect(screen.getByText(/v2\.0\.0 → v1\.9\.0/)).toBeInTheDocument();
		expect(screen.getByText(/daemon restarts briefly/i)).toBeInTheDocument();
		expect(screen.getByText(/paused until/i)).toBeInTheDocument();
		fireEvent.click(screen.getByRole('button', { name: 'Confirm rollback' }));
		await waitFor(() => expect(rollback).toHaveBeenCalledWith({ targetVersion: '1.9.0', holdHours: 72, force: false }));
	});

	it('renders typed incompatibility without an applying overlay', async () => {
		vi.spyOn(api, 'getRollbackTargets').mockResolvedValue({
			current: '2.0.0', targets: [{ version: '1.9.0', ranAtMs: 1, eligible: true }], hold: null, lastAttempt: null,
		});
		vi.spyOn(api, 'rollbackUpdate').mockRejectedValue(new ApiError(409, 'declined', {
			code: 'rollback_incompatible', store: 'workflow', requiredReader: 3, targetReader: 2,
			targetVersion: '1.9.0', message: 'nothing changed',
		}));
		renderRollback({ current: '2.0.0', available: false });
		fireEvent.click(screen.getByRole('button', { name: /Details/ }));
		fireEvent.click(await screen.findByRole('button', { name: 'Roll back this update' }));
		fireEvent.click(screen.getByRole('button', { name: 'Confirm rollback' }));
		expect(await screen.findByText('Rollback declined — nothing was changed.')).toBeInTheDocument();
		expect(document.querySelector('.update-overlay')).toBeNull();
	});

	it('shows failed retry and releases a visible hold', async () => {
		vi.spyOn(api, 'getRollbackTargets').mockResolvedValue({ current: '2.0.0', targets: [], hold: null, lastAttempt: null });
		const retry = vi.fn().mockResolvedValue(undefined);
		const release = vi.fn().mockResolvedValue(undefined);
		useStore.setState({ rollbackUpdate: retry as never, releaseUpdateHold: release as never });
		renderRollback({
			current: '2.0.0', available: false,
			holdVersion: '2.1.0', holdExpiresMs: Date.parse('2026-09-09T00:00:00Z'),
			rollbackState: 'failed', rollbackTarget: '1.9.0', rollbackError: 'previous binary restored',
		});
		expect(screen.getByText(/previous binary restored/)).toBeInTheDocument();
		fireEvent.click(screen.getByRole('button', { name: 'Retry' }));
		await waitFor(() => expect(retry).toHaveBeenCalled());
		fireEvent.click(screen.getByRole('button', { name: 'Resume updates now' }));
		expect(release).toHaveBeenCalledOnce();
	});

	it('replaces the native installer action with the held-release banner', async () => {
		Object.defineProperty(window, '__SOLU_TAURI__', { value: true, configurable: true });
		Object.defineProperty(window, '__TAURI_INTERNALS__', {
			value: { invoke: vi.fn(async (command: string) => command === 'browser_pane_ping' ? '1.0.0' : undefined) },
			configurable: true,
		});
		vi.spyOn(api, 'getRollbackTargets').mockResolvedValue({ current: '1.0.0', targets: [], hold: null, lastAttempt: null });
		renderRollback({
			current: '1.0.0', available: false, shellCurrent: '1.0.0', shellLatest: '2.0.0',
			holdVersion: '2.0.0', holdReason: 'known regression', holdExpiresMs: Date.now() + 60_000,
		});
		expect(await screen.findByRole('button', { name: 'Resume updates now' })).toBeInTheDocument();
		expect(screen.queryByRole('button', { name: 'Update installer' })).not.toBeInTheDocument();
	});
});
