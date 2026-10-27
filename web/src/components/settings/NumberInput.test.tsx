import { useEffect, useState } from 'react';
import { fireEvent, render, screen } from '@testing-library/react';
import { describe, expect, it, vi } from 'vitest';
import { NumberInput } from './NumberInput';

const props = {
  label: 'Escalation max wait (minutes)',
  min: 1,
  max: 240,
  placeholder: '10',
};

function DeferredNumberInput({ onCommit }: { onCommit: (value: number) => void }) {
  const [visible, setVisible] = useState(false);
  useEffect(() => {
    void Promise.resolve().then(() => setVisible(true));
  }, []);
  return visible ? <NumberInput {...props} value={10} onCommit={onCommit} /> : null;
}

function observeInput(): Promise<HTMLInputElement> {
  return new Promise((resolve) => {
    const find = () => {
      const input = document.querySelector<HTMLInputElement>('input[aria-label="Escalation max wait (minutes)"]');
      if (!input) return false;
      observer.disconnect();
      resolve(input);
      return true;
    };
    const observer = new MutationObserver(find);
    observer.observe(document.body, { childList: true, subtree: true });
    queueMicrotask(find);
  });
}

describe('NumberInput', () => {
  it('keeps a value typed immediately after the input appears (mount-time sync must not revert it)', async () => {
    const onCommit = vi.fn();
    const actEnvironment = globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT?: boolean };
    const previousActEnvironment = actEnvironment.IS_REACT_ACT_ENVIRONMENT;
    actEnvironment.IS_REACT_ACT_ENVIRONMENT = false;
    try {
      render(<DeferredNumberInput onCommit={onCommit} />);
      const input = await observeInput();

      fireEvent.change(input, { target: { value: '25' } });
      expect(input).toHaveValue(25);
      fireEvent.blur(input);
      expect(onCommit).toHaveBeenCalledTimes(1);
      expect(onCommit).toHaveBeenCalledWith(25);
    } finally {
      actEnvironment.IS_REACT_ACT_ENVIRONMENT = previousActEnvironment;
    }
  });

  it('commits Enter and clamps to max', () => {
    const onCommit = vi.fn();
    render(<NumberInput {...props} value={10} onCommit={onCommit} />);
    const input = screen.getByRole('spinbutton', { name: props.label });
    fireEvent.change(input, { target: { value: '999' } });
    fireEvent.keyDown(input, { key: 'Enter' });
    expect(input).toHaveValue(240);
    expect(onCommit).toHaveBeenCalledWith(240);
  });

  it('Escape restores the prop value', () => {
    const onCommit = vi.fn();
    render(<NumberInput {...props} value={10} onCommit={onCommit} />);
    const input = screen.getByRole('spinbutton', { name: props.label });
    fireEvent.change(input, { target: { value: '25' } });
    fireEvent.keyDown(input, { key: 'Escape' });
    expect(input).toHaveValue(10);
    expect(onCommit).not.toHaveBeenCalled();
  });

  it('a prop change resets the draft', () => {
    const onCommit = vi.fn();
    const view = render(<NumberInput {...props} value={10} onCommit={onCommit} />);
    const input = screen.getByRole('spinbutton', { name: props.label });
    fireEvent.change(input, { target: { value: '25' } });
    view.rerender(<NumberInput {...props} value={15} onCommit={onCommit} />);
    expect(input).toHaveValue(15);
    expect(onCommit).not.toHaveBeenCalled();
  });
});
