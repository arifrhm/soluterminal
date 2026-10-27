import { act, fireEvent, render, screen } from '@testing-library/react';
import { describe, expect, it, vi } from 'vitest';
import { TopbarPopover } from './TopbarPopover';

function renderPopover(onOpenChange?: (open: boolean) => void) {
  const innerClick = vi.fn();
  render(
    <TopbarPopover
      onOpenChange={onOpenChange}
      trigger={({ toggle }) => <button onClick={toggle}>Toggle panel</button>}
    >
      <button onClick={innerClick}>Inner action</button>
    </TopbarPopover>,
  );
  return { innerClick };
}

describe('TopbarPopover', () => {
  it('opens from its trigger and closes on an outside click', () => {
    renderPopover();
    fireEvent.click(screen.getByRole('button', { name: 'Toggle panel' }));
    expect(screen.getByText('Inner action')).toBeInTheDocument();
    fireEvent.click(document.body);
    expect(screen.queryByText('Inner action')).toBeNull();
  });

  it('uses document bubble so a body handler can suppress dismissal', () => {
    renderPopover();
    fireEvent.click(screen.getByRole('button', { name: 'Toggle panel' }));
    const stopAtBody = (event: Event) => event.stopPropagation();
    document.body.addEventListener('click', stopAtBody);
    fireEvent.click(document.body);
    document.body.removeEventListener('click', stopAtBody);
    expect(screen.getByText('Inner action')).toBeInTheDocument();
  });

  it('stays open for clicks inside the portal panel', () => {
    const { innerClick } = renderPopover();
    fireEvent.click(screen.getByRole('button', { name: 'Toggle panel' }));
    fireEvent.click(screen.getByRole('button', { name: 'Inner action' }));
    expect(innerClick).toHaveBeenCalledOnce();
    expect(screen.getByText('Inner action')).toBeInTheDocument();
  });

  it('closes on Escape without stopping propagation', () => {
    renderPopover();
    fireEvent.click(screen.getByRole('button', { name: 'Toggle panel' }));
    const downstream = vi.fn();
    document.addEventListener('keydown', downstream);
    fireEvent.keyDown(document.body, { key: 'Escape' });
    document.removeEventListener('keydown', downstream);
    expect(screen.queryByText('Inner action')).toBeNull();
    expect(downstream).toHaveBeenCalledOnce();
  });

  it('delivers an inner click and supports the synthetic body-click close pattern', () => {
    const { innerClick } = renderPopover();
    fireEvent.click(screen.getByRole('button', { name: 'Toggle panel' }));
    const button = screen.getByRole('button', { name: 'Inner action' });
    fireEvent.mouseDown(button);
    fireEvent.mouseUp(button);
    fireEvent.click(button);
    expect(innerClick).toHaveBeenCalledOnce();
    expect(screen.getByText('Inner action')).toBeInTheDocument();

    act(() => {
      document.body.dispatchEvent(new MouseEvent('click', { bubbles: true }));
    });
    expect(screen.queryByText('Inner action')).toBeNull();
  });

  it('reports both opening and closing through onOpenChange', () => {
    const onOpenChange = vi.fn();
    renderPopover(onOpenChange);
    onOpenChange.mockClear();
    fireEvent.click(screen.getByRole('button', { name: 'Toggle panel' }));
    fireEvent.click(document.body);
    expect(onOpenChange.mock.calls).toEqual([[true], [false]]);
  });
});
