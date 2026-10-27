import { describe, expect, it } from 'vitest';
import { ApiError, isDeviceNotApprovedError } from '../api';
import { PrivateRelayApprovalNeeded, PrivateRelayUnavailable } from './privateRelay/transport';

// The bootstrap failure screen is chosen from the error that failed that
// load: only the daemon refusing this device counts as "needs approval".
describe('isDeviceNotApprovedError', () => {
  it('is true only for the 401 device_not_approved refusal', () => {
    expect(isDeviceNotApprovedError(new ApiError(401, 'refused', { error: 'device_not_approved' }))).toBe(true);
    expect(isDeviceNotApprovedError(new ApiError(401, 'sign in', { error: 'unauthorized' }))).toBe(false);
    expect(isDeviceNotApprovedError(new ApiError(503, 'down', { error: 'device_not_approved' }))).toBe(false);
    expect(isDeviceNotApprovedError(new TypeError('Failed to fetch'))).toBe(false);
    expect(isDeviceNotApprovedError(new Error('timeout'))).toBe(false);
  });

  it('counts an end-to-end handshake the computer refused, not an unreachable one', () => {
    const refused = new TypeError("Can't reach this computer securely (private relay: approval needed)", { cause: new PrivateRelayApprovalNeeded('approval needed') });
    const unreachable = new TypeError("Can't reach this computer securely (private relay: channel unavailable)", { cause: new PrivateRelayUnavailable('channel unavailable') });
    expect(isDeviceNotApprovedError(refused)).toBe(true);
    expect(isDeviceNotApprovedError(unreachable)).toBe(false);
  });
});
