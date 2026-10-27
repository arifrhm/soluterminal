import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { readFileSync } from 'node:fs';
import type { Todo } from '../../types';
import { useStore } from '../../store';
import { TodoSection } from './TodoSection';
import { useEntitlements } from '../../entitlements';

const style = document.createElement('style');
style.textContent = readFileSync('src/styles.css', 'utf8');
document.head.append(style);

const todo = (overrides: Partial<Todo> = {}): Todo => ({
  id: 'todo_one',
  text: 'Review the auth flow',
  done: false,
  createdAt: Date.now(),
  source: 'user',
  ...overrides,
});

type StoreState = ReturnType<typeof useStore.getState>;

function renderTodos(todos: Todo[], overrides: {
  addTodo?: StoreState['addTodo'];
  toast?: StoreState['toast'];
} = {}) {
  const addTodo = overrides.addTodo ?? vi.fn().mockResolvedValue(undefined);
  const toast = overrides.toast ?? vi.fn();
  useStore.setState({
    todos,
    addTodo,
    patchTodo: vi.fn().mockResolvedValue(undefined),
    removeTodo: vi.fn().mockResolvedValue(undefined),
    toast,
  });
  return { ...render(<TodoSection collapsed={false} onToggle={() => {}} />), addTodo, toast };
}

function rowFor(text: string): HTMLElement {
  const textNode = screen.getByText(text);
  const row = textNode.closest('div');
  if (!row) throw new Error(`row for ${text} not found`);
  return row;
}

describe('TodoSection task delegation', () => {
  beforeEach(() => {
    useStore.setState({ todos: [], layout: null });
    useEntitlements.setState({ loaded: true, tier: 'team', team: true, operator: true, account: true });
  });

  it('offers no delegate action on a public install (Tasks is team-only)', () => {
    useEntitlements.setState({ loaded: true, tier: 'none', team: false, operator: false, account: false });
    const { container } = renderTodos([todo()]);
    expect(container.querySelector('button[aria-label="Delegate todo as task"]')).toBeNull();
  });

  afterEach(() => {
    cleanup();
    vi.restoreAllMocks();
  });

  it('does not render a delegate action for a completed todo', () => {
    const { container } = renderTodos([todo({ done: true, doneAt: Date.now() })]);
    fireEvent.click(screen.getByText('1 done'));
    expect(container.querySelector('button[aria-label="Delegate todo as task"]')).toBeNull();
  });

  it('does not render a delegate action for a linked todo and shows its task id in the tooltip', () => {
    const item = todo({ taskId: 'wft_linked' });
    const { container } = renderTodos([item]);
    expect(container.querySelector('button[aria-label="Delegate todo as task"]')).toBeNull();
    expect(rowFor(item.text).title).toContain('Delegated: wft_linked');
  });

  it('shows the matching todo text for a possible duplicate in the tooltip', () => {
    const original = todo({ id: 'todo_original', text: 'Audit authentication' });
    const possibleDuplicate = todo({ id: 'todo_duplicate', duplicateOf: original.id });
    renderTodos([possibleDuplicate, original]);
    expect(rowFor(possibleDuplicate.text).title).toContain('Possible duplicate of: Audit authentication');
  });

  it('clears the capture input immediately while addTodo is pending', async () => {
    let resolveAdd!: () => void;
    const pending = new Promise<void>((resolve) => { resolveAdd = resolve; });
    const addTodo = vi.fn(() => pending);
    renderTodos([], { addTodo });
    const input = screen.getByPlaceholderText('Capture an idea…') as HTMLInputElement;

    fireEvent.change(input, { target: { value: 'buy milk' } });
    fireEvent.keyDown(input, { key: 'Enter' });

    expect(addTodo).toHaveBeenCalledWith('buy milk');
    expect(input.value).toBe('');
    resolveAdd();
    await waitFor(() => expect(input.value).toBe(''));
  });

  it('restores the capture text and toasts when addTodo fails', async () => {
    const addTodo = vi.fn().mockRejectedValue(new Error('offline'));
    const toast = vi.fn();
    renderTodos([], { addTodo, toast });
    const input = screen.getByPlaceholderText('Capture an idea…') as HTMLInputElement;

    fireEvent.change(input, { target: { value: 'buy milk' } });
    fireEvent.keyDown(input, { key: 'Enter' });

    await waitFor(() => expect(input.value).toBe('buy milk'));
    expect(toast).toHaveBeenCalledWith('Add failed: offline', { kind: 'error', icon: '⚠' });
  });

  it('filters todos by workspace and restores all workspaces', () => {
    renderTodos([
      todo({ id: 'todo_solu', text: 'Review daemon', workspace: 'soluterminal' }),
      todo({ id: 'todo_web', text: 'Review landing page', workspace: 'website' }),
    ]);
    const select = screen.getByRole('combobox', { name: 'Filter by workspace' });

    fireEvent.change(select, { target: { value: 'website' } });
    expect(screen.queryByText('Review daemon')).toBeNull();
    expect(screen.getByText('Review landing page')).toBeInTheDocument();

    fireEvent.change(select, { target: { value: '' } });
    expect(screen.getByText('Review daemon')).toBeInTheDocument();
    expect(screen.getByText('Review landing page')).toBeInTheDocument();
  });

  it('treats mixed-case workspace tags as one filter value', () => {
    renderTodos([
      todo({ id: 'todo_upper', text: 'Review daemon', workspace: 'Soluterminal' }),
      todo({ id: 'todo_lower', text: 'Review web UI', workspace: 'soluterminal' }),
    ]);
    const select = screen.getByRole('combobox', { name: 'Filter by workspace' });

    expect(screen.getAllByRole('option')).toHaveLength(2);
    fireEvent.change(select, { target: { value: 'Soluterminal' } });
    expect(screen.getByText('Review daemon')).toBeInTheDocument();
    expect(screen.getByText('Review web UI')).toBeInTheDocument();
  });

  it('searches case-insensitively without changing server order', () => {
    renderTodos([
      todo({ id: 'todo_third', text: 'Third task' }),
      todo({ id: 'todo_first', text: 'First task' }),
      todo({ id: 'todo_second', text: 'Second note' }),
    ]);
    const search = screen.getByRole('textbox', { name: 'Search todos' });

    fireEvent.change(search, { target: { value: 'TASK' } });
    expect(screen.getAllByText(/task/i).map((node) => node.textContent)).toEqual(['Third task', 'First task']);
    expect(screen.queryByText('Second note')).toBeNull();

    fireEvent.change(search, { target: { value: '' } });
    expect(screen.getAllByText(/task|note/i).map((node) => node.textContent)).toEqual(['Third task', 'First task', 'Second note']);
  });

  it('explains when active filters match no todos', () => {
    renderTodos([todo({ text: 'Review daemon' })]);

    fireEvent.change(screen.getByRole('textbox', { name: 'Search todos' }), { target: { value: 'zzz' } });

    expect(screen.getByText('No todos match this filter.')).toBeInTheDocument();
    expect(screen.queryByText('Review daemon')).toBeNull();
  });
});
