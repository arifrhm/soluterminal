import { render, screen } from '@testing-library/react';
import { describe, expect, it, vi } from 'vitest';
import { FallbackRouteAuthorizations } from './FallbackRouteAuthorizations';

describe('FallbackRouteAuthorizations', () => {
  it('keeps cache TTL variants as distinct exact routes', () => {
    render(<FallbackRouteAuthorizations
      role="executor"
      chain={[
        { runtime: 'claude-headless', model: 'sonnet', effort: 'high' },
        { runtime: 'claude-headless', model: 'sonnet', effort: 'high', cacheTtl: '1h' },
      ]}
      routes={[]}
      onChange={vi.fn()}
      active
    />);

    expect(screen.getByRole('group', { name: 'Bounded route permissions' })).toBeInTheDocument();
    expect(screen.getByText(/sonnet .* claude-headless .* high .* auto .* sonnet .* claude-headless .* high .* 1h/i)).toBeInTheDocument();
  });
});
