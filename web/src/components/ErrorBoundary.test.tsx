import { fireEvent, render, screen } from '@testing-library/react';
import type { ReactElement } from 'react';
import { describe, expect, it, vi } from 'vitest';
import { ErrorBoundary } from './ErrorBoundary';

function Crash(): ReactElement {
  throw new Error('xterm mount failed');
}

describe('ErrorBoundary', () => {
  it('renders a tokenized recovery state and can retry rendering children', () => {
    const spy = vi.spyOn(console, 'error').mockImplementation(() => {});
    let shouldCrash = true;
    const Child = () => (shouldCrash ? <Crash /> : <div>Recovered panel</div>);

    render(
      <ErrorBoundary label="test">
        <Child />
      </ErrorBoundary>,
    );

    expect(screen.getByRole('alert')).toHaveClass('error-boundary-state');
    expect(screen.getByText('Panel failed to render')).toBeInTheDocument();
    expect(screen.getByText('xterm mount failed')).toBeInTheDocument();

    shouldCrash = false;
    fireEvent.click(screen.getByRole('button', { name: /try again/i }));

    expect(screen.getByText('Recovered panel')).toBeInTheDocument();
    spy.mockRestore();
  });
});
