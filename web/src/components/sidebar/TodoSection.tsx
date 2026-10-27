
import { useMemo, useRef, useState } from 'react';
import { useStore } from '../../store';
import type { Todo } from '../../types';
import { SectionHeader } from './SectionHeader';
import { IconListTodo, IconChevronDown, IconX } from '../ui/icons';
import { relativeTime } from './time';
import { useIsMobileView } from '../useIsMobileView';

// Touch devices have no hover, so hover-revealed controls are invisible
// there. Evaluated once — a device doesn't grow a mouse mid-session, and
// if it does (convertible laptops), hover still works.
const NO_HOVER = typeof window !== 'undefined'
  && typeof window.matchMedia === 'function'
  && window.matchMedia('(hover: none)').matches;

interface TodoSectionProps {
  collapsed: boolean;
  onToggle: () => void;
}

export function TodoSection({ collapsed, onToggle }: TodoSectionProps) {
  const isMobile = useIsMobileView();
  const todos = useStore((s) => s.todos);
  const addTodo = useStore((s) => s.addTodo);
  const patchTodo = useStore((s) => s.patchTodo);
  const removeTodo = useStore((s) => s.removeTodo);
  const toast = useStore((s) => s.toast);

  const [draft, setDraft] = useState('');
  const [adding, setAdding] = useState(false);
  const [showDone, setShowDone] = useState(false);
  const [query, setQuery] = useState('');
  const [workspaceFilter, setWorkspaceFilter] = useState('');
  const inputRef = useRef<HTMLInputElement | null>(null);

  // Split open/done in two passes — preserves server order (newest-first)
  // within each bucket, which matters because a freshly-added todo should
  // jump to the top of "open", not be sorted by some derived rank.
  const open = useMemo(() => todos
    .filter((t) => !t.done)
    .filter((t) => (!workspaceFilter || t.workspace?.trim().toLowerCase() === workspaceFilter.toLowerCase())
      && t.text.toLowerCase().includes(query.trim().toLowerCase())), [todos, query, workspaceFilter]);
  const done = useMemo(() => todos
    .filter((t) => t.done)
    .filter((t) => (!workspaceFilter || t.workspace?.trim().toLowerCase() === workspaceFilter.toLowerCase())
      && t.text.toLowerCase().includes(query.trim().toLowerCase())), [todos, query, workspaceFilter]);
  const workspaceTags = useMemo(() => {
    const byCaseFoldedName = new Map<string, string>();
    todos.forEach((t) => {
      const workspace = t.workspace?.trim();
      if (workspace && !byCaseFoldedName.has(workspace.toLowerCase())) {
        byCaseFoldedName.set(workspace.toLowerCase(), workspace);
      }
    });
    return Array.from(byCaseFoldedName.values()).sort((a, b) => a.localeCompare(b));
  }, [todos]);

  async function submit() {
    const text = draft.trim();
    if (!text || adding) return;
    setAdding(true);
    setDraft('');
    try {
      await addTodo(text);
      // Keep focus in the input — the capture-inbox flow rewards rapid
      // back-to-back additions, and stealing focus would punish that.
      inputRef.current?.focus();
    } catch (e) {
      setDraft(text);
      toast(`Add failed: ${(e as Error).message}`, { kind: 'error', icon: '⚠' });
    } finally {
      setAdding(false);
    }
  }

  async function toggleDone(id: string, next: boolean) {
    try {
      await patchTodo(id, { done: next });
    } catch (e) {
      toast(`Toggle failed: ${(e as Error).message}`, { kind: 'error', icon: '⚠' });
    }
  }

  async function handleDelete(id: string) {
    try {
      await removeTodo(id);
    } catch (e) {
      toast(`Delete failed: ${(e as Error).message}`, { kind: 'error', icon: '⚠' });
    }
  }

  // Counter shown in the header — open count is the "still to do" signal.
  // Hide entirely when zero so a clean inbox doesn't display "(0)".
  const countLabel = todos.filter((t) => !t.done).length || undefined;

  return (
    <div className="sidebar-section">
      <SectionHeader
        icon={<IconListTodo size={14} />}
        title="Todos"
        count={countLabel}
        collapsed={collapsed}
        onToggle={onToggle}
      />
      {!collapsed && (
        <>
          <div style={{ display: 'flex', gap: 4, marginBottom: 6 }}>
            <input
              ref={inputRef}
              type="text"
              value={draft}
              onChange={(e) => setDraft(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === 'Enter') { e.preventDefault(); void submit(); }
              }}
              placeholder="Capture an idea…"
              // 16px on mobile: anything smaller makes iOS Safari zoom the
              // whole page on focus. Desktop keeps the compact sidebar scale.
              style={{ flex: 1, fontSize: isMobile ? 16 : 11, padding: isMobile ? '6px 8px' : '3px 6px' }}
              disabled={adding}
            />
            <button
              onClick={() => void submit()}
              disabled={!draft.trim() || adding}
              style={{ fontSize: isMobile ? 14 : 11, padding: isMobile ? '4px 12px' : '2px 8px' }}
              title="Add to backlog (Enter)"
            >+</button>
          </div>
          {todos.length > 0 && (
            <div style={{ display: 'flex', gap: 4, marginBottom: 6 }}>
              <input
                type="text"
                value={query}
                onChange={(e) => setQuery(e.target.value)}
                aria-label="Search todos"
                placeholder="Search…"
                style={{ flex: 1, minWidth: 0, fontSize: isMobile ? 16 : 11, padding: isMobile ? '6px 8px' : '3px 6px' }}
              />
              <select
                value={workspaceFilter}
                onChange={(e) => setWorkspaceFilter(e.target.value)}
                aria-label="Filter by workspace"
                style={{ minWidth: 0, maxWidth: '45%', fontSize: isMobile ? 16 : 11, padding: isMobile ? '6px 4px' : '3px 4px' }}
              >
                <option value="">All workspaces</option>
                {workspaceTags.map((workspace) => (
                  <option key={workspace} value={workspace}>{workspace}</option>
                ))}
              </select>
            </div>
          )}
          {todos.length === 0 && (
            <div style={{ fontSize: 11, color: 'var(--fg-dim)' }}>
              Nothing here yet. Drop a thought above, or ask Claude to "add
              this to my todos" — it can append directly via the soluterminal
              MCP tool.
            </div>
          )}
          {todos.length > 0 && open.length === 0 && done.length === 0 && (
            <div style={{ fontSize: 11, color: 'var(--fg-dim)' }}>
              No todos match this filter.
            </div>
          )}
          {open.map((t) => (
            <TodoRow
              key={t.id}
              t={t}
              onToggle={(next) => void toggleDone(t.id, next)}
              onDelete={() => void handleDelete(t.id)}
            />
          ))}
          {done.length > 0 && (
            <div
              style={{ marginTop: 4, fontSize: 10, color: 'var(--fg-dim)', cursor: 'pointer', userSelect: 'none', display: 'flex', alignItems: 'center', gap: 4 }}
              onClick={() => setShowDone((v) => !v)}
              title={showDone ? 'Hide completed' : 'Show completed'}
            >
              <IconChevronDown size={11} style={{ transform: showDone ? 'none' : 'rotate(-90deg)', transition: 'transform 0.12s ease', flexShrink: 0 }} />
              {done.length} done
            </div>
          )}
          {showDone && done.map((t) => (
            <TodoRow
              key={t.id}
              t={t}
              onToggle={(next) => void toggleDone(t.id, next)}
              onDelete={() => void handleDelete(t.id)}
            />
          ))}
        </>
      )}
    </div>
  );
}

