// The owner team-view lens selector: Output | Efficiency | Cost. A segmented
// pill control matching the existing window/scope toggles in UsagePage (same
// chrome; magenta accent to read as the primary lens choice). DUMB: the caller
// owns the `lens` state and the column math each lens drives.
export type Lens = 'output' | 'efficiency' | 'cost';

const LENSES: Array<{ key: Lens; label: string }> = [
  { key: 'output', label: 'Output' },
  { key: 'efficiency', label: 'Efficiency' },
  { key: 'cost', label: 'Cost' },
];

export function LensToggle({
  lens,
  onChange,
}: {
  lens: Lens;
  onChange: (l: Lens) => void;
}) {
  return (
    <div className="usage-lens-toggle" role="group" aria-label="Lens">
      {LENSES.map((l) => (
        <button
          key={l.key}
          type="button"
          className={`usage-lens-btn${lens === l.key ? ' active' : ''}`}
          aria-pressed={lens === l.key}
          onClick={() => onChange(l.key)}
        >{l.label}</button>
      ))}
    </div>
  );
}
