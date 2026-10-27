import { fireEvent, render, screen } from '@testing-library/react';
import { useRef } from 'react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { useDismiss, useOutsideClick } from './useDismiss';

function OutsideHarness({
  onOutside,
  rawInside,
  enabled = true,
  event,
  ignoreSelectors,
  capture,
  target,
}: {
  onOutside: () => void;
  rawInside?: Element;
  enabled?: boolean;
  event?: 'click' | 'mousedown';
  ignoreSelectors?: string[];
  capture?: boolean;
  target?: 'window' | 'document';
}) {
  const panelRef = useRef<HTMLDivElement | null>(null);
  useOutsideClick(onOutside, {
    enabled,
    event,
    capture,
    target,
    inside: [panelRef, null, rawInside, undefined],
    ignoreSelectors,
  });
  return <div ref={panelRef} data-testid="panel" />;
}

afterEach(() => {
  vi.restoreAllMocks();
});

describe('useOutsideClick', () => {
  it('defaults to click and can explicitly preserve mousedown semantics', () => {
    const onClickOutside = vi.fn();
    const clickView = render(<OutsideHarness onOutside={onClickOutside} />);
    fireEvent.mouseDown(document.body);
    expect(onClickOutside).not.toHaveBeenCalled();
    fireEvent.click(document.body);
    expect(onClickOutside).toHaveBeenCalledOnce();
    clickView.unmount();

    const onMouseDownOutside = vi.fn();
    render(<OutsideHarness onOutside={onMouseDownOutside} event="mousedown" />);
    fireEvent.click(document.body);
    expect(onMouseDownOutside).not.toHaveBeenCalled();
    fireEvent.mouseDown(document.body);
    expect(onMouseDownOutside).toHaveBeenCalledOnce();
  });

  it('registers only while enabled', () => {
    const onOutside = vi.fn();
    const view = render(<OutsideHarness onOutside={onOutside} enabled={false} />);
    fireEvent.click(document.body);
    expect(onOutside).not.toHaveBeenCalled();

    view.rerender(<OutsideHarness onOutside={onOutside} enabled />);
    fireEvent.click(document.body);
    expect(onOutside).toHaveBeenCalledOnce();
  });

  it('ignores targets beneath configured selectors', () => {
    const onOutside = vi.fn();
    const backdrop = document.createElement('div');
    backdrop.className = 'app-dialog-backdrop';
    const child = document.createElement('button');
    backdrop.appendChild(child);
    document.body.appendChild(backdrop);
    render(
      <OutsideHarness onOutside={onOutside} ignoreSelectors={['.app-dialog-backdrop']} />,
    );

    fireEvent.click(child);
    expect(onOutside).not.toHaveBeenCalled();
    backdrop.remove();
  });

  it('ignores text-node targets beneath configured selectors', () => {
    const onOutside = vi.fn();
    const backdrop = document.createElement('div');
    backdrop.className = 'app-dialog-backdrop';
    const text = document.createTextNode('dialog copy');
    backdrop.appendChild(text);
    document.body.appendChild(backdrop);
    render(
      <OutsideHarness onOutside={onOutside} ignoreSelectors={['.app-dialog-backdrop']} />,
    );

    text.dispatchEvent(new MouseEvent('click', { bubbles: true }));
    expect(onOutside).not.toHaveBeenCalled();
    backdrop.remove();
  });

  it('registers on the configured event target with the configured capture phase', () => {
    const windowAdd = vi.spyOn(window, 'addEventListener');
    const documentAdd = vi.spyOn(document, 'addEventListener');
    const first = render(<OutsideHarness onOutside={vi.fn()} />);
    expect(windowAdd).toHaveBeenCalledWith('click', expect.any(Function), true);
    expect(documentAdd).not.toHaveBeenCalledWith('click', expect.any(Function), expect.anything());
    first.unmount();
    windowAdd.mockClear();
    documentAdd.mockClear();

    render(
      <OutsideHarness onOutside={vi.fn()} target="document" capture={false} />,
    );
    expect(documentAdd).toHaveBeenCalledWith('click', expect.any(Function), false);
    expect(windowAdd).not.toHaveBeenCalledWith('click', expect.any(Function), expect.anything());
  });

  it('treats both refs and raw elements as inside while dismissing outside clicks', () => {
    const onOutside = vi.fn();
    const rawInside = document.createElement('div');
    const rawChild = document.createElement('button');
    rawInside.appendChild(rawChild);
    document.body.appendChild(rawInside);
    render(<OutsideHarness onOutside={onOutside} rawInside={rawInside} />);

    fireEvent.click(screen.getByTestId('panel'));
    fireEvent.click(rawChild);
    expect(onOutside).not.toHaveBeenCalled();
    fireEvent.click(document.body);
    expect(onOutside).toHaveBeenCalledOnce();
    rawInside.remove();
  });
});

describe('useDismiss', () => {
  it('can disable Escape independently', () => {
    const onDismiss = vi.fn();
    function Harness() {
      const ref = useRef<HTMLDivElement | null>(null);
      useDismiss(onDismiss, { inside: [ref], escape: false });
      return <div ref={ref} />;
    }
    render(<Harness />);
    fireEvent.keyDown(document.body, { key: 'Escape' });
    expect(onDismiss).not.toHaveBeenCalled();
  });

  it('registers Escape only while enabled', () => {
    const onDismiss = vi.fn();
    function Harness({ enabled }: { enabled: boolean }) {
      const ref = useRef<HTMLDivElement | null>(null);
      useDismiss(onDismiss, { inside: [ref], enabled });
      return <div ref={ref} />;
    }
    const view = render(<Harness enabled={false} />);
    fireEvent.keyDown(document.body, { key: 'Escape' });
    expect(onDismiss).not.toHaveBeenCalled();

    view.rerender(<Harness enabled />);
    fireEvent.keyDown(document.body, { key: 'Escape' });
    expect(onDismiss).toHaveBeenCalledOnce();
  });

  it.each([
    [true, 0],
    [false, 1],
  ])('sets Escape propagation to %s', (escapeStopPropagation, downstreamCalls) => {
    const onDismiss = vi.fn();
    function Harness() {
      const ref = useRef<HTMLDivElement | null>(null);
      useDismiss(onDismiss, { inside: [ref], escapeStopPropagation });
      return <div ref={ref} />;
    }
    render(<Harness />);
    const downstream = vi.fn();
    document.addEventListener('keydown', downstream);
    fireEvent.keyDown(document.body, { key: 'Escape' });
    document.removeEventListener('keydown', downstream);
    expect(onDismiss).toHaveBeenCalledOnce();
    expect(downstream).toHaveBeenCalledTimes(downstreamCalls);
  });
});
