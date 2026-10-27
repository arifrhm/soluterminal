// Render tests for the native message views. The per-message hover action
// toolbar (copy / edit & resend) was removed from assistant + user message rows
// to reclaim the reserved row — only the ErrorView retry action and the failed-
// send retry remain. TodoView covers the `todo` plan card.

import { fireEvent, render, screen } from '@testing-library/react';
import { describe, expect, it, vi } from 'vitest';
import {
  TextView,
  UserMessageView,
  ErrorView,
  TodoView,
} from './NativeEventViews';
import type { NativeEvent } from '../../native/nativeEvents';

function ev(partial: Partial<NativeEvent> & { type: NativeEvent['type']; seq: number }): NativeEvent {
  return { paneId: 'p', fromReplay: false, ...partial } as NativeEvent;
}

describe('assistant TextView has no message-action toolbar', () => {
  it('renders the message but no copy action', () => {
    render(<TextView ev={ev({ type: 'text', seq: 1, text: 'hello world' })} />);
    expect(screen.getByText('hello world')).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Copy message' })).toBeNull();
  });
});

describe('UserMessageView has no edit/resend/copy actions', () => {
  it('renders the prompt text but no action buttons', () => {
    render(<UserMessageView ev={ev({ type: 'user_text', seq: 2, text: 'do the thing' })} />);
    expect(screen.getByText('do the thing')).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Edit & resend this prompt' })).toBeNull();
    expect(screen.queryByRole('button', { name: 'Resend this prompt as a new turn' })).toBeNull();
    expect(screen.queryByRole('button', { name: 'Copy message' })).toBeNull();
  });

  it('still shows the retry button on a failed send', () => {
    const onRetry = vi.fn();
    render(
      <UserMessageView
        ev={ev({ type: 'user_text', seq: 3, text: 'oops' })}
        failed
        onRetry={onRetry}
      />,
    );
    fireEvent.click(screen.getByRole('button', { name: 'Retry sending this message' }));
    expect(onRetry).toHaveBeenCalledTimes(1);
  });
});

describe('ErrorView retry action', () => {
  it('fires onRetry when present and omits it otherwise', () => {
    const onRetry = vi.fn();
    const { rerender } = render(
      <ErrorView ev={ev({ type: 'error', seq: 3, message: 'boom' })} onRetry={onRetry} />,
    );
    fireEvent.click(screen.getByRole('button', { name: 'Resend the last prompt' }));
    expect(onRetry).toHaveBeenCalledTimes(1);
    rerender(<ErrorView ev={ev({ type: 'error', seq: 3, message: 'boom' })} />);
    expect(screen.queryByRole('button', { name: 'Resend the last prompt' })).toBeNull();
  });
});

describe('TodoView renders the emitted todo event data', () => {
  it('shows each item, the done/total count, and active/done markers', () => {
    const { container } = render(
      <TodoView
        ev={ev({
          type: 'todo',
          seq: 4,
          items: [
            { text: 'first', status: 'completed' },
            { text: 'second', status: 'in_progress' },
            { text: 'third', status: 'pending' },
          ],
        })}
      />,
    );
    expect(screen.getByText('first')).toBeInTheDocument();
    expect(screen.getByText('second')).toBeInTheDocument();
    expect(screen.getByText('third')).toBeInTheDocument();
    // "Plan · 1/3" (one completed of three).
    expect(screen.getByText('Plan · 1/3')).toBeInTheDocument();
    expect(container.querySelector('.native-todo-item.done')).not.toBeNull();
    expect(container.querySelector('.native-todo-item.active')).not.toBeNull();
  });

  it('renders nothing for an empty plan', () => {
    const { container } = render(<TodoView ev={ev({ type: 'todo', seq: 5, items: [] })} />);
    expect(container.firstChild).toBeNull();
  });
});
