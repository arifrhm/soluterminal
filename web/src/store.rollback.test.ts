import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { api, ApiError } from './api';
import { cancelDeferredUpdateRetry, resetUpdateRecoveryForTests, useStore } from './store';

describe('rollback store actions', () => {
	const realRollback = useStore.getState().rollbackUpdate;
	const realRelease = useStore.getState().releaseUpdateHold;
	const realApply = useStore.getState().applyUpdate;

	beforeEach(() => {
		resetUpdateRecoveryForTests();
		useStore.setState({
			panes: {}, updateSettings: { autoApply: 'when-idle', shellAutoApply: 'when-idle' },
			updateStatus: { current: '1.0.0', latest: '2.0.0', available: true, holdVersion: '2.0.0' },
			deferredUpdate: null, rollbackDecline: null, updateApplyRequestInFlight: false,
			rollbackUpdate: realRollback, releaseUpdateHold: realRelease,
		});
	});

	afterEach(() => {
		cancelDeferredUpdateRetry();
		resetUpdateRecoveryForTests();
		vi.restoreAllMocks();
		vi.useRealTimers();
	});

	it('does not auto-apply an exact held daemon release', () => {
		const apply = vi.fn();
		useStore.setState({ applyUpdate: apply as never });
		useStore.getState().maybeAutoApplyUpdate();
		expect(apply).not.toHaveBeenCalled();
	});

	it('records an incompatible decline without throwing or applying', async () => {
		vi.spyOn(api, 'rollbackUpdate').mockRejectedValue(new ApiError(409, 'declined', {
			code: 'rollback_incompatible', store: 'workflow', requiredReader: 3,
			targetReader: 2, targetVersion: '0.9.0', message: 'nothing changed',
		}));
		await expect(useStore.getState().rollbackUpdate({ targetVersion: '0.9.0' })).resolves.toBeUndefined();
		expect(useStore.getState().rollbackDecline).toMatchObject({ store: 'workflow', requiredReader: 3, targetReader: 2 });
		expect(useStore.getState().updateStatus?.applying).toBe(false);
	});

	it('records a typed update hold and releases it through the API', async () => {
		vi.spyOn(api, 'rollbackUpdate').mockRejectedValue(new ApiError(409, 'held', {
			code: 'update_held', heldVersion: '2.0.0', holdReason: 'regression', holdExpiresMs: 456,
		}));
		await useStore.getState().rollbackUpdate({ targetVersion: '0.9.0' });
		expect(useStore.getState().updateStatus).toMatchObject({ holdVersion: '2.0.0', holdExpiresMs: 456, applying: false });

		vi.spyOn(api, 'releaseUpdateHold').mockResolvedValue({ current: '1.0.0', latest: '2.0.0', available: true });
		await useStore.getState().releaseUpdateHold();
		expect(useStore.getState().updateStatus?.holdVersion).toBeUndefined();
	});

	it('queues a busy rollback with rollback-specific retry intent', async () => {
		vi.spyOn(api, 'rollbackUpdate').mockRejectedValue(new ApiError(409, 'busy', {
			busy: true, cause: 'workflow', reason: 'workflow invocations are still running',
			workflowInflight: 2, workflowDeferDeadlineMs: 9000,
		}));
		await expect(useStore.getState().rollbackUpdate({ targetVersion: '0.9.0', holdHours: 48 })).resolves.toBeUndefined();
		expect(useStore.getState().deferredUpdate).toMatchObject({
			cause: 'workflow', workflowInflight: 2, rollbackTarget: '0.9.0', rollbackHoldHours: 48,
		});
	});

	it('treats a forward update_held refusal as a hold and never queues a retry', async () => {
		vi.useFakeTimers();
		useStore.setState({
			applyUpdate: realApply,
			updateStatus: { current: '1.0.0', latest: '2.0.0', available: true },
			deferredUpdate: null,
		});
		const apply = vi.spyOn(api, 'applyUpdate').mockRejectedValue(new ApiError(409, 'held', {
			code: 'update_held', heldVersion: '2.0.0', holdReason: 'known regression', holdExpiresMs: 987654,
		}));

		await expect(useStore.getState().applyUpdate()).resolves.toBeUndefined();
		expect(useStore.getState().updateStatus).toMatchObject({
			applying: false, holdVersion: '2.0.0', holdReason: 'known regression', holdExpiresMs: 987654,
		});
		expect(useStore.getState().deferredUpdate).toBeNull();
		await vi.advanceTimersByTimeAsync(60_000);
		expect(apply).toHaveBeenCalledTimes(1);
	});
});
