import { describe, expect, it } from 'vitest';
import { deriveRollbackPresentation } from './types';

describe('deriveRollbackPresentation', () => {
	it('derives held and retryable failure state from daemon status', () => {
		expect(deriveRollbackPresentation({
			current: '1.0.0', available: false,
			holdVersion: '2.0.0', holdExpiresMs: 123,
			rollbackState: 'failed', rollbackTarget: '1.0.0', rollbackError: 'boot guard restored 2.0.0',
		})).toEqual({
			held: true, heldVersion: '2.0.0', holdExpiresMs: 123,
			rollbackFailed: true, rollbackTarget: '1.0.0', rollbackError: 'boot guard restored 2.0.0',
		});
	});
});
