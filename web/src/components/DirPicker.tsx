import { useEffect, useLayoutEffect, useMemo, useRef, useState } from 'react';
import { createPortal } from 'react-dom';
import { api, type DirCandidate } from '../api';

interface Props {
  value: string;
  onChange: (v: string) => void;
  placeholder?: string;
  autoFocus?: boolean;
  id?: string;
  pinned?: DirCandidate[];
  inputClassName?: string;
}

// DirPicker is a combobox: a free-form path input with a dropdown of
// recent/discovered directories that fuzzy-filters as you type. Keyboard
// navigable. The user can either type a path literally or pick one.
export function DirPicker({ value, onChange, placeholder, autoFocus, id, pinned, inputClassName }: Props) {
  const [open, setOpen] = useState(false);
  const [items, setItems] = useState<DirCandidate[]>([]);
  const [highlight, setHighlight] = useState(0);
  const [loading, setLoading] = useState(false);
  // A prefilled repository is a default, not a search query: show the whole
  // browse list on first open, then filter normally once the user types.
  const [userEdited, setUserEdited] = useState(false);
  const inputRef = useRef<HTMLInputElement | null>(null);
  const rootRef = useRef<HTMLDivElement | null>(null);
  const menuRef = useRef<HTMLDivElement | null>(null);
  // The dropdown portals to <body> (fixed, anchored to the input) so it can
  // escape a scrollable/resizable dialog card, which would otherwise clip it.
  // `bottom` set instead of `top` ⇒ opens upward (input near the viewport
  // bottom), growing up from the input's top edge.
  const [menuPos, setMenuPos] = useState<
    { left: number; width: number; top?: number; bottom?: number } | null
  >(null);

  const query = pinned !== undefined && !userEdited ? '' : value;
  const merged = useMemo(() => {
    const normalizedQuery = query.toLowerCase();
    const seen = new Set<string>();
    const result: DirCandidate[] = [];
    for (const item of pinned ?? []) {
      if (normalizedQuery && !item.path.toLowerCase().includes(normalizedQuery) && !item.label.toLowerCase().includes(normalizedQuery)) continue;
      const key = item.path.toLowerCase();
      if (seen.has(key)) continue;
      seen.add(key);
      result.push(item);
    }
    for (const item of items) {
      const key = item.path.toLowerCase();
      if (seen.has(key)) continue;
      seen.add(key);
      result.push(item);
    }
    return result;
  }, [items, pinned, query]);
  const mergedPaths = merged.map((item) => item.path).join('\0');

  useEffect(() => {
    setHighlight(0);
  }, [mergedPaths]);

  useLayoutEffect(() => {
    if (!open) {
      setMenuPos(null);
      return;
    }
    const place = () => {
      const r = rootRef.current?.getBoundingClientRect();
      if (!r) return;
      const spaceBelow = window.innerHeight - r.bottom;
      // 220px menu max-height + margins; prefer below, flip up when cramped.
      const openUp = spaceBelow < 240 && r.top > spaceBelow;
      setMenuPos(
        openUp
          ? { left: r.left, width: r.width, bottom: window.innerHeight - r.top + 2 }
          : { left: r.left, width: r.width, top: r.bottom + 2 },
      );
    };
    place();
    window.addEventListener('resize', place);
    // Capture-phase so scrolls inside the dialog (not just the window) re-anchor.
    window.addEventListener('scroll', place, true);
    return () => {
      window.removeEventListener('resize', place);
      window.removeEventListener('scroll', place, true);
    };
  }, [open]);

  // Debounced fetch as the user types.
  useEffect(() => {
    let cancel = false;
    // Watchdog: listDirs goes over the cloud relay, where a request can stall
    // with no response. Without a bound the dropdown sticks on "searching…"
    // forever (the try/finally only clears loading once the request settles).
    // Drop the loading state after a timeout so the menu never looks hung.
    const watchdog = setTimeout(() => {
      if (!cancel) setLoading(false);
    }, 3000);
    const h = setTimeout(async () => {
      setLoading(true);
      try {
        const rows = await api.listDirs(query);
        if (!cancel) {
          // Older daemons answer `null` when nothing matches (an empty
          // home): treat it as no rows rather than crashing the dialog.
          setItems(Array.isArray(rows) ? rows : []);
          setHighlight(0);
        }
      } finally {
        if (!cancel) setLoading(false);
      }
    }, 150);
    return () => {
      cancel = true;
      clearTimeout(h);
      clearTimeout(watchdog);
    };
  }, [query]);

  // Close the dropdown on outside click.
  useEffect(() => {
    const onDoc = (e: MouseEvent) => {
      const t = e.target as Node;
      if (menuRef.current?.contains(t)) return;
      if (rootRef.current && !rootRef.current.contains(t)) {
        setOpen(false);
      }
    };
    document.addEventListener('mousedown', onDoc);
    return () => document.removeEventListener('mousedown', onDoc);
  }, []);

  function selectItem(idx: number) {
    const item = merged[idx];
    if (!item) return;
    onChange(item.path);
    setOpen(false);
    inputRef.current?.focus();
  }

  function onKeyDown(e: React.KeyboardEvent<HTMLInputElement>) {
    if (!open && (e.key === 'ArrowDown' || e.key === 'ArrowUp')) {
      setOpen(true);
      e.preventDefault();
      return;
    }
    if (!open) return;
    if (e.key === 'ArrowDown') {
      e.preventDefault();
      setHighlight((h) => Math.min(merged.length - 1, h + 1));
    } else if (e.key === 'ArrowUp') {
      e.preventDefault();
      setHighlight((h) => Math.max(0, h - 1));
    } else if (e.key === 'Enter') {
      if (merged[highlight]) {
        e.preventDefault();
        selectItem(highlight);
      }
    } else if (e.key === 'Escape') {
      // Stop here so a parent dialog's Escape-to-close doesn't also fire
      // when Escape was meant to just dismiss this dropdown.
      e.stopPropagation();
      setOpen(false);
    } else if (e.key === 'Tab' && merged[highlight]) {
      e.preventDefault();
      selectItem(highlight);
    }
  }

  return (
    <div className="dirpicker" ref={rootRef}>
      <input
        ref={inputRef}
        id={id}
        className={inputClassName}
        autoFocus={autoFocus}
        value={value}
        onChange={(e) => { setUserEdited(true); onChange(e.target.value); setOpen(true); }}
        // Open on click, NOT on focus: the New Session dialog autofocuses this
        // input, and focus-opens meant the suggestion menu popped over the
        // dialog on every open. Typing and ↑↓ still open it (see above).
        onClick={() => setOpen(true)}
        onKeyDown={onKeyDown}
        placeholder={placeholder}
        spellCheck={false}
        autoComplete="off"
        style={{ width: '100%' }}
      />
      {open && (merged.length > 0 || loading) && menuPos && createPortal(
        <div
          ref={menuRef}
          className="dirpicker-menu"
          style={{
            left: menuPos.left,
            width: menuPos.width,
            top: menuPos.top,
            bottom: menuPos.bottom,
          }}
        >
          {loading && merged.length === 0 && (
            <div className="dirpicker-empty">searching…</div>
          )}
          {merged.map((item, i) => (
            <div
              key={item.path}
              className={`dirpicker-item ${i === highlight ? 'highlight' : ''}`}
              onMouseEnter={() => setHighlight(i)}
              onMouseDown={(e) => { e.preventDefault(); selectItem(i); }}
            >
              <span className={`dirpicker-badge ${item.source}`}>
                {item.source === 'recent' ? '↻' : ''}
              </span>
              <span className="dirpicker-label">{item.label}</span>
            </div>
          ))}
        </div>,
        document.body,
      )}
    </div>
  );
}
