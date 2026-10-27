import { cleanup, fireEvent, render, screen } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { Health, ReleaseReadiness } from '../types';
import { ReleaseChip } from './StatusBar';

const now = new Date('2026-08-10T12:00:00Z');

function readiness(overrides: Partial<ReleaseReadiness> = {}): ReleaseReadiness {
  return {
    state: 'ok',
    lastReleaseVersion: '0.0.989',
    lastReleaseAnchor: 'abc123',
    anchorSource: 'bump-commit',
    anchorProvenance: 'inferred',
    unreleasedTotal: 5,
    functionalCount: 3,
    docsOnlyCount: 2,
    functionalPaths: ['daemon', 'web'],
    suggestedNextVersion: '0.0.990',
    oldestUnreleasedAt: '2026-08-09T10:00:00Z',
    runningVersion: '0.0.986',
    dirty: false,
    computedAt: '2026-08-10T11:59:00Z',
    ...overrides,
  };
}

function health(snapshot?: ReleaseReadiness): Health {
  return {
    status: 'ok', version: '0.0.986', uptimeSec: 1, panes: 0,
    ...(snapshot ? { releaseReadiness: snapshot } : {}),
  };
}

describe('ReleaseChip', () => {
  beforeEach(() => {
    vi.useFakeTimers();
    vi.setSystemTime(now);
    localStorage.clear();
  });
  afterEach(() => {
    cleanup();
    vi.useRealTimers();
    vi.restoreAllMocks();
  });

  it('is hidden without an actionable functional delta', () => {
    const { container, rerender } = render(<ReleaseChip health={health()} />);
    expect(container).toBeEmptyDOMElement();
    rerender(<ReleaseChip health={health(readiness({ state: 'unknown' }))} />);
    expect(container).toBeEmptyDOMElement();
    rerender(<ReleaseChip health={health(readiness({ functionalCount: 0 }))} />);
    expect(container).toBeEmptyDOMElement();
  });

  it('shows the commit delta and warns strictly after 24 hours', () => {
    const { rerender } = render(<ReleaseChip health={health(readiness())} />);
    const old = screen.getByText('release +5').closest('button');
    expect(old).toHaveClass('warn');

    rerender(<ReleaseChip health={health(readiness({ oldestUnreleasedAt: '2026-08-09T12:00:00Z' }))} />);
    expect(screen.getByText('release +5').closest('button')).not.toHaveClass('warn');
    rerender(<ReleaseChip health={health(readiness({ oldestUnreleasedAt: '2026-08-09T12:00:01Z' }))} />);
    expect(screen.getByText('release +5').closest('button')).not.toHaveClass('warn');
    rerender(<ReleaseChip health={health(readiness({ oldestUnreleasedAt: '' }))} />);
    expect(screen.getByText('release +5').closest('button')).not.toHaveClass('warn');
  });

  it('opens the informational activation sequence with only a read-only scope check', () => {
    const fetchSpy = vi.spyOn(globalThis, 'fetch').mockResolvedValue(new Response(JSON.stringify({
      allowed: true, cut: null, blockers: [], waived: [], included: [], warnings: [],
    }), { status: 200, headers: { 'content-type': 'application/json' } }));
    render(<ReleaseChip health={health(readiness())} />);
    fireEvent.click(screen.getByText('release +5').closest('button')!);
    expect(screen.getByRole('dialog', { name: 'Release readiness' })).toBeInTheDocument();
    expect(screen.getByText(/5 unreleased commits since v0\.0\.989/)).toBeInTheDocument();
    expect(screen.getByText(/3 functional files \+ 2 documentation files changed/)).toBeInTheDocument();
    expect(screen.getAllByText(/v0\.0\.990/).length).toBeGreaterThan(0);
    expect(screen.getByText(/bumps to v0\.0\.990 and freezes main/)).toBeInTheDocument();
    expect(screen.getByText(/tried on Preview/)).toBeInTheDocument();
    // The retired engine's steps and its empty "cut" row are gone.
    expect(screen.queryByText(/Declare the release cut/)).toBeNull();
    expect(screen.queryByText(/implicit \/ not declared/)).toBeNull();
    expect(screen.getByText('./publish.sh')).toBeInTheDocument();
    // Opening the popover reads two things and writes nothing: the release
    // scope check and the (opt-in, default-off) release-train status. The
    // guarantee this test exists for is that NOTHING here mutates — so pin the
    // exact call list and assert every request is a GET.
    expect(fetchSpy).toHaveBeenCalledTimes(2);
    const urls = fetchSpy.mock.calls.map((call) => String(call[0]));
    expect(urls[0]).toContain('/api/workflow/release-check');
    expect(urls.filter((url) => url.includes('/api/workflow/release-train'))).toHaveLength(1);
    // Refresh is preview-only on the daemon (it never declares/departs) and
    // repeated reads are coalesced by the preview TTL.
    const trainUrl = urls.find((url) => url.includes('/api/workflow/release-train'));
    expect(trainUrl).toContain('refresh=1');
    for (const [, init] of fetchSpy.mock.calls) {
      expect(String(init?.method ?? 'GET').toUpperCase()).toBe('GET');
    }
  });
});
