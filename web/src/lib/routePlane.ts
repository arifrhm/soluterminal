// Single client-side mapping of the routing-plane vocabulary. Availability
// ("unavailable — switch provider/account") and quality ("escalate
// model/effort") are separate decisions and must never be presented as one.

export const ROUTE_PLANE_HEADINGS = {
  availability: 'Unavailable: switch provider/account',
  quality: 'Quality: escalate model/effort',
} as const;

export type RoutePlane = keyof typeof ROUTE_PLANE_HEADINGS;

type PlaneDecision = {
  plane?: 'availability' | 'quality' | string;
  reason?: string;
  trigger?: string;
};

/** Availability is the default; only an explicit quality signal escapes it. */
export function planeOf(decision: PlaneDecision | null | undefined): RoutePlane {
  if (decision?.plane === 'quality') return 'quality';
  if (decision?.plane === 'availability') return 'availability';
  const reason = (decision?.reason ?? '').toLowerCase();
  if (reason === 'escalation' || reason.startsWith('quality')) return 'quality';
  if (decision?.trigger && String(decision.trigger).length > 0 && reason === '') return 'quality';
  return 'availability';
}

export function planeHeading(decision: PlaneDecision | null | undefined): string {
  return ROUTE_PLANE_HEADINGS[planeOf(decision)];
}

const FAILURE_SCOPE_LABELS: Record<string, string> = {
  account: 'Account / entitlement pool',
  model: 'Model',
  gateway: 'Gateway / endpoint',
  vendor: 'Upstream vendor',
  local: 'Local runtime',
  unknown: 'Unknown',
};

export function failureScopeLabel(kind: string | null | undefined): string {
  if (!kind) return FAILURE_SCOPE_LABELS.unknown;
  return FAILURE_SCOPE_LABELS[kind] ?? FAILURE_SCOPE_LABELS.unknown;
}

const SKIP_REASON_LABELS: Record<string, string> = {
  scope_account: 'Same account / entitlement pool exhausted',
  scope_model: 'This model is unavailable',
  scope_gateway: 'Gateway / endpoint unavailable',
  scope_vendor: 'Upstream vendor unavailable',
  scope_local: 'Local runtime unavailable',
  scope_unknown: 'Availability scope unknown — park',
  below_floor: 'Below the role floor',
  credential_unavailable: 'Credential unavailable',
  breaker_open: 'Circuit breaker open',
  not_installed: 'Runtime not installed',
  unregistered: 'Runtime not registered',
  route_guard: 'Capability ineligible',
  not_scanned: 'Not reached',
  failed_head: 'The route that failed',
  quality_no_evidence: 'No quality evidence',
  quality_availability_failure: 'Availability failure, not quality',
  quality_below_floor: 'Below the role floor',
  quality_not_stronger: 'Not a stronger rung',
  quality_cycle: 'Escalation cycle refused',
  quality_budget_exhausted: 'Escalation budget exhausted',
  quality_cost_ceiling: 'Cost ceiling reached',
  quality_independence_conflict: 'Reviewer-author independence conflict',
  quality_binding_unavailable: 'Candidate binding unavailable',
  quality_degraded_consent: 'Degraded-consent conflict',
};

/** Human label for one machine-readable skip/reason code. */
export function skipReasonLabel(code: string | null | undefined): string {
  if (!code) return '';
  const raw = String(code).trim();
  if (SKIP_REASON_LABELS[raw]) return SKIP_REASON_LABELS[raw];
  if (raw.startsWith('capability_lost:')) {
    const cap = raw.slice('capability_lost:'.length);
    return cap ? `Capability ineligible: ${cap}` : 'Capability ineligible';
  }
  if (raw.startsWith('capability_ineligible:')) {
    const cap = raw.slice('capability_ineligible:'.length);
    return cap ? `Capability ineligible: ${cap}` : 'Capability ineligible';
  }
  if (raw.startsWith('ineligible:')) return skipReasonLabel(raw.slice('ineligible:'.length));
  if (raw.startsWith('incompatible:')) {
    return raw
      .slice('incompatible:'.length)
      .split(',')
      .map((part) => skipReasonLabel(part.trim()))
      .filter(Boolean)
      .join('; ');
  }
  if (raw.startsWith('cost_')) return 'Cost bound exceeded';
  switch (raw) {
    case 'quota':
      return 'Quota exhausted';
    case 'auth':
      return 'Sign-in required';
    case 'billing':
      return 'Billing problem';
    case 'infrastructure':
      return 'Infrastructure failure';
    case 'timeout':
      return 'Timed out';
    case 'health':
      return 'Unhealthy';
  }
  return raw.replaceAll('_', ' ');
}
