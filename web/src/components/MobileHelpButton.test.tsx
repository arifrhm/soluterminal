import { render, screen } from '@testing-library/react';
import { describe, expect, it } from 'vitest';
import { Topbar } from './Topbar';
import { AccountFooter } from './sidebar/AccountFooter';

// Help-affordance placement. Post nav-uplift the global controls (including the
// "?" Help menu) live in the desktop topbar utility toolbar;
// the sidebar footer is account-only on desktop and only carries the chrome row
// on mobile. These tests run at jsdom's default desktop width (1024), so
// useIsMobileView resolves to desktop deterministically.

describe('help affordance placement', () => {
  it('renders exactly one Help menu button in the desktop topbar', () => {
    render(<Topbar onOpenHelp={() => {}} />);

    const buttons = screen.getAllByRole('button', { name: /^help$/i });
    expect(buttons).toHaveLength(1);
    expect(buttons[0]).toHaveAttribute('aria-haspopup', 'menu');
  });

  it('keeps the help button out of the account footer on desktop (it lives in the topbar)', () => {
    render(<AccountFooter onOpenHelp={() => {}} />);

    expect(screen.queryByRole('button', { name: /keyboard shortcuts/i })).toBeNull();
  });
});
