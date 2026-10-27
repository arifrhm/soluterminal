import { fireEvent, render, screen } from '@testing-library/react';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { useEntitlements } from './entitlements';
import { setExtensionsForTests } from './extensions';
import {
  EMPTY_WORKSPACE_COPY,
  EmptyWorkspaceState,
  WorkspaceLoadingState,
} from './App';

describe('workspace first-run states', () => {
  beforeEach(() => {
    useEntitlements.setState({ loaded: true, team: true, operator: true });
  });

  it('describes daemon-backed loading without the placeholder copy', () => {
    render(<WorkspaceLoadingState />);

    expect(screen.getByRole('status')).toHaveAttribute('aria-busy', 'true');
    expect(screen.getByText('Loading sessions')).toBeInTheDocument();
    expect(screen.queryByText(/loading…/i)).toBeNull();
  });

  it('offers Claude Code, Codex and a terminal with plain copy', () => {
    const onCreateClaude = vi.fn();
    const onCreateCodex = vi.fn();
    const onCreateShell = vi.fn();
    useEntitlements.setState({ loaded: true, team: false, operator: false });
    const props = {
      hosts: null,
      onCreateShell,
      onCreateClaude,
      onCreateCodex,
      onCreateOrchestrator: vi.fn(),
      onCreateOrchestratorCodex: vi.fn(),
      onOpenDialog: vi.fn(),
    };
    const { rerender } = render(<EmptyWorkspaceState creating={false} {...props} />);
    const names = screen.getAllByRole('button').map((b) => b.textContent);
    expect(names.slice(0, 3)).toEqual(['Start Claude Code', 'Start Codex', 'Open a terminal']);
    expect(screen.getByText(EMPTY_WORKSPACE_COPY)).toBeInTheDocument();
    expect(EMPTY_WORKSPACE_COPY).toBe('Start an agent, or open a terminal. Sessions keep running when you close the window.');
    expect(screen.queryByText(/Alt\+/)).toBeNull();
    fireEvent.click(screen.getByRole('button', { name: 'Start Codex' }));
    expect(onCreateCodex).toHaveBeenCalledTimes(1);

    // Only the button that was clicked says "Starting…".
    rerender(<EmptyWorkspaceState creating creatingKind="codex" {...props} />);
    expect(screen.getByRole('button', { name: 'Starting…' })).toBeDisabled();
    expect(screen.getByRole('button', { name: 'Start Claude Code' })).toBeDisabled();
  });

  it('offers no Orchestrator when no module provides the role', () => {
    setExtensionsForTests([]);
    try {
      render(
        <EmptyWorkspaceState
          creating={false}
          hosts={[{ agent: 'codex', label: 'Codex', supported: true, installed: true, available: true }]}
          onCreateShell={vi.fn()}
          onCreateClaude={vi.fn()}
          onCreateOrchestrator={vi.fn()}
          onCreateOrchestratorCodex={vi.fn()}
          onOpenDialog={vi.fn()}
        />,
      );
      expect(screen.getByRole('button', { name: /^start claude code$/i })).toBeInTheDocument();
      expect(screen.queryByRole('button', { name: /orchestrator/i })).toBeNull();
    } finally {
      setExtensionsForTests(null);
    }
  });

  it('hides the Orchestrator presets until a team install is confirmed', () => {
    for (const verdict of [{ loaded: false, team: false, operator: false }, { loaded: true, team: false, operator: false }]) {
      useEntitlements.setState(verdict);
      const { unmount } = render(
        <EmptyWorkspaceState
          creating={false}
          hosts={[{ agent: 'codex', label: 'Codex', supported: true, installed: true, available: true }]}
          onCreateShell={vi.fn()}
          onCreateClaude={vi.fn()}
          onCreateOrchestrator={vi.fn()}
          onCreateOrchestratorCodex={vi.fn()}
          onOpenDialog={vi.fn()}
        />,
      );
      expect(screen.getByRole('button', { name: /open a terminal/i })).toBeInTheDocument();
      expect(screen.queryByRole('button', { name: /orchestrator/i })).toBeNull();
      expect(screen.queryByText(/One pane that investigates, builds, and coordinates other panes/)).toBeNull();
      unmount();
    }
  });
});
