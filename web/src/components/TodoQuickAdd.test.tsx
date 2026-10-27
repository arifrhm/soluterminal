import { act, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { TodoQuickAdd } from './TodoQuickAdd';
import { useStore } from '../store';
import { useEntitlements } from '../entitlements';
import { usePrefs } from '../prefs';

// The bar is opened by the `todo.quickAdd` chord (default Alt+T), the command
// palette, and this event — App dispatches it for all three, so driving the
// event IS the real open path.
const openBar = () =>
  act(() => {
    window.dispatchEvent(new CustomEvent('solu:todo-capture-open'));
  });

const input = () => screen.getByLabelText('New todo text') as HTMLInputElement;

describe('TodoQuickAdd draft lifecycle', () => {
  // Team installs show the Todos section by default, so capture is live.
  beforeEach(() => useEntitlements.setState({ loaded: true, team: true, operator: true }));
  afterEach(() => {
    useEntitlements.setState({ loaded: false, team: false, operator: false });
    vi.restoreAllMocks();
  });

  it('starts EMPTY on reopen after a submit', async () => {
    // The regression: TodoQuickAdd is always mounted (the `!open` early-return
    // only drops the portaled subtree), so `draft` survived every close path and
    // the next Alt+T came back holding the previous todo's text.
    const addTodo = vi.fn().mockResolvedValue(undefined);
    useStore.setState({ addTodo });
    render(<TodoQuickAdd />);

    openBar();
    fireEvent.change(input(), { target: { value: 'buy milk' } });
    fireEvent.keyDown(input(), { key: 'Enter' });
    await waitFor(() => expect(addTodo).toHaveBeenCalledWith('buy milk'));
    // Enter adds AND closes.
    await waitFor(() => expect(screen.queryByLabelText('New todo text')).toBeNull());

    openBar();
    expect(input().value).toBe('');
  });

  it('starts EMPTY on reopen after Esc abandoned a draft', async () => {
    useStore.setState({ addTodo: vi.fn().mockResolvedValue(undefined) });
    render(<TodoQuickAdd />);

    openBar();
    fireEvent.change(input(), { target: { value: 'half-typed thought' } });
    fireEvent.keyDown(input(), { key: 'Escape' });
    await waitFor(() => expect(screen.queryByLabelText('New todo text')).toBeNull());

    openBar();
    expect(input().value).toBe('');
  });

  it('keeps text the user is mid-typing when the chord fires while already open', () => {
    // The `!prev` guard: re-invoking the chord means "focus the bar", not "wipe
    // what I just typed".
    useStore.setState({ addTodo: vi.fn().mockResolvedValue(undefined) });
    render(<TodoQuickAdd />);

    openBar();
    fireEvent.change(input(), { target: { value: 'still writing this' } });
    openBar();
    expect(input().value).toBe('still writing this');
  });

  it('clears between rapid Shift+Enter captures but stays open', async () => {
    const addTodo = vi.fn().mockResolvedValue(undefined);
    useStore.setState({ addTodo });
    render(<TodoQuickAdd />);

    openBar();
    fireEvent.change(input(), { target: { value: 'first' } });
    fireEvent.keyDown(input(), { key: 'Enter', shiftKey: true });
    await waitFor(() => expect(addTodo).toHaveBeenCalledWith('first'));
    await waitFor(() => expect(input().value).toBe(''));
    expect(screen.getByLabelText('New todo text')).toBeInTheDocument();
  });

  it('does NOT lose the draft when the add fails, so the text can be retried', async () => {
    // The bar stays open on failure; wiping the user's words here would be worse
    // than the original bug.
    useStore.setState({
      addTodo: vi.fn().mockRejectedValue(new Error('offline')),
      toast: vi.fn(),
    });
    render(<TodoQuickAdd />);

    openBar();
    fireEvent.change(input(), { target: { value: 'important thing' } });
    fireEvent.keyDown(input(), { key: 'Enter' });
    await waitFor(() => expect(useStore.getState().toast).toHaveBeenCalled());
    expect(input().value).toBe('important thing');
  });
});

describe('TodoQuickAdd on a public install', () => {
  afterEach(() => {
    useEntitlements.setState({ loaded: false, team: false, operator: false });
    usePrefs.setState((st) => ({ prefs: { ...st.prefs, sidebar: undefined } }));
  });

  it('stays closed while the Todos section is hidden (the public default)', () => {
    useEntitlements.setState({ loaded: true, team: false, operator: false });
    render(<TodoQuickAdd />);
    openBar();
    expect(screen.queryByLabelText('New todo text')).toBeNull();
  });

  it('opens once the user shows the Todos section', () => {
    useEntitlements.setState({ loaded: true, team: false, operator: false });
    usePrefs.setState((st) => ({ prefs: { ...st.prefs, sidebar: { order: undefined, hidden: [] } } }));
    render(<TodoQuickAdd />);
    openBar();
    expect(screen.getByLabelText('New todo text')).toBeTruthy();
  });
});
