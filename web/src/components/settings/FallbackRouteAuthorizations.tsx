import type { WorkflowFallbackRouteAuthorization, WorkflowFallbackRouteConsent, WorkflowRoleBinding } from '../../api';

const bindingKey = (b: WorkflowRoleBinding) => JSON.stringify([b.runtime, b.model ?? '', b.effort ?? '', b.permissionPreset ?? '', b.cacheTtl ?? 'auto']);
const routeKey = (from: WorkflowRoleBinding, to: WorkflowRoleBinding) => `${bindingKey(from)}>${bindingKey(to)}`;
const bindingLabel = (b: WorkflowRoleBinding) => `${b.model || 'model not set'} · ${b.runtime} · ${b.effort || 'effort not set'} · cache TTL ${b.cacheTtl ?? 'auto'}`;
const hasException = (a?: WorkflowFallbackRouteAuthorization) => !!a && (a.allowCriticalReview || a.allowPromptStructuredOutput || a.allowUnknownCost);

export function fallbackRouteAuthorizationInvalid(routes: WorkflowFallbackRouteConsent[]): boolean {
  return routes.some((r) => r.authorization && (!hasException(r.authorization) || !Number.isInteger(r.authorization.maxTaskInvocations) || r.authorization.maxTaskInvocations < 1 || r.authorization.maxTaskInvocations > 100));
}

export function FallbackRouteAuthorizations({ role, chain, routes, onChange, active }: {
  role: string;
  chain: WorkflowRoleBinding[];
  routes: WorkflowFallbackRouteConsent[];
  onChange: (routes: WorkflowFallbackRouteConsent[]) => void;
  active: boolean;
}) {
  const candidates = new Map<string, { from: WorkflowRoleBinding; to: WorkflowRoleBinding }>();
  chain.forEach((from, index) => chain.slice(index + 1).forEach((to) => {
    if (from.runtime && to.runtime && bindingKey(from) !== bindingKey(to)) candidates.set(routeKey(from, to), { from, to });
  }));
  routes.forEach((route) => candidates.set(routeKey(route.from, route.to), route));
  if (!candidates.size) return null;

  const update = (from: WorkflowRoleBinding, to: WorkflowRoleBinding, authorization: WorkflowFallbackRouteAuthorization) => {
    const key = routeKey(from, to);
    const rest = routes.filter((r) => routeKey(r.from, r.to) !== key);
    onChange(hasException(authorization) ? [...rest, { role, from: { ...from }, to: { ...to }, authorization, grantedAt: new Date().toISOString(), source: 'settings' }] : rest);
  };
  const criticalRole = role.startsWith('reviewer:') || role === 'refuter' || role === 'judge';

  return <fieldset className="wf-route-authorizations">
    <legend>Bounded route permissions</legend>
    <p className="settings-row-hint">Choose exceptions for an exact model and effort pair. This fallback remains within both the task budget and the route limit below. Retries, failed calls and recovery predecessors count. Task Ask or Never policies still apply.</p>
    {!active && <p className="settings-row-hint">These permissions are inactive until fallback policy controls are enabled.</p>}
    {[...candidates.entries()].map(([key, { from, to }], index) => {
      const route = routes.find((r) => routeKey(r.from, r.to) === key);
      const a = route?.authorization ?? { maxTaskInvocations: 20 };
      const explicit = !!from.model && !!to.model && !!from.effort && !!to.effort;
      const prefix = `${role} route ${index + 1}`;
      return <details key={key}>
        <summary>{bindingLabel(from)} → {bindingLabel(to)}{route ? ' · route permission' : ''}</summary>
        {!explicit && <p className="settings-row-hint">Set explicit models and efforts before granting route permissions.</p>}
        <label><input type="checkbox" aria-label={`${prefix} unknown cost`} disabled={!explicit} checked={!!a.allowUnknownCost} onChange={(event) => update(from, to, { ...a, allowUnknownCost: event.target.checked })} />Accept unknown cost within the task call limit</label>
        <p className="settings-row-hint">Cost may be unknown at first use. This permission offers no dollar guarantee; an unpriceable task with a dollar ceiling still waits. Known cost ratio limits still apply.</p>
        <label><input type="checkbox" aria-label={`${prefix} prompt structured output`} disabled={!explicit} checked={!!a.allowPromptStructuredOutput} onChange={(event) => update(from, to, { ...a, allowPromptStructuredOutput: event.target.checked })} />Allow prompt-based structured output with engine validation and at most two corrective retries</label>
        {criticalRole && <label><input type="checkbox" aria-label={`${prefix} critical review`} disabled={!explicit} checked={!!a.allowCriticalReview} onChange={(event) => update(from, to, { ...a, allowCriticalReview: event.target.checked })} />Allow critical review when tier, effort, permissions and independent review are preserved</label>}
        <label>Maximum recorded task calls <input className="settings-input" type="number" min={1} max={100} aria-label={`${prefix} task call limit`} disabled={!explicit || !hasException(a)} value={a.maxTaskInvocations} onChange={(event) => update(from, to, { ...a, maxTaskInvocations: Number(event.target.value) })} /></label>
        <p className="settings-row-hint">This fallback may run only before the task reaches {a.maxTaskInvocations} recorded calls; the task's own limits also apply. Noncritical routes may use a lower model tier while meeting the configured role floor. Required sandbox and tool isolation remain enforced.</p>
        {route?.authorization?.allowUnknownCost && <p className="settings-row-hint">Cost unknown · fallback permitted before {a.maxTaskInvocations} recorded task calls.</p>}
        {route && <button type="button" className="settings-btn" onClick={() => onChange(routes.filter((r) => routeKey(r.from, r.to) !== key))}>Revoke exact route {index + 1}</button>}
      </details>;
    })}
  </fieldset>;
}
