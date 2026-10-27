import { render, screen, fireEvent, within } from '@testing-library/react';
import { describe, expect, it, vi } from 'vitest';

import { DeltaBadge } from './DeltaBadge';
import { ActivityHeatmap } from './ActivityHeatmap';
import { CumulativeSpendChart } from './CumulativeSpendChart';
import { ModelMixTrendChart } from './ModelMixTrendChart';
import { TeamTrendChart, type TeamTrendSeries } from './TeamTrendChart';
import { MiniSparkline } from './MiniSparkline';
import { LensToggle } from './LensToggle';
import { LeaderboardCard } from './LeaderboardCard';
import {
  MemberComparisonTable,
  type MemberComparisonColumn,
  type MemberComparisonRow,
} from './MemberComparisonTable';
import { ProjectMembersBar, type ProjectMemberDatum } from './ProjectMembersBar';
import { PeriodDigest } from './PeriodDigest';
import { ProviderToggle } from './ProviderToggle';
import { ByProviderCard } from './ByProviderCard';
import { tierMixClass } from './MemberComparisonTable';
import type { ProviderRow } from '../../lib/usage';
import type { PeriodDigest as PeriodDigestData } from '../../lib/usage';

// -------- DeltaBadge --------
describe('DeltaBadge', () => {
  it('renders "new" with no glyph when pct is null', () => {
    const { container } = render(<DeltaBadge pct={null} dir="flat" />);
    expect(screen.getByText('new')).toBeInTheDocument();
    expect(container.querySelector('.usage-delta-new')).toBeTruthy();
  });

  it('colors an up delta good when goodWhen defaults to up', () => {
    const { container } = render(<DeltaBadge pct={12.4} dir="up" />);
    expect(container.querySelector('.usage-delta-good')).toBeTruthy();
    expect(screen.getByText('+12%')).toBeInTheDocument();
    expect(container.textContent).toContain('▲');
  });

  it('colors an up delta bad when goodWhen is down (e.g. cost-per-turn)', () => {
    const { container } = render(<DeltaBadge pct={8} dir="up" goodWhen="down" />);
    expect(container.querySelector('.usage-delta-bad')).toBeTruthy();
  });

  it('renders flat as neutral with a dash and 0%', () => {
    const { container } = render(<DeltaBadge pct={0} dir="flat" />);
    expect(container.querySelector('.usage-delta-flat')).toBeTruthy();
    expect(screen.getByText('0%')).toBeInTheDocument();
    expect(container.textContent).toContain('–');
  });

  it('signs a negative down delta', () => {
    render(<DeltaBadge pct={-4} dir="down" />);
    expect(screen.getByText('-4%')).toBeInTheDocument();
  });
});

// -------- ActivityHeatmap --------
function emptyMatrix(): number[][] {
  return Array.from({ length: 7 }, () => Array.from({ length: 24 }, () => 0));
}

describe('ActivityHeatmap', () => {
  it('renders an empty state when max <= 0', () => {
    const { container } = render(<ActivityHeatmap matrix={emptyMatrix()} max={0} />);
    expect(container.querySelector('.usage-heatmap')).toBeNull();
    expect(screen.getByText(/No activity to chart/i)).toBeInTheDocument();
  });

  it('renders a 7×24 grid of cells with a per-cell title when there is activity', () => {
    const m = emptyMatrix();
    m[1][14] = 5; // Mon 14:00 -> 5 turns
    const { container } = render(<ActivityHeatmap matrix={m} max={5} />);
    const cells = container.querySelectorAll('.usage-heatmap-cell');
    expect(cells.length).toBe(7 * 24);
    expect(container.querySelectorAll('.usage-heatmap-row').length).toBe(7);
    // The hot cell carries the "Mon 14:00 — 5 turns" tooltip.
    const titles = Array.from(container.querySelectorAll('title')).map((t) => t.textContent);
    expect(titles).toContain('Mon 14:00 — 5 turns');
  });

  it('honors a custom metric label in the tooltip', () => {
    const m = emptyMatrix();
    m[0][0] = 3;
    const { container } = render(
      <ActivityHeatmap matrix={m} max={3} metricLabel="output tokens" />,
    );
    const titles = Array.from(container.querySelectorAll('title')).map((t) => t.textContent);
    expect(titles).toContain('Sun 00:00 — 3 output tokens');
  });

  // (item 10) Intensity-scale legend + non-empty cells flagged with data-filled
  // (so a faint border distinguishes low-activity from empty on the dark bg).
  it('renders the less→more intensity legend and marks non-empty cells', () => {
    const m = emptyMatrix();
    m[1][14] = 5;
    const { container } = render(<ActivityHeatmap matrix={m} max={5} />);
    expect(container.querySelector('.usage-heatmap-legend')).toBeTruthy();
    expect(container.querySelector('.usage-heatmap-legend-ramp')).toBeTruthy();
    // exactly one non-empty cell carries data-filled; the rest carry data-empty.
    expect(container.querySelectorAll('.usage-heatmap-cell[data-filled]').length).toBe(1);
    expect(container.querySelectorAll('.usage-heatmap-cell[data-empty]').length).toBe(7 * 24 - 1);
  });
});

// -------- CumulativeSpendChart --------
describe('CumulativeSpendChart', () => {
  it('renders an empty state with no points', () => {
    render(<CumulativeSpendChart points={[]} />);
    expect(screen.getByText(/No spend to chart/i)).toBeInTheDocument();
  });

  it('draws an area + line and per-vertex tooltips', () => {
    const { container } = render(
      <CumulativeSpendChart
        points={[
          { date: '2026-06-01', value: 1 },
          { date: '2026-06-02', value: 3 },
          { date: '2026-06-03', value: 6 },
        ]}
      />,
    );
    expect(container.querySelector('.usage-cumulative-area')).toBeTruthy();
    expect(container.querySelector('.usage-cumulative-line')).toBeTruthy();
    const titles = Array.from(container.querySelectorAll('title')).map((t) => t.textContent);
    expect(titles).toContain('2026-06-03: $6.00 cumulative');
  });
});

