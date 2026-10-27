import { useState } from 'react';

// Commit-on-blur numeric input for Settings rows: typing an intermediate value
// must never fire a save per keystroke, because every control in Settings
// writes straight through to the daemon (there is no Save button anywhere).
// Enter commits, Escape resets, and the value is clamped into [min, max] on
// the way out so a hand-typed 9999 becomes the cap rather than an error.
export function NumberInput({ label, value, min, max, placeholder, disabled, onCommit }: {
  label: string;
  value: number;
  min: number;
  max: number;
  placeholder: string;
  disabled?: boolean;
  onCommit: (value: number) => void;
}) {
  const [draft, setDraft] = useState(String(value));
  const [syncedValue, setSyncedValue] = useState(value);
  // Settings sections mount after their async settings fetch resolves. A
  // passive mount effect can therefore still be queued when the user types;
  // React's controlled-input restore then applies both updates and reverts the
  // fresh draft to the prop. Derive prop changes during render so mount never
  // enqueues that stale reset.
  if (value !== syncedValue) {
    setSyncedValue(value);
    setDraft(String(value));
  }
  const commit = () => {
    if (draft.trim() === '') {
      setDraft(String(value));
      return;
    }
    const parsed = Number(draft);
    if (!Number.isFinite(parsed)) {
      setDraft(String(value));
      return;
    }
    const next = Math.max(min, Math.min(max, Math.round(parsed)));
    setDraft(String(next));
    if (next !== value) onCommit(next);
  };
  return <input
    className="settings-input"
    aria-label={label}
    type="number"
    min={min}
    max={max}
    placeholder={placeholder}
    disabled={disabled}
    value={draft}
    onChange={(event) => setDraft(event.target.value)}
    onBlur={commit}
    onKeyDown={(event) => {
      if (event.key === 'Enter') { event.preventDefault(); commit(); }
      if (event.key === 'Escape') setDraft(String(value));
    }}
  />;
}
