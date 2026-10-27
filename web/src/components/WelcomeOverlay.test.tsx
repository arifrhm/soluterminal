import { fireEvent, render, screen } from '@testing-library/react';
import { describe, expect, it } from 'vitest';
import { useEntitlements } from '../entitlements';
import { WelcomeOverlay } from './WelcomeOverlay';

function tourText(): string {
  let text = '';
  for (let i = 0; i < 3; i++) {
    text += screen.getByRole('dialog').textContent ?? '';
    const next = screen.queryByRole('button', { name: 'Next' });
    if (next) fireEvent.click(next);
  }
  return text;
}

describe('WelcomeOverlay', () => {
  it('keeps phone, Orchestrator and ARIA out of the no-account tour', () => {
    useEntitlements.setState({ loaded: true, team: false, operator: false, account: false });
    render(<WelcomeOverlay open onClose={() => {}} />);
    const text = tourText();
    expect(text).not.toMatch(/phone/i);
    expect(text).not.toMatch(/orchestrator/i);
    expect(text).not.toMatch(/ARIA/);
  });

  it('mentions the phone but not the Orchestrator for a free account', () => {
    useEntitlements.setState({ loaded: true, team: false, operator: false, account: true });
    render(<WelcomeOverlay open onClose={() => {}} />);
    const text = tourText();
    expect(text).toMatch(/your phone/);
    expect(text).not.toMatch(/orchestrator/i);
  });

  it('still mentions phone and the Orchestrator for a team install, without ARIA', () => {
    useEntitlements.setState({ loaded: true, team: true, operator: true });
    render(<WelcomeOverlay open onClose={() => {}} />);
    const text = tourText();
    expect(text).toMatch(/your phone/);
    expect(text).toMatch(/Orchestrator pane/);
    expect(text).not.toMatch(/ARIA/);
  });

  it('makes step 2 about starting an agent, not shortcuts, in three steps or fewer', () => {
    useEntitlements.setState({ loaded: true, team: false, operator: false, account: false });
    render(<WelcomeOverlay open onClose={() => {}} />);
    expect(screen.getByText(/1 \/ [1-3]$/)).toBeInTheDocument();
    expect(screen.getByRole('dialog').textContent).not.toMatch(/Codex/);
    fireEvent.click(screen.getByRole('button', { name: 'Next' }));
    expect(screen.getByRole('dialog', { name: 'Start your first agent' })).toBeInTheDocument();
    expect(screen.getByText('Start Claude Code')).toBeInTheDocument();
    expect(screen.getByRole('dialog').querySelector('kbd')).toBeNull();
    fireEvent.click(screen.getByRole('button', { name: 'Next' }));
    expect(screen.getByRole('dialog').textContent).not.toMatch(/Restarting/);
  });

  it('keeps new-user jargon out of the tour', () => {
    useEntitlements.setState({ loaded: true, team: false, operator: false, account: true });
    render(<WelcomeOverlay open onClose={() => {}} />);
    expect(tourText()).not.toMatch(/daemon|solud|MCP|\bhost\b|distro/i);
  });

  it('ends with what leaves this computer and a privacy link', () => {
    useEntitlements.setState({ loaded: true, team: false, operator: false, account: false });
    render(<WelcomeOverlay open onClose={() => {}} />);
    fireEvent.click(screen.getByRole('button', { name: 'Next' }));
    expect(screen.queryByText('What leaves this computer')).toBeNull();
    fireEvent.click(screen.getByRole('button', { name: 'Next' }));
    expect(screen.getByText('What leaves this computer')).toBeInTheDocument();
    expect(screen.getByText(/anonymous daily check-in/)).toBeInTheDocument();
    expect(screen.getByRole('link', { name: 'Privacy policy' })).toHaveAttribute(
      'href',
      'https://soluterminal.com/privacy/',
    );
  });

  it('tells a free account what voice and the relay send', () => {
    useEntitlements.setState({ loaded: true, team: false, operator: false, account: true });
    render(<WelcomeOverlay open onClose={() => {}} />);
    fireEvent.click(screen.getByRole('button', { name: 'Next' }));
    fireEvent.click(screen.getByRole('button', { name: 'Next' }));
    expect(screen.getByText(/last few lines of terminal text/)).toBeInTheDocument();
    expect(screen.getByText(/through our relay/)).toBeInTheDocument();
  });
});
