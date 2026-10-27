import { fireEvent, render, screen } from '@testing-library/react';
import { useState } from 'react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { FooterPopover } from './FooterPopover';

const anchors: HTMLElement[] = [];

function makeAnchor() {
  const anchor = document.createElement('button');
  document.body.appendChild(anchor);
  anchors.push(anchor);
  return anchor;
}

afterEach(() => {
  anchors.splice(0).forEach((anchor) => anchor.remove());
});

function renderPopover(onClose = vi.fn()) {
  const anchor = makeAnchor();
  render(
    <FooterPopover anchor={anchor} onClose={onClose} ariaLabel="Footer tools">
      <button type="button">Portal action</button>
    </FooterPopover>,
  );
  return { anchor, onClose };
}

describe('FooterPopover', () => {
  it('closes on outside click but not inside or anchor clicks', () => {
    const { anchor, onClose } = renderPopover();
    fireEvent.click(screen.getByRole('dialog'));
    fireEvent.click(anchor);
    expect(onClose).not.toHaveBeenCalled();
    fireEvent.click(document.body);
    expect(onClose).toHaveBeenCalledOnce();
  });

  it('uses window capture so document capture cannot suppress dismissal', () => {
    const { onClose } = renderPopover();
    const stopAtDocument = (event: Event) => event.stopPropagation();
    document.addEventListener('click', stopAtDocument, true);
    fireEvent.click(document.body);
    document.removeEventListener('click', stopAtDocument, true);
    expect(onClose).toHaveBeenCalledOnce();
  });

  it('does not close for clicks inside an app dialog backdrop', () => {
    const { onClose } = renderPopover();
    const backdrop = document.createElement('div');
    backdrop.className = 'app-dialog-backdrop';
    const child = document.createElement('button');
    backdrop.appendChild(child);
    document.body.appendChild(backdrop);
    fireEvent.click(child);
    expect(onClose).not.toHaveBeenCalled();
    backdrop.remove();
  });

  it('closes on Escape and stops propagation', () => {
    const { onClose } = renderPopover();
    const downstream = vi.fn();
    document.addEventListener('keydown', downstream);
    fireEvent.keyDown(document.body, { key: 'Escape' });
    document.removeEventListener('keydown', downstream);
    expect(onClose).toHaveBeenCalledOnce();
    expect(downstream).not.toHaveBeenCalled();
  });

  it('delivers an inner button click before that click closes the popover', () => {
    const clicked = vi.fn();
    function StatefulPopover() {
      const [open, setOpen] = useState(true);
      const [anchor] = useState(makeAnchor);
      return open ? (
        <FooterPopover anchor={anchor} onClose={() => setOpen(false)} ariaLabel="Footer tools">
          <button
            type="button"
            onClick={() => {
              clicked();
              setOpen(false);
            }}
          >
            Insert
          </button>
        </FooterPopover>
      ) : null;
    }
    render(<StatefulPopover />);
    const button = screen.getByRole('button', { name: 'Insert' });
    fireEvent.mouseDown(button);
    fireEvent.mouseUp(button);
    fireEvent.click(button);
    expect(clicked).toHaveBeenCalledOnce();
    expect(screen.queryByRole('dialog')).toBeNull();
  });
});
