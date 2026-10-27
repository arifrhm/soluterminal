import type { UsageProvider } from '../../api';

// The provider filter: All | Claude Code | Codex. A segmented pill control
// matching LensToggle / the window toggle (same shared pill chrome). DUMB: the
// caller owns the `provider` state and decides when the toggle is shown (only
// once usage from more than one provider exists, or while a filter is active).
const PROVIDERS: Array<{ key: UsageProvider; label: string }> = [
  { key: 'all', label: 'All' },
  { key: 'claude', label: 'Claude Code' },
  { key: 'codex', label: 'Codex' },
];

export function ProviderToggle({
  provider,
  onChange,
  className = '',
}: {
  provider: UsageProvider;
  onChange: (p: UsageProvider) => void;
  className?: string;
}) {
  return (
    <div className={`usage-provider-toggle${className ? ' ' + className : ''}`} role="group" aria-label="Provider">
      {PROVIDERS.map((p) => (
        <button
          key={p.key}
          type="button"
          className={`usage-provider-btn${provider === p.key ? ' active' : ''}`}
          aria-pressed={provider === p.key}
          onClick={() => onChange(p.key)}
        >{p.label}</button>
      ))}
    </div>
  );
}
