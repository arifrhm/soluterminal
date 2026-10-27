import { render } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';

import { Topbar } from './Topbar';

describe('Topbar mounted-preview assets', () => {
  afterEach(() => vi.unstubAllEnvs());

  it('routes its boot-visible app icon through the mounted asset resolver', () => {
    vi.stubEnv('BASE_URL', './');
    const { container } = render(<Topbar />);
    const icon = container.querySelector<HTMLImageElement>('.topbar-app-icon');

    expect(icon?.getAttribute('src')).toBe('./icon-mark-192.png');
  });
});
