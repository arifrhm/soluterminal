import { useEffect, useRef, useState } from 'react';
import { createPortal } from 'react-dom';
import { useStore } from '../store';
import { IconListTodo } from './ui/icons';
import { operatorFeaturesAllowed, personalViewOn } from '../entitlements';
import { usePrefs } from '../prefs';
import { todosSectionVisible } from './sidebar/sidebarSections';

// Quick todo capture — a Spotlight-style bar opened by the rebindable
// `todo.quickAdd` chord (default Alt+T), the command palette, or a
// `solu:todo-capture-open` event from anywhere. Deliberately minimal:
// type, Enter, it's saved to the global todo inbox (same store/API as the
// sidebar Todos section). Enter adds and closes (the common case — one
// capture, done). Shift+Enter adds and keeps the bar open for rapid
// back-to-back capture. Esc/backdrop closes. Portaled to <body> so it
// renders correctly no matter what dispatched it (including from inside
// the transformed mobile drawer).
export function TodoQuickAdd() {
  const addTodo = useStore((s) => s.addTodo);
  const toast = useStore((s) => s.toast);
  const [open, setOpen] = useState(false);
  const [draft, setDraft] = useState('');
  const [busy, setBusy] = useState(false);
  const [added, setAdded] = useState(0);
  const inputRef = useRef<HTMLInputElement | null>(null);

  useEffect(() => {
    const onOpen = () => {
      // Capture adds to the Todos section; with that section hidden (the
      // public default) there is nowhere to see the result.
      const sidebar = personalViewOn() ? undefined : usePrefs.getState().prefs.sidebar;
      if (!todosSectionVisible(sidebar?.order, sidebar?.hidden, operatorFeaturesAllowed())) return;
      setOpen((prev) => {
        if (!prev) {
          setAdded(0); // fresh session counter, but re-invoking while open keeps it
          // ...and a fresh bar starts EMPTY. The component is always mounted (the
          // `!open` early-return only drops the portaled subtree), so `draft`
          // survives every close path — Esc, backdrop, and the Enter-submit that
          // closes without clearing. Reset here, at the one choke point every
          // reopen flows through, rather than in each close path. Guarded by
          // `!prev` so re-invoking the chord while open never wipes text the
          // user is mid-typing.
          setDraft('');
        }
        return true;
      });
      // Re-focus when ALREADY open — the chord means "I want to type a todo".
      // (On first open the input doesn't exist yet; the effect below owns it.)
      requestAnimationFrame(() => inputRef.current?.focus());
    };
    window.addEventListener('solu:todo-capture-open', onOpen);
    return () => window.removeEventListener('solu:todo-capture-open', onOpen);
  }, []);

  // Focus after the input actually mounts — a rAF from the open handler fires
  // before React commits the first render, leaving keyboard focus (and Esc)
  // stranded on whatever was focused before.
  useEffect(() => {
    if (open) inputRef.current?.focus();
  }, [open]);

  if (!open) return null;

  const submit = async (keepOpen: boolean) => {
    const text = draft.trim();
    if (!text || busy) return;
    setBusy(true);
    try {
      await addTodo(text);
      if (keepOpen) {
        // Rapid capture (Shift+Enter): clear, count, stay for the next one.
        setDraft('');
        setAdded((n) => n + 1);
        inputRef.current?.focus();
      } else {
        // Common case (Enter): one capture, then dismiss so there's no
        // extra step to get back to work.
        setOpen(false);
      }
    } catch (e) {
      toast(`Add failed: ${(e as Error).message}`, { kind: 'error', icon: '⚠' });
    } finally {
      setBusy(false);
    }
  };

  return createPortal(
    <div
      className="todo-capture-backdrop"
      onMouseDown={(e) => {
        if (e.target === e.currentTarget) setOpen(false);
      }}
    >
      <div className="todo-capture" role="dialog" aria-label="Quick todo">
        <IconListTodo size={16} className="todo-capture-icon" />
        <input
          ref={inputRef}
          type="text"
          value={draft}
          disabled={busy}
          placeholder="Capture a todo…"
          aria-label="New todo text"
          onChange={(e) => setDraft(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === 'Enter') {
              e.preventDefault();
              void submit(e.shiftKey); // Shift+Enter keeps the bar open for rapid capture
            } else if (e.key === 'Escape') {
              e.preventDefault();
              setOpen(false);
            }
          }}
        />
        <span className="todo-capture-hint" aria-live="polite">
          {added > 0 ? `${added} added ✓ · ` : ''}Enter adds · Shift+Enter adds more · Esc closes
        </span>
      </div>
    </div>,
    document.body,
  );
}
