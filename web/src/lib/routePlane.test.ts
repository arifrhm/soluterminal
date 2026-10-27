import { describe, expect, it } from 'vitest';
import { failureScopeLabel, planeHeading, ROUTE_PLANE_HEADINGS, skipReasonLabel, planeOf } from './routePlane';

describe('planeOf', () => {
  it('honours an explicit plane', () => {
    expect(planeOf({ plane: 'quality' })).toBe('quality');
    expect(planeOf({ plane: 'availability' })).toBe('availability');
  });

  it('defaults old payloads by reason', () => {
    expect(planeOf({ reason: 'quota' })).toBe('availability');
    expect(planeOf({ reason: 'infrastructure' })).toBe('availability');
    expect(planeOf({ reason: 'escalation' })).toBe('quality');
    expect(planeOf(null)).toBe('availability');
    expect(planeOf({})).toBe('availability');
  });

  it('exposes one source of truth for both headings', () => {
    expect(planeHeading({ plane: 'availability' })).toBe(ROUTE_PLANE_HEADINGS.availability);
    expect(planeHeading({ plane: 'quality' })).toBe(ROUTE_PLANE_HEADINGS.quality);
    expect(ROUTE_PLANE_HEADINGS.availability).toBe('Unavailable: switch provider/account');
    expect(ROUTE_PLANE_HEADINGS.quality).toBe('Quality: escalate model/effort');
  });
});

describe('failureScopeLabel', () => {
  it('labels every typed scope and defaults unknown', () => {
    expect(failureScopeLabel('account')).toContain('Account');
    expect(failureScopeLabel('model')).toBe('Model');
    expect(failureScopeLabel('gateway')).toContain('Gateway');
    expect(failureScopeLabel('vendor')).toContain('vendor');
    expect(failureScopeLabel('local')).toContain('Local');
    expect(failureScopeLabel('unknown')).toBe('Unknown');
    expect(failureScopeLabel(undefined)).toBe('Unknown');
    expect(failureScopeLabel('made-up')).toBe('Unknown');
  });
});

describe('skipReasonLabel', () => {
  it('labels availability scopes', () => {
    expect(skipReasonLabel('scope_account')).toContain('account');
    expect(skipReasonLabel('scope_model')).toContain('model');
    expect(skipReasonLabel('scope_gateway')).toContain('Gateway');
    expect(skipReasonLabel('scope_vendor')).toContain('vendor');
    expect(skipReasonLabel('scope_local')).toContain('Local');
    expect(skipReasonLabel('scope_unknown')).toContain('park');
  });

  it('labels capability and cost reasons', () => {
    expect(skipReasonLabel('capability_lost:tool_isolation')).toContain('tool_isolation');
    expect(skipReasonLabel('capability_ineligible:mcp')).toContain('mcp');
    expect(skipReasonLabel('cost_ratio_exceeded')).toContain('Cost');
    expect(skipReasonLabel('below_floor')).toContain('floor');
  });

  it('labels quality reasons distinctly', () => {
    expect(skipReasonLabel('quality_no_evidence')).toContain('No quality evidence');
    expect(skipReasonLabel('quality_availability_failure')).toContain('Availability failure');
    expect(skipReasonLabel('quality_cycle')).toContain('cycle');
    expect(skipReasonLabel('quality_cost_ceiling')).toContain('Cost');
  });

  it('never presents a transport/harness fault as quality', () => {
    expect(skipReasonLabel('infrastructure')).not.toContain('quality');
    expect(skipReasonLabel('timeout')).not.toContain('quality');
    expect(skipReasonLabel('scope_gateway')).not.toContain('quality');
  });
});