// -------- ModelMixTrendChart --------
describe('ModelMixTrendChart', () => {
  const colors = new Map<string, string>([
    ['opus', 'var(--usage-magenta)'],
    ['sonnet', 'var(--usage-violet)'],
  ]);

  it('renders an empty state with no days', () => {
    render(<ModelMixTrendChart days={[]} modelColors={colors} legendModels={['opus']} />);
    expect(screen.getByText(/No model mix to chart/i)).toBeInTheDocument();
  });

  it('renders one band per legend model and a legend', () => {
    const { container } = render(
      <ModelMixTrendChart
        days={[
          { date: '2026-06-01', fractions: { opus: 0.7, sonnet: 0.3 } },
          { date: '2026-06-02', fractions: { opus: 0.5, sonnet: 0.5 } },
        ]}
        modelColors={colors}
        legendModels={['opus', 'sonnet']}
      />,
    );
    expect(container.querySelectorAll('.usage-modelmix-band').length).toBe(2);
    expect(container.querySelectorAll('.usage-daily-legend-item').length).toBe(2);
  });

  // (item 6) Older daemons omit daily by_model → legendModels === []. The chart
  // must show its empty-state, NOT an empty SVG frame.
  it('shows the empty-state (no SVG frame) when legendModels is []', () => {
    const { container } = render(
      <ModelMixTrendChart
        days={[
          { date: '2026-06-01', fractions: {} },
          { date: '2026-06-02', fractions: {} },
        ]}
        modelColors={colors}
        legendModels={[]}
      />,
    );
    expect(screen.getByText(/No model mix to chart/i)).toBeInTheDocument();
    expect(container.querySelector('.usage-modelmix-chart')).toBeNull();
    expect(container.querySelectorAll('.usage-modelmix-band').length).toBe(0);
  });

  it('shows the empty-state when every band is all-zero share', () => {
    const { container } = render(
      <ModelMixTrendChart
        days={[
          { date: '2026-06-01', fractions: { opus: 0, sonnet: 0 } },
          { date: '2026-06-02', fractions: { opus: 0, sonnet: 0 } },
        ]}
        modelColors={colors}
        legendModels={['opus', 'sonnet']}
      />,
    );
    expect(screen.getByText(/No model mix to chart/i)).toBeInTheDocument();
    expect(container.querySelector('.usage-modelmix-chart')).toBeNull();
  });
});

// -------- MiniSparkline --------
describe('MiniSparkline', () => {
  it('renders a flat baseline for empty values', () => {
    const { container } = render(<MiniSparkline values={[]} />);
    const path = container.querySelector('.usage-sparkline-line');
    expect(path).toBeTruthy();
    // Flat line = single horizontal segment (one L command).
    expect((path?.getAttribute('d') ?? '').match(/L/g)?.length).toBe(1);
  });

  it('renders a flat baseline when all values are equal', () => {
    const { container } = render(<MiniSparkline values={[5, 5, 5]} />);
    const d = container.querySelector('.usage-sparkline-line')?.getAttribute('d') ?? '';
    // All y-coords equal -> still a valid multi-point path; just assert it drew.
    expect(d.startsWith('M')).toBe(true);
  });

  it('renders a multi-segment trend for varied values', () => {
    const { container } = render(<MiniSparkline values={[1, 5, 2, 8]} />);
    const d = container.querySelector('.usage-sparkline-line')?.getAttribute('d') ?? '';
    expect((d.match(/L/g)?.length ?? 0)).toBe(3);
  });
});

// -------- LensToggle --------
describe('LensToggle', () => {
  it('marks the active lens and fires onChange on click', () => {
    const onChange = vi.fn();
    render(<LensToggle lens="output" onChange={onChange} />);
    const output = screen.getByRole('button', { name: 'Output' });
    expect(output).toHaveClass('active');
    fireEvent.click(screen.getByRole('button', { name: 'Efficiency' }));
    expect(onChange).toHaveBeenCalledWith('efficiency');
  });

  it('exposes all three lenses', () => {
    render(<LensToggle lens="cost" onChange={() => {}} />);
    expect(screen.getByRole('button', { name: 'Output' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Efficiency' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Cost' })).toHaveClass('active');
  });
});

// -------- LeaderboardCard --------
describe('LeaderboardCard', () => {
  it('renders an empty state with no rows', () => {
    render(<LeaderboardCard title="Top output" rows={[]} />);
    expect(screen.getByText(/No data in this window/i)).toBeInTheDocument();
  });

  it('renders ranked rows and highlights the caller', () => {
    const { container } = render(
      <LeaderboardCard
        title="Top output"
        rows={[
          { label: 'Alice', value: '1.2M', sub: 'tok' },
          { label: 'You', value: '900K', isYou: true },
        ]}
      />,
    );
    expect(screen.getByText('Top output')).toBeInTheDocument();
    expect(screen.getAllByRole('listitem').length).toBe(2);
    expect(container.querySelector('.usage-leaderboard-row.is-you')).toBeTruthy();
    expect(screen.getByText('1.2M')).toBeInTheDocument();
  });

  // (item 5) Honesty caption renders under the title when provided.
  it('renders an honesty caption when provided', () => {
    const { container } = render(
      <LeaderboardCard
        title="Lowest cost / turn"
        caption="lowest API-equivalent cost per turn — reflects model choice, not output quality"
        rows={[{ label: 'Alice', value: '$0.10' }]}
      />,
    );
    const cap = container.querySelector('.usage-leaderboard-caption');
    expect(cap).toBeTruthy();
    expect(cap?.textContent).toMatch(/model choice/i);
  });

  it('omits the caption element when none is given', () => {
    const { container } = render(
      <LeaderboardCard title="Most active" rows={[{ label: 'Alice', value: '400' }]} />,
    );
    expect(container.querySelector('.usage-leaderboard-caption')).toBeNull();
  });
});

// -------- MemberComparisonTable --------
const columns: MemberComparisonColumn[] = [
  { key: 'cost', label: 'Cost', primary: true },
  { key: 'turns', label: 'Turns' },
];

