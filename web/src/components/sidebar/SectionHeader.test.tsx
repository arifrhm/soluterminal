import { fireEvent, render, screen } from '@testing-library/react';
import { describe, expect, it, vi } from 'vitest';
import { SectionHeader } from './SectionHeader';

describe('SectionHeader', () => {
  it('renders the toggle as an accessible expanded/collapsed control', () => {
    const onToggle = vi.fn();
    render(
      <SectionHeader
        title="Workspaces"
        subtitle="Saved layouts"
        count={2}
        collapsed={false}
        onToggle={onToggle}
      />,
    );

    const toggle = screen.getByRole('button', { name: /workspaces/i });
    expect(toggle).toHaveAttribute('aria-expanded', 'true');

    fireEvent.click(toggle);
    expect(onToggle).toHaveBeenCalledTimes(1);
  });

  it('does not route extra action clicks through the section toggle', () => {
    const onToggle = vi.fn();
    const onExtra = vi.fn();
    render(
      <SectionHeader
        title="Schedules"
        collapsed={true}
        onToggle={onToggle}
        extra={<button onClick={onExtra}>Add</button>}
      />,
    );

    expect(screen.getByRole('button', { name: /schedules/i })).toHaveAttribute('aria-expanded', 'false');

    fireEvent.click(screen.getByRole('button', { name: 'Add' }));
    expect(onExtra).toHaveBeenCalledTimes(1);
    expect(onToggle).not.toHaveBeenCalled();
  });
});
