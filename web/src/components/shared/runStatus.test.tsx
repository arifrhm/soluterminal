import { render } from '@testing-library/react';
import { describe, expect, it } from 'vitest';
import {
  agentToneVisual,
  RunStatusDot,
  RunStatusGlyph,
  runVisualColor,
  scheduleRunVisual,
  taskStatusVisual,
} from './runStatus';
import type { RunVisual } from './runStatus';

describe('run-status vocabulary', () => {
  it.each([
    ['fired', 'ok'],
    ['skipped', 'paused'],
    ['error', 'error'],
    ['quota_paused', 'paused'],
    ['credential_paused', 'attention'],
    [undefined, 'neutral'],
    ['bogus', 'neutral'],
  ] as const)('maps schedule status %s to %s', (status, visual) => {
    expect(scheduleRunVisual(status)).toBe(visual);
  });

  it.each([
    ['ok', 'ok'],
    ['warn', 'attention'],
    ['err', 'error'],
    ['off', 'paused'],
    ['idle', 'neutral'],
  ] as const)('maps agent tone %s to %s', (tone, visual) => {
    expect(agentToneVisual(tone)).toBe(visual);
  });

  it.each([
    ['completed', 'ok'],
    ['running', 'running'],
    ['awaiting_approval', 'attention'],
    ['needs_merge', 'attention'],
    ['failed', 'error'],
    ['paused_for_quota', 'paused'],
    ['paused_for_budget', 'paused'],
    ['pending', 'queued'],
    ['cancelled', 'neutral'],
    ['superseded', 'neutral'],
    ['future_state', 'neutral'],
  ] as const)('maps task status %s to %s', (status, visual) => {
    expect(taskStatusVisual(status)).toBe(visual);
  });

  it.each([
    ['ok', 'var(--ok)'],
    ['running', 'var(--accent)'],
    ['attention', 'var(--warn)'],
    ['error', 'var(--danger)'],
    ['paused', 'var(--fg-dim)'],
    ['queued', 'var(--fg-dim)'],
    ['neutral', 'var(--fg-dim)'],
  ] as const)('uses the shared theme color for %s', (visual, color) => {
    expect(runVisualColor(visual)).toBe(color);
  });
});

describe('RunStatusGlyph', () => {
  it('draws a distinct svg for every visual kind', () => {
    const visuals: RunVisual[] = ['ok', 'running', 'attention', 'error', 'paused', 'queued', 'neutral'];
    const markup = visuals.map((visual) => {
      const { container, unmount } = render(<RunStatusGlyph visual={visual} />);
      const svg = container.querySelector('svg');
      expect(svg).not.toBeNull();
      const value = svg?.outerHTML;
      unmount();
      return value;
    });
    expect(new Set(markup).size).toBe(visuals.length);
  });

  it('sets the shared color by default and can inherit the host color', () => {
    const colored = render(<RunStatusGlyph visual="ok" />).container.firstElementChild as HTMLElement;
    expect(colored.style.color).toBe('var(--ok)');

    const inherited = render(<RunStatusGlyph visual="ok" colored={false} />).container.firstElementChild as HTMLElement;
    expect(inherited.style.color).toBe('');
  });
});

describe('RunStatusDot', () => {
  it('renders the visual color and title', () => {
    const dot = render(<RunStatusDot visual="error" title="Run failed" />).container.firstElementChild as HTMLElement;
    expect(dot.tagName).toBe('SPAN');
    expect(dot.style.background).toBe(runVisualColor('error'));
    expect(dot).toHaveAttribute('title', 'Run failed');
  });
});
