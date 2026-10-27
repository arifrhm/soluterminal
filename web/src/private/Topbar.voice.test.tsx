// The top-bar dictation mic (voice).
import { render, screen } from '@testing-library/react';
import { afterEach, describe, expect, it } from 'vitest';
import { Topbar } from '../components/Topbar';
import { useEntitlements } from '../entitlements';
import '../loadExtensions';

afterEach(() => {
  useEntitlements.setState({ loaded: false, team: false, operator: false, account: false });
});

describe('Topbar dictation mic', () => {
  it('shows the dictation mic but not Task Owners for a free account', () => {
    useEntitlements.setState({ loaded: true, team: false, operator: false, account: true });
    const { container } = render(<Topbar onOpenHelp={() => {}} />);
    expect(container.querySelector('.voice-btn')).not.toBeNull();
    expect(screen.queryByRole('button', { name: /Task Owners/ })).toBeNull();
  });


  it('shows the dictation mic for a team install', () => {
    useEntitlements.setState({ loaded: true, team: true, operator: true });
    const { container } = render(<Topbar onOpenHelp={() => {}} />);
    expect(container.querySelector('.voice-btn')).not.toBeNull();
    expect(screen.queryByRole('button', { name: /Task Owners/ })).toBeNull();
  });
});
