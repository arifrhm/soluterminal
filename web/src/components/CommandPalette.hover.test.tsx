import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { CommandPalette } from './CommandPalette';
import { useEntitlements } from '../entitlements';
import { setExtensionsForTests } from '../extensions';

vi.mock('../api', () => ({
  onRoutingScopeChange: vi.fn(() => () => {}),
  currentRoutingScope: vi.fn(() => ({ prefix: '', backendPrefix: '', key: '|' })),
  api: { health: vi.fn().mockResolvedValue({ version: '1.2.3' }) },
}));


Element.prototype.scrollIntoView = vi.fn();

function renderPalette() {
  const onOpenNewPane = vi.fn();
  render(
    <CommandPalette
      open
      onClose={vi.fn()}
      onOpenHelp={vi.fn()}
      onOpenNewPane={onOpenNewPane}
      onOpenBroadcast={vi.fn()}
      onOpenActivity={vi.fn()}
      onOpenFeedback={vi.fn()}
    />,
  );
  const input = document.querySelector<HTMLInputElement>('.quickopen-input')!;
  return { input, onOpenNewPane };
}

const selectedText = () =>
  screen.getAllByRole('option').find((o) => o.getAttribute('aria-selected') === 'true')?.textContent ?? '';

describe('CommandPalette hover vs keyboard selection', () => {
  beforeEach(() => {
    useEntitlements.setState({ loaded: true, team: false, operator: false });
    // Only the core actions: an optional module's rows would shift the list.
    setExtensionsForTests([]);
  });

  afterEach(() => {
    setExtensionsForTests(null);
    vi.restoreAllMocks();
  });

  it('keeps Enter on the typed best match when rows change under a resting pointer', async () => {
    const { input, onOpenNewPane } = renderPalette();
    fireEvent.change(input, { target: { value: 'N' } });

    // The pointer really moves onto the second row: hover follows it.
    const second = screen.getAllByRole('option')[1];
    fireEvent.mouseEnter(second, { screenX: 300, screenY: 200 });
    fireEvent.mouseMove(second, { screenX: 300, screenY: 200 });
    fireEvent.mouseMove(second, { screenX: 310, screenY: 200 });
    expect(second.getAttribute('aria-selected')).toBe('true');

    // Typing re-ranks the list and the selection goes back to the top match.
    fireEvent.change(input, { target: { value: 'New session' } });
    expect(selectedText()).toMatch(/New session/);

    // The list re-rendered under the still pointer: the browser re-runs hover
    // on whichever row now sits there, at the same position.
    const under = screen.getAllByRole('option').find((o) => /Export focused pane transcript/.test(o.textContent ?? ''))!;
    expect(under).toBeTruthy();
    fireEvent.mouseEnter(under, { screenX: 310, screenY: 200 });
    fireEvent.mouseOver(under, { screenX: 310, screenY: 200 });
    fireEvent.mouseMove(under, { screenX: 310, screenY: 200 });
    expect(selectedText()).toMatch(/New session/);

    fireEvent.keyDown(input, { key: 'Enter' });
    await waitFor(() => expect(onOpenNewPane).toHaveBeenCalledWith(null));
  });

  it('ignores the first pointer report after opening (mouse already resting there)', () => {
    renderPalette();
    const third = screen.getAllByRole('option')[2];
    fireEvent.mouseMove(third, { screenX: 400, screenY: 300 });
    expect(screen.getAllByRole('option')[0].getAttribute('aria-selected')).toBe('true');
    expect(third.getAttribute('aria-selected')).toBe('false');
  });
});
