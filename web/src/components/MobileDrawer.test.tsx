import { fireEvent, render } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { MobileDrawer } from './MobileDrawer';

vi.mock('./Sidebar', () => ({
  Sidebar: () => <nav>Sidebar</nav>,
}));

describe('MobileDrawer', () => {
  afterEach(() => {
    vi.restoreAllMocks();
  });

  it('translates from the capped drawer width while opening', () => {
    vi.spyOn(window, 'innerWidth', 'get').mockReturnValue(800);
    const onOpen = vi.fn();

    const { container } = render(
      <MobileDrawer open={false} onOpen={onOpen} onClose={vi.fn()} />,
    );
    const drawer = container.querySelector('.mobile-drawer') as HTMLElement;

    expect(drawer.style.transform).toBe('translateX(-100%)');

    fireEvent.touchStart(window, { touches: [{ clientX: 0, clientY: 10 }] });
    fireEvent.touchMove(window, { touches: [{ clientX: 100, clientY: 12 }] });

    expect(drawer.style.transform).toBe('translateX(-240px)');

    fireEvent.touchEnd(window);

    expect(onOpen).toHaveBeenCalledTimes(1);
  });
});