function rowsFixture(): MemberComparisonRow[] {
  return [
    {
      userId: 'u1',
      name: 'Alice',
      isYou: false,
      roles: ['owner'],
      active: true,
      cells: { cost: { display: '$120.00', raw: 120 }, turns: { display: '400', raw: 400 } },
      share: 0.6,
      spark: [1, 3, 2, 5],
    },
    {
      userId: 'u2',
      name: 'Bob',
      isYou: true,
      roles: [],
      active: true,
      cells: { cost: { display: '$80.00', raw: 80 }, turns: { display: '250', raw: 250 } },
      share: 0.4,
      spark: [2, 2, 2],
    },
    {
      userId: 'u3',
      name: 'Carol',
      isYou: false,
      roles: ['member'],
      active: false,
      cells: { cost: { display: '$0.00', raw: 0 }, turns: { display: '0', raw: 0 } },
      share: 0,
      spark: [],
    },
  ];
}

describe('MemberComparisonTable', () => {
  it('shows the sort indicator only on the active column', () => {
    const { container } = render(
      <MemberComparisonTable
        rows={rowsFixture()}
        columns={columns}
        sortKey="cost"
        sortDir="desc"
        onSort={() => {}}
        canDrill={false}
      />,
    );
    const costHdr = screen.getByRole('button', { name: /Cost/ });
    expect(within(costHdr).getByText('▼')).toBeInTheDocument();
    // The non-active column header carries an empty arrow span.
    const turnsHdr = screen.getByRole('button', { name: /Turns/ });
    expect(turnsHdr.querySelector('.usage-compare-arrow')?.textContent).toBe('');
    // The sorted <th> gets the .sorted class.
    expect(container.querySelector('.usage-compare-colhdr.sorted')).toBeTruthy();
  });

  it('reflects asc direction with the up arrow', () => {
    render(
      <MemberComparisonTable
        rows={rowsFixture()}
        columns={columns}
        sortKey="turns"
        sortDir="asc"
        onSort={() => {}}
        canDrill={false}
      />,
    );
    const turnsHdr = screen.getByRole('button', { name: /Turns/ });
    expect(within(turnsHdr).getByText('▲')).toBeInTheDocument();
  });

  it('calls onSort with the column key when a header is clicked', () => {
    const onSort = vi.fn();
    render(
      <MemberComparisonTable
        rows={rowsFixture()}
        columns={columns}
        sortKey="cost"
        sortDir="desc"
        onSort={onSort}
        canDrill={false}
      />,
    );
    fireEvent.click(screen.getByRole('button', { name: /Turns/ }));
    expect(onSort).toHaveBeenCalledWith('turns');
  });

  it('renders inactive members muted (not hidden)', () => {
    const { container } = render(
      <MemberComparisonTable
        rows={rowsFixture()}
        columns={columns}
        sortKey="cost"
        sortDir="desc"
        onSort={() => {}}
        canDrill={false}
      />,
    );
    // Carol (inactive) is still present, with the muted class.
    expect(screen.getByText('Carol')).toBeInTheDocument();
    expect(container.querySelector('.usage-compare-row.is-inactive')).toBeTruthy();
    expect(screen.getAllByText('inactive').length).toBe(1);
  });

  it('marks the caller row and renders a "you" chip', () => {
    const { container } = render(
      <MemberComparisonTable
        rows={rowsFixture()}
        columns={columns}
        sortKey="cost"
        sortDir="desc"
        onSort={() => {}}
        canDrill={false}
      />,
    );
    expect(container.querySelector('.usage-compare-row.is-you')).toBeTruthy();
    expect(screen.getByText('you')).toBeInTheDocument();
  });

  it('renders the muted baseline row when baseline is provided', () => {
    const { container } = render(
      <MemberComparisonTable
        rows={rowsFixture()}
        columns={columns}
        sortKey="cost"
        sortDir="desc"
        onSort={() => {}}
        baseline={{ cost: '$66.67', turns: '217' }}
        canDrill={false}
      />,
    );
    expect(container.querySelector('.usage-compare-baseline')).toBeTruthy();
    expect(screen.getByText('team avg')).toBeInTheDocument();
    expect(screen.getByText('$66.67')).toBeInTheDocument();
  });

  it('shows drill chevrons and fires onDrill when canDrill', () => {
    const onDrill = vi.fn();
    render(
      <MemberComparisonTable
        rows={rowsFixture()}
        columns={columns}
        sortKey="cost"
        sortDir="desc"
        onSort={() => {}}
        canDrill
        onDrill={onDrill}
      />,
    );
    const drill = screen.getByRole('button', { name: /View Alice's usage/ });
    fireEvent.click(drill);
    expect(onDrill).toHaveBeenCalledWith('u1');
  });

  it('omits drill chevrons when canDrill is false', () => {
    render(
      <MemberComparisonTable
        rows={rowsFixture()}
        columns={columns}
        sortKey="cost"
        sortDir="desc"
        onSort={() => {}}
        canDrill={false}
      />,
    );
    expect(screen.queryByRole('button', { name: /View Alice's usage/ })).toBeNull();
  });

  // (item 8) Per-cell baseline tint — direction respects lowerIsBetter.
  it('tints cells above/below baseline (higher-is-better column)', () => {
    // baseline turns = 200. Alice 400 (good), Bob 250 (good), Carol 0 (bad).
    const cols: MemberComparisonColumn[] = [
      { key: 'cost', label: 'Cost', primary: true },
      { key: 'turns', label: 'Turns', baseline: 200, lowerIsBetter: false },
    ];
    const { container } = render(
      <MemberComparisonTable
        rows={rowsFixture()}
        columns={cols}
        sortKey="turns"
        sortDir="desc"
        onSort={() => {}}
        canDrill={false}
      />,
    );
    // 400 and 250 are ABOVE the 200 baseline on a higher-is-better column → good.
    expect(screen.getByText('400').closest('td')).toHaveClass('usage-cell-good');
    expect(screen.getByText('250').closest('td')).toHaveClass('usage-cell-good');
    // 0 is BELOW the baseline → bad.
    expect(screen.getByText('0').closest('td')).toHaveClass('usage-cell-bad');
    // sanity: at least one good + one bad rendered.
    expect(container.querySelectorAll('.usage-cell-good').length).toBeGreaterThanOrEqual(1);
    expect(container.querySelectorAll('.usage-cell-bad').length).toBe(1);
  });

  it('INVERTS the tint direction for a lower-is-better column', () => {
    // A cost/turn-style column where SMALLER is better. baseline 100.
    // rows: Alice raw 120 (above → worse → bad), Bob raw 80 (below → better → good).
    const rows: MemberComparisonRow[] = [
      {
        userId: 'u1', name: 'Alice', isYou: false, roles: [], active: true,
        cells: { cpt: { display: '$120', raw: 120 } },
      },
      {
        userId: 'u2', name: 'Bob', isYou: false, roles: [], active: true,
        cells: { cpt: { display: '$80', raw: 80 } },
      },
    ];
    const cols: MemberComparisonColumn[] = [
      { key: 'cpt', label: 'Cost / turn', primary: true, baseline: 100, lowerIsBetter: true },
    ];
    render(
      <MemberComparisonTable
        rows={rows}
        columns={cols}
        sortKey="cpt"
        sortDir="asc"
        onSort={() => {}}
        canDrill={false}
      />,
    );
    // Above baseline on lower-is-better → BAD; below → GOOD (inverted vs above).
    expect(screen.getByText('$120').closest('td')).toHaveClass('usage-cell-bad');
    expect(screen.getByText('$80').closest('td')).toHaveClass('usage-cell-good');
  });

  it('omits the primary share bar when share is undefined (lower-is-better primary)', () => {
    const rows: MemberComparisonRow[] = [
      {
        userId: 'u1', name: 'Alice', isYou: false, roles: [], active: true,
        cells: { cpt: { display: '$1.00', raw: 1 } },
        share: undefined, // caller omits the bar for a lower-is-better primary
      },
    ];
    const cols: MemberComparisonColumn[] = [
      { key: 'cpt', label: 'Cost / turn', primary: true, lowerIsBetter: true },
    ];
    const { container } = render(
      <MemberComparisonTable
        rows={rows}
        columns={cols}
        sortKey="cpt"
        sortDir="asc"
        onSort={() => {}}
        canDrill={false}
      />,
    );
    // No share bar drawn; the value still renders.
    expect(container.querySelector('.usage-compare-bar')).toBeNull();
    expect(screen.getByText('$1.00')).toBeInTheDocument();
  });

  it('draws the primary share bar when share is provided (active-metric share)', () => {
    const { container } = render(
      <MemberComparisonTable
        rows={rowsFixture()}
        columns={columns}
        sortKey="cost"
        sortDir="desc"
        onSort={() => {}}
        canDrill={false}
      />,
    );
    // rowsFixture carries share values → bars render on the primary column.
    expect(container.querySelectorAll('.usage-compare-bar').length).toBeGreaterThanOrEqual(1);
  });

  // (Phase 3a) Per-member Δ badge on the primary cell + tier model-mix bar.
  it('renders a per-member Δ badge on the primary cell when delta is provided', () => {
    const rows: MemberComparisonRow[] = [
      {
        userId: 'u1', name: 'Alice', isYou: false, roles: [], active: true,
        cells: { cost: { display: '$120.00', raw: 120 }, turns: { display: '400', raw: 400 } },
        share: 1,
        delta: { pct: 25, dir: 'up' }, // +25% on the primary
      },
    ];
    const { container } = render(
      <MemberComparisonTable
        rows={rows}
        columns={columns}
        sortKey="cost"
        sortDir="desc"
        onSort={() => {}}
        canDrill={false}
      />,
    );
    const badge = container.querySelector('.usage-compare-primary .usage-delta');
    expect(badge).toBeTruthy();
    expect(badge?.textContent).toContain('+25%');
    expect(badge?.textContent).toContain('▲');
  });

  it('renders the Δ "new" pill when delta.pct is null and no badge when delta is absent', () => {
    const withNew: MemberComparisonRow[] = [
      {
        userId: 'u1', name: 'Alice', isYou: false, roles: [], active: true,
        cells: { cost: { display: '$10.00', raw: 10 }, turns: { display: '5', raw: 5 } },
        share: 1,
        delta: { pct: null, dir: 'up' },
      },
    ];
    const { container, rerender } = render(
      <MemberComparisonTable
        rows={withNew} columns={columns} sortKey="cost" sortDir="desc" onSort={() => {}} canDrill={false}
      />,
    );
    expect(container.querySelector('.usage-delta-new')).toBeTruthy();
    // Absent delta → no badge at all.
    const noDelta: MemberComparisonRow[] = [
      {
        userId: 'u1', name: 'Alice', isYou: false, roles: [], active: true,
        cells: { cost: { display: '$10.00', raw: 10 }, turns: { display: '5', raw: 5 } },
        share: 1,
      },
    ];
    rerender(
      <MemberComparisonTable
        rows={noDelta} columns={columns} sortKey="cost" sortDir="desc" onSort={() => {}} canDrill={false}
      />,
    );
    expect(container.querySelector('.usage-compare-primary .usage-delta')).toBeNull();
  });

  it('renders a tier-colored model-mix bar from modelMix segments', () => {
    const rows: MemberComparisonRow[] = [
      {
        userId: 'u1', name: 'Alice', isYou: false, roles: [], active: true,
        cells: { cost: { display: '$120.00', raw: 120 }, turns: { display: '400', raw: 400 } },
        share: 1,
        modelMix: [
          { model: 'opus', tier: 'opus', share: 0.7 },
          { model: 'haiku', tier: 'haiku', share: 0.3 },
        ],
      },
    ];
    const { container } = render(
      <MemberComparisonTable
        rows={rows} columns={columns} sortKey="cost" sortDir="desc" onSort={() => {}} canDrill={false}
      />,
    );
    const bar = container.querySelector('.usage-mix-bar');
    expect(bar).toBeTruthy();
    const segs = container.querySelectorAll('.usage-mix-seg');
    expect(segs.length).toBe(2);
    expect(container.querySelector('.usage-mix-opus')).toBeTruthy();
    expect(container.querySelector('.usage-mix-haiku')).toBeTruthy();
    // Widths reflect the shares (the DOM normalizes "70.0%" → "70%").
    expect((segs[0] as HTMLElement).style.width).toBe('70%');
    expect((segs[1] as HTMLElement).style.width).toBe('30%');
  });

  it('omits the model-mix bar when modelMix is empty/absent', () => {
    const rows: MemberComparisonRow[] = [
      {
        userId: 'u1', name: 'Alice', isYou: false, roles: [], active: true,
        cells: { cost: { display: '$10.00', raw: 10 }, turns: { display: '5', raw: 5 } },
        share: 1,
        modelMix: [],
      },
    ];
    const { container } = render(
      <MemberComparisonTable
        rows={rows} columns={columns} sortKey="cost" sortDir="desc" onSort={() => {}} canDrill={false}
      />,
    );
    expect(container.querySelector('.usage-mix-bar')).toBeNull();
  });
});

// -------- MiniSparkline accent (item 8) --------
describe('MiniSparkline accent', () => {
  it('uses the up-accent class only when accent AND trending up', () => {
    const { container } = render(<MiniSparkline values={[1, 2, 5]} accent />);
    expect(container.querySelector('.usage-sparkline-up')).toBeTruthy();
  });
  it('does NOT accent a flat/down series even with accent', () => {
    const { container: down } = render(<MiniSparkline values={[5, 3, 1]} accent />);
    expect(down.querySelector('.usage-sparkline-up')).toBeNull();
    const { container: flat } = render(<MiniSparkline values={[2, 2, 2]} accent />);
    expect(flat.querySelector('.usage-sparkline-up')).toBeNull();
  });
  it('never accents without the accent flag', () => {
    const { container } = render(<MiniSparkline values={[1, 2, 5]} />);
    expect(container.querySelector('.usage-sparkline-up')).toBeNull();
  });
});

// -------- TeamTrendChart (Phase 3b) --------
describe('TeamTrendChart', () => {
  const dates = ['2026-06-10', '2026-06-11', '2026-06-12'];
  function fixture(): TeamTrendSeries[] {
    return [
      { userId: 'u1', name: 'alice', color: '#d670d6', values: [1, 2, 3] },
      { userId: 'u2', name: 'eli', color: '#9d7fd6', values: [3, 2, 1], isYou: true },
    ];
  }

  it('renders one polyline per series', () => {
    const { container } = render(<TeamTrendChart dates={dates} series={fixture()} />);
    expect(container.querySelectorAll('.usage-teamtrend-line').length).toBe(2);
    expect(container.querySelector('.usage-teamtrend-chart')).toBeTruthy();
  });

  it('renders a legend with each member name + a "you" chip for isYou', () => {
    const { container } = render(<TeamTrendChart dates={dates} series={fixture()} />);
    const legend = container.querySelector('.usage-teamtrend-legend') as HTMLElement;
    expect(legend.querySelectorAll('.usage-daily-legend-item').length).toBe(2);
    // Names appear in the legend (scope to it — the name also lives in <title>s).
    expect(within(legend).getByText('alice')).toBeInTheDocument();
    expect(within(legend).getByText('eli')).toBeInTheDocument();
    // Exactly one "you" chip — on eli's entry.
    expect(within(legend).getAllByText('you').length).toBe(1);
  });

  it('shows the empty-state when there are no series', () => {
    const { container } = render(<TeamTrendChart dates={dates} series={[]} />);
    expect(screen.getByText(/No member trend data in this window/i)).toBeInTheDocument();
    expect(container.querySelector('.usage-teamtrend-chart')).toBeNull();
  });

  it('shows the empty-state when there are no dates', () => {
    render(<TeamTrendChart dates={[]} series={fixture()} />);
    expect(screen.getByText(/No member trend data in this window/i)).toBeInTheDocument();
  });

  it('shows the empty-state when every series value is zero', () => {
    const allZero: TeamTrendSeries[] = [
      { userId: 'u1', name: 'alice', color: '#d670d6', values: [0, 0, 0] },
      { userId: 'u2', name: 'eli', color: '#9d7fd6', values: [0, 0, 0] },
    ];
    const { container } = render(<TeamTrendChart dates={dates} series={allZero} />);
    expect(screen.getByText(/No member trend data in this window/i)).toBeInTheDocument();
    expect(container.querySelector('.usage-teamtrend-chart')).toBeNull();
  });

  it('renders dots and no lines in single-date mode', () => {
    const { container } = render(
      <TeamTrendChart
        dates={['2026-06-10']}
        series={[{ userId: 'u1', name: 'alice', color: '#d670d6', values: [5] }]}
      />,
    );
    expect(container.querySelectorAll('.usage-teamtrend-line').length).toBe(0);
    expect(container.querySelectorAll('.usage-teamtrend-dot').length).toBe(1);
  });

  it('de-emphasizes an inactive (active:false) series in chart and legend', () => {
    const series: TeamTrendSeries[] = [
      { userId: 'u1', name: 'alice', color: '#d670d6', values: [1, 2, 3], active: true },
      { userId: 'u2', name: 'carol', color: '#9d7fd6', values: [1, 0, 0], active: false },
    ];
    const { container } = render(<TeamTrendChart dates={dates} series={series} />);
    // The inactive series' <g> + its legend item both carry is-faint.
    expect(container.querySelectorAll('.usage-teamtrend-series.is-faint').length).toBe(1);
    expect(container.querySelectorAll('.usage-teamtrend-legend .usage-daily-legend-item.is-faint').length).toBe(1);
  });

  it('treats an all-zero (but active) series as faint too', () => {
    const series: TeamTrendSeries[] = [
      { userId: 'u1', name: 'alice', color: '#d670d6', values: [1, 2, 3] },
      { userId: 'u2', name: 'dan', color: '#9d7fd6', values: [0, 0, 0] },
    ];
    const { container } = render(<TeamTrendChart dates={dates} series={series} />);
    expect(container.querySelectorAll('.usage-teamtrend-series.is-faint').length).toBe(1);
  });

  it('formats USD tooltips by default ("name · date: $value")', () => {
    const { container } = render(
      <TeamTrendChart
        dates={dates}
        series={[{ userId: 'u1', name: 'eli', color: '#d670d6', values: [0, 0, 1234] }]}
      />,
    );
    const titles = Array.from(container.querySelectorAll('title')).map((t) => t.textContent);
    expect(titles).toContain('eli · 2026-06-12: $1,234');
  });

  it('formats integer tooltips when valueFormat is "int" (e.g. turns)', () => {
    const { container } = render(
      <TeamTrendChart
        dates={dates}
        series={[{ userId: 'u1', name: 'eli', color: '#d670d6', values: [0, 0, 1234] }]}
        valueFormat="int"
        metricLabel="daily turns"
      />,
    );
    const titles = Array.from(container.querySelectorAll('title')).map((t) => t.textContent);
    expect(titles).toContain('eli · 2026-06-12: 1,234');
  });

  it('stays legible with many series (≥6 polylines, one legend item each)', () => {
    const palette = ['#d670d6', '#9d7fd6', '#c062c0', '#8a6fd0', '#e07fe0', '#7a5fc0', '#b050b0'];
    const many: TeamTrendSeries[] = palette.map((color, i) => ({
      userId: `u${i}`,
      name: `m${i}`,
      color,
      values: [i + 1, i + 2, i + 3],
    }));
    const { container } = render(<TeamTrendChart dates={dates} series={many} />);
    expect(container.querySelectorAll('.usage-teamtrend-line').length).toBe(7);
    expect(container.querySelectorAll('.usage-teamtrend-legend .usage-daily-legend-item').length).toBe(7);
  });
});

// -------- ProjectMembersBar (Phase 3c) --------
describe('ProjectMembersBar', () => {
  function members(): ProjectMemberDatum[] {
    return [
      { userId: 'a', name: 'Alice', color: '#d670d6', costUsd: 60, turns: 30, share: 0.6 },
      { userId: 'b', name: 'Bob', color: '#9d7fd6', costUsd: 30, turns: 12, share: 0.3, isYou: true },
      { userId: 'c', name: 'Carol', color: '#c062c0', costUsd: 10, turns: 4, share: 0.1 },
    ];
  }

  it('renders nothing for empty members', () => {
    const { container } = render(<ProjectMembersBar members={[]} />);
    expect(container.firstChild).toBeNull();
  });

  it('renders one bar segment per member with width = share and the member color', () => {
    const { container } = render(<ProjectMembersBar members={members()} />);
    expect(container.querySelector('.usage-projmembers-bar')).toBeTruthy();
    const segs = container.querySelectorAll('.usage-projmembers-seg');
    expect(segs.length).toBe(3);
    // Widths reflect the shares (DOM normalizes "60.0%" → "60%").
    expect((segs[0] as HTMLElement).style.width).toBe('60%');
    expect((segs[1] as HTMLElement).style.width).toBe('30%');
    expect((segs[2] as HTMLElement).style.width).toBe('10%');
    // Colors come straight from the data (caller-owned color map).
    expect((segs[0] as HTMLElement).style.background).toBe('rgb(214, 112, 214)');
  });

  it('puts the cost/turns/share breakdown in each segment <title>', () => {
    const { container } = render(<ProjectMembersBar members={members()} />);
    const titles = Array.from(container.querySelectorAll('.usage-projmembers-seg')).map(
      (s) => s.getAttribute('title'),
    );
    expect(titles[0]).toBe('Alice: $60.00 · 30 turns · 60%');
    expect(titles[1]).toBe('Bob: $30.00 · 12 turns · 30%');
  });

  it('renders a legend with each member name + share% and a "you" chip', () => {
    const { container } = render(<ProjectMembersBar members={members()} />);
    const legend = container.querySelector('.usage-projmembers-legend') as HTMLElement;
    expect(legend.querySelectorAll('.usage-projmembers-legend-item').length).toBe(3);
    expect(within(legend).getByText('Alice')).toBeInTheDocument();
    expect(within(legend).getByText('60%')).toBeInTheDocument();
    // Exactly one "you" chip — on Bob's entry.
    expect(within(legend).getAllByText('you').length).toBe(1);
  });

  it('folds the tail into an "others" segment + legend entry past maxSegments', () => {
    const many: ProjectMemberDatum[] = [
      { userId: 'a', name: 'A', color: '#1', costUsd: 40, turns: 10, share: 0.4 },
      { userId: 'b', name: 'B', color: '#2', costUsd: 25, turns: 8, share: 0.25 },
      { userId: 'c', name: 'C', color: '#3', costUsd: 15, turns: 6, share: 0.15 },
      { userId: 'd', name: 'D', color: '#4', costUsd: 12, turns: 4, share: 0.12 },
      { userId: 'e', name: 'E', color: '#5', costUsd: 8, turns: 2, share: 0.08 },
    ];
    const { container } = render(<ProjectMembersBar members={many} maxSegments={3} />);
    // 3 head segments + 1 folded "others" segment.
    expect(container.querySelectorAll('.usage-projmembers-seg').length).toBe(4);
    const others = container.querySelector('.usage-projmembers-seg-others') as HTMLElement;
    expect(others).toBeTruthy();
    // Folded width/share = D + E = 0.12 + 0.08 = 0.20.
    expect(others.style.width).toBe('20%');
    expect(others.getAttribute('title')).toBe('+2 others: $20.00 · 6 turns · 20%');
    // Legend carries the head members + a folded "+2 others" entry.
    expect(container.querySelectorAll('.usage-projmembers-legend-item').length).toBe(4);
    expect(screen.getByText('+2 others')).toBeInTheDocument();
  });

  it('does not fold when members fit within maxSegments', () => {
    const { container } = render(<ProjectMembersBar members={members()} maxSegments={5} />);
    expect(container.querySelector('.usage-projmembers-seg-others')).toBeNull();
    expect(container.querySelectorAll('.usage-projmembers-legend-item').length).toBe(3);
  });

  it('skips zero-share segments in the bar (but keeps them in the legend)', () => {
    const withZero: ProjectMemberDatum[] = [
      { userId: 'a', name: 'A', color: '#1', costUsd: 100, turns: 10, share: 1 },
      { userId: 'b', name: 'B', color: '#2', costUsd: 0, turns: 0, share: 0 },
    ];
    const { container } = render(<ProjectMembersBar members={withZero} />);
    // Only the non-zero member draws a bar segment.
    expect(container.querySelectorAll('.usage-projmembers-seg').length).toBe(1);
    // Both still appear in the legend.
    expect(container.querySelectorAll('.usage-projmembers-legend-item').length).toBe(2);
  });
});

// -------- PeriodDigest --------
// A complete digest with both up + down movers, chips, and the four key deltas.
// pcts are FRACTIONS (the lib contract); the component scales them ×100.
function digestFixture(over: Partial<PeriodDigestData> = {}): PeriodDigestData {
  return {
    windowLabel: '30 days',
    headline: 'Team spend up 12% vs the prior 30 days · 2 of 3 active · Eli accelerating, Dana cooling',
    heroDeltas: {
      cost_usd: { cur: 112, prev: 100, pct: 0.12, dir: 'up' },
      turns: { cur: 200, prev: 150, pct: 0.333, dir: 'up' },
      sessions: { cur: 10, prev: 10, pct: 0, dir: 'flat' },
      active_days: { cur: 20, prev: 22, pct: -0.0909, dir: 'down' },
      fresh_tokens: { cur: 0, prev: 0, pct: null, dir: 'flat' },
      cached_tokens: { cur: 0, prev: 0, pct: null, dir: 'flat' },
      cache_hit_rate: { cur: 0.8, prev: 0.5, pct: 0.6, dir: 'up' },
    },
    moversUp: [
      { userId: 'a', name: 'Eli', pct: 1.0, dir: 'up', costUsd: 200, turns: 50, isYou: false },
      { userId: 'b', name: 'You', pct: 0.1, dir: 'up', costUsd: 110, turns: 10, isYou: true },
    ],
    moversDown: [
      { userId: 'c', name: 'Dana', pct: -0.6, dir: 'down', costUsd: 40, turns: 20, isYou: false },
    ],
    newcomers: [],
    topContributor: { name: 'Eli', userId: 'a', share: 0.7 },
    mostEfficient: { name: 'Eff', userId: 'e', costPerTurn: 0.25 },
    membersActive: 2,
    membersTotal: 3,
    ...over,
  };
}

describe('PeriodDigest', () => {
  it('renders nothing when the digest is null', () => {
    const { container } = render(<PeriodDigest digest={null} />);
    expect(container.firstChild).toBeNull();
  });

  it('renders the headline sentence', () => {
    render(<PeriodDigest digest={digestFixture()} />);
    expect(
      screen.getByText(/Team spend up 12% vs the prior 30 days/),
    ).toBeInTheDocument();
  });

  it('renders the four key hero deltas with values and badges scaled ×100', () => {
    const { container } = render(<PeriodDigest digest={digestFixture()} />);
    const labels = Array.from(
      container.querySelectorAll('.usage-digest-delta-label'),
    ).map((n) => n.textContent);
    expect(labels).toEqual(['Spend', 'Turns', 'Active', 'Cache hit']);
    // cost 0.12 fraction → "+12%" (×100 then rounded). Active -0.0909 → "-9%".
    expect(screen.getByText('+12%')).toBeInTheDocument();
    expect(screen.getByText('-9%')).toBeInTheDocument();
    // Cache-hit value formats via pct() (0.8 → "80%").
    const vals = Array.from(
      container.querySelectorAll('.usage-digest-delta-val'),
    ).map((n) => n.textContent);
    expect(vals).toContain('80%');
    expect(vals).toContain('$112.00');
  });

  it('renders the spend delta with the NEUTRAL (flat) tone, not the bad tone', () => {
    const { container } = render(<PeriodDigest digest={digestFixture()} />);
    const spend = container.querySelector('.usage-digest-delta');
    // The spend group is first; its badge must be flat (neutral), never bad.
    expect(spend?.querySelector('.usage-delta-bad')).toBeNull();
  });

  it('lists accelerating movers (up) and cooling movers (down)', () => {
    const { container } = render(<PeriodDigest digest={digestFixture()} />);
    const accel = container.querySelector('.usage-digest-accel')!;
    const cool = container.querySelector('.usage-digest-cool')!;
    expect(within(accel as HTMLElement).getByText('Eli')).toBeInTheDocument();
    expect(within(accel as HTMLElement).getByText('+100%')).toBeInTheDocument();
    expect(within(cool as HTMLElement).getByText('Dana')).toBeInTheDocument();
    expect(within(cool as HTMLElement).getByText('-60%')).toBeInTheDocument();
  });

  it('flags the caller own mover row with a "you" chip', () => {
    const { container } = render(<PeriodDigest digest={digestFixture()} />);
    const accel = container.querySelector('.usage-digest-accel')!;
    expect(within(accel as HTMLElement).getByText('you')).toBeInTheDocument();
    expect(within(accel as HTMLElement).getByText('You')).toBeInTheDocument();
  });

  it('renders the top-contributor and most-efficient chips', () => {
    const { container } = render(<PeriodDigest digest={digestFixture()} />);
    const chips = container.querySelector('.usage-digest-chips')!;
    expect(within(chips as HTMLElement).getByText('Eli')).toBeInTheDocument();
    expect(within(chips as HTMLElement).getByText('70%')).toBeInTheDocument();
    expect(within(chips as HTMLElement).getByText('Eff')).toBeInTheDocument();
    expect(within(chips as HTMLElement).getByText('$0.25/turn')).toBeInTheDocument();
  });

  it('shows empty-state lines when a mover direction has no members', () => {
    const { container } = render(
      <PeriodDigest digest={digestFixture({ moversUp: [], moversDown: [] })} />,
    );
    expect(screen.getByText('No one ramped up.')).toBeInTheDocument();
    expect(screen.getByText('No one cooled off.')).toBeInTheDocument();
    // No mover rows rendered.
    expect(container.querySelectorAll('.usage-digest-mover').length).toBe(0);
  });

  it('hides the chips row entirely when both highlights are null', () => {
    const { container } = render(
      <PeriodDigest digest={digestFixture({ topContributor: null, mostEfficient: null })} />,
    );
    expect(container.querySelector('.usage-digest-chips')).toBeNull();
  });

  it('renders a "new" pill for a hero delta with null pct', () => {
    const d = digestFixture({
      heroDeltas: {
        ...digestFixture().heroDeltas,
        cost_usd: { cur: 200, prev: 0, pct: null, dir: 'up' },
      },
    });
    const { container } = render(<PeriodDigest digest={d} />);
    // The Spend group's badge becomes a "new" pill (no prior to divide by).
    const spend = container.querySelector('.usage-digest-delta')!;
    expect(within(spend as HTMLElement).getByText('new')).toBeInTheDocument();
  });
});

// -------- Codex provider surfaces --------
describe('ProviderToggle', () => {
  it('renders a Provider group with the active option pressed and reports clicks', () => {
    const onChange = vi.fn();
    render(<ProviderToggle provider="claude" onChange={onChange} />);
    const group = screen.getByRole('group', { name: 'Provider' });
    expect(within(group).getByRole('button', { name: 'Claude Code' })).toHaveAttribute('aria-pressed', 'true');
    expect(within(group).getByRole('button', { name: 'All' })).toHaveAttribute('aria-pressed', 'false');
    fireEvent.click(within(group).getByRole('button', { name: 'Codex' }));
    expect(onChange).toHaveBeenCalledWith('codex');
  });
});

describe('ByProviderCard', () => {
  const rows: ProviderRow[] = [
    {
      provider: 'claude', label: 'Claude Code', cost_usd: 75, share: 0.75, turns: 10, sessions: 2,
      fresh_tokens: 1500, cached_tokens: 2_000_000, output_tokens: 300, reasoning_tokens: 0,
    },
    {
      provider: 'codex', label: 'Codex', cost_usd: 25, share: 0.25, turns: 4, sessions: 1,
      fresh_tokens: 100, cached_tokens: 0, output_tokens: 5000, reasoning_tokens: 1200,
    },
  ];

  it('renders one row per provider with cost, share and token split', () => {
    const { container } = render(<ByProviderCard rows={rows} active="all" onSelect={() => {}} />);
    expect(screen.getByText('Claude Code')).toBeInTheDocument();
    expect(screen.getByText('$75.00')).toBeInTheDocument();
    expect(screen.getByText('75%')).toBeInTheDocument();
    expect(screen.getByText('2.0M cached')).toBeInTheDocument();
    // Reasoning subset shown only when non-zero.
    expect(container.textContent).toContain('5.0K output (1.2K reasoning)');
    expect(container.textContent).not.toContain('300 output (');
  });

  it('a row click sets the filter; clicking the active row clears it', () => {
    const onSelect = vi.fn();
    const { rerender } = render(<ByProviderCard rows={rows} active="all" onSelect={onSelect} />);
    fireEvent.click(screen.getByRole('button', { name: /Codex/ }));
    expect(onSelect).toHaveBeenLastCalledWith('codex');
    rerender(<ByProviderCard rows={rows} active="codex" onSelect={onSelect} />);
    const codex = screen.getByRole('button', { name: /Codex/ });
    expect(codex).toHaveAttribute('aria-pressed', 'true');
    fireEvent.click(codex);
    expect(onSelect).toHaveBeenLastCalledWith('all');
  });
});

describe('MemberComparisonTable — provider + last active', () => {
  it('maps OpenAI tiers/models to the Codex mix class', () => {
    expect(tierMixClass('opus')).toBe('usage-mix-opus');
    expect(tierMixClass('gpt-6-astra')).toBe('usage-mix-codex');
    expect(tierMixClass('sonnet', 'gpt-5.6-luna')).toBe('usage-mix-codex');
    expect(tierMixClass('other', 'other')).toBe('usage-mix-other');
  });

  it('renders Last active under the member and the provider-mix chip when rows carry them', () => {
    const rows = rowsFixture();
    rows[0].lastActive = 'today';
    rows[1].lastActive = '3d ago';
    rows[0].providerMix = 'Codex 32%';
    render(
      <MemberComparisonTable
        rows={rows}
        columns={columns}
        sortKey="cost"
        sortDir="desc"
        onSort={() => {}}
        canDrill={false}
      />,
    );
    expect(screen.queryByRole('columnheader', { name: 'Last active' })).toBeNull();
    expect(screen.getByText('Last active today')).toBeInTheDocument();
    expect(screen.getByText('Last active 3d ago')).toBeInTheDocument();
    expect(screen.getByText('Codex 32%')).toBeInTheDocument();
  });

  it('omits Last active when no row carries it', () => {
    render(
      <MemberComparisonTable
        rows={rowsFixture()}
        columns={columns}
        sortKey="cost"
        sortDir="desc"
        onSort={() => {}}
        canDrill={false}
      />,
    );
    expect(screen.queryByText(/Last active/)).toBeNull();
  });

  it('colours a lower-is-better primary Δ with up = bad (cost / turn)', () => {
    const rows = rowsFixture();
    rows[0].delta = { pct: 25, dir: 'up' };
    const { container } = render(
      <MemberComparisonTable
        rows={rows}
        columns={[{ key: 'cost', label: 'Cost / turn', primary: true, lowerIsBetter: true }]}
        sortKey="cost"
        sortDir="asc"
        onSort={() => {}}
        canDrill={false}
      />,
    );
    expect(container.querySelector('.usage-compare-primary .usage-delta-bad')).toBeTruthy();
  });
});