function TodoRow({
  t,
  onToggle,
  onDelete,
}: {
  t: Todo;
  onToggle: (done: boolean) => void;
  onDelete: () => void;
}) {
  const [hover, setHover] = useState(false);
  // Delegating hands the todo to the team-only Tasks engine.

  const duplicateText = useStore((s) => t.duplicateOf
    ? s.todos.find((candidate) => candidate.id === t.duplicateOf)?.text
    : undefined);
  const tip = [
    t.workspace ? `Workspace: ${t.workspace}` : null,
    t.source === 'claude' ? 'Added by Claude' : 'Added manually',
    `Created: ${relativeTime(t.createdAt)} (${new Date(t.createdAt).toLocaleString()})`,
    t.done && t.doneAt ? `Done: ${relativeTime(t.doneAt)}` : null,
    t.taskId ? `Delegated: ${t.taskId}` : null,
    duplicateText ? `Possible duplicate of: ${duplicateText}` : null,
  ].filter(Boolean).join('\n');

  return (
    <div
      className={`todo-row${hover ? ' is-hover' : ''}${NO_HOVER ? ' no-hover' : ''}`}
      data-todo-id={t.id}
      data-todo-row=""
      onMouseEnter={() => setHover(true)}
      onMouseLeave={() => setHover(false)}
      style={{
        display: 'flex',
        alignItems: 'flex-start',
        gap: 6,
        padding: '2px 0',
        fontSize: 12,
        opacity: t.done ? 0.55 : 1,
      }}
      title={tip}
    >
      <input
        type="checkbox"
        checked={t.done}
        onChange={(e) => onToggle(e.target.checked)}
        style={{ marginTop: 2 }}
      />
      <span
        style={{
          flex: 1,
          textDecoration: t.done ? 'line-through' : 'none',
          wordBreak: 'break-word',
        }}
      >
        {t.text}
        {t.workspace && (
          <span style={{ marginLeft: 6, color: 'var(--fg-dim)', fontSize: 10 }}>
            #{t.workspace}
          </span>
        )}
      </span>

      <button
        className="todo-row-action"
        onClick={onDelete}
        style={{
          background: 'transparent',
          border: 'none',
          color: 'var(--fg-dim)',
          cursor: 'pointer',
          padding: '0 4px',
          display: 'inline-flex',
        }}
        title="Remove"
        aria-label="Remove todo"
      ><IconX size={12} /></button>
    </div>
  );
}
