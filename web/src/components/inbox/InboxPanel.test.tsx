import { act, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { beforeEach, afterEach, describe, expect, it, vi } from 'vitest';
import { useEntitlements } from '../../entitlements';
import { useStore } from '../../store';
import { api } from '../../api';
import { InboxHost, openInbox } from './InboxPanel';
import { __resetFeedbackInboxForTests } from '../../lib/useFeedbackInbox';
import { __resetWardenInboxForTests } from '../../lib/useWardenInbox';
import { __resetWardenReadForTests } from '../../lib/wardenRead';
import type { Schedule, WardenRun } from '../../types';
import type { FeedbackEvent } from '../../api';

// LOCAL yesterday at noon — "now - 26h" is timezone/time-of-day flaky (at
// 00:31 UTC it lands two calendar days back and the "Yesterday" group never
// renders; that exact flake failed CI on 2026-07-11).
function yesterdayNoon(): string {
  const d = new Date();
  d.setDate(d.getDate() - 1);
  d.setHours(12, 0, 0, 0);
  return d.toISOString();
}

function sched(over: Partial<Schedule> = {}): Schedule {
  return {
    id: 's1',
    name: 'Morning digest',
    kind: 'task',
    enabled: true,
    createdAt: '2026-07-01T00:00:00Z',
    fireCount: 1,
    skipCount: 0,
    errorCount: 0,
    runs: [
      { at: new Date(Date.now() - 60_000).toISOString(), status: 'fired', summary: '## Needs you\n- approve PR #7', logFile: 'run-0.md' },
      { at: yesterdayNoon(), status: 'fired', summary: 'Nothing needs you today.' },
    ],
    ...over,
  } as Schedule;
}

function fbEvent(over: Partial<FeedbackEvent> = {}): FeedbackEvent {
  return {
    number: 42,
    title: 'Paste fails on Windows',
    event: 'needs-info',
    message: 'Needs more info — which browser?',
    at: new Date(Date.now() - 1800_000).toISOString(),
    ...over,
  };
}

function wardenRun(over: Partial<WardenRun> = {}): WardenRun {
  return {
    kind: 'triage',
    paneTitle: 'blocked-pane',
    at: new Date(Date.now() - 120_000).toISOString(),
    status: 'ok',
    summary: 'needs a decision: A or B',
    logFile: 'run-w1.log',
    ...over,
  };
}

beforeEach(() => useEntitlements.setState({ loaded: true, team: true, operator: true }));

afterEach(() => {
  useEntitlements.setState({ loaded: false, team: false, operator: false });
  useStore.setState({ schedules: [] });
  __resetFeedbackInboxForTests([]);
  __resetWardenInboxForTests([]);
  __resetWardenReadForTests([]);
  vi.restoreAllMocks();
});

function openHost() {
  render(<InboxHost />);
  act(() => openInbox());
}

describe('InboxHost / feed', () => {
  it('opens on the event, pins needs-you, groups the rest by day, Esc closes', () => {
    useStore.setState({ schedules: [sched()] });
    __resetFeedbackInboxForTests([fbEvent()]);
    openHost();

    expect(screen.getByRole('dialog', { name: 'Inbox' })).toBeInTheDocument();
    // Both unread needs-you items pinned under "Needs you"; the quiet older
    // report files under Yesterday.
    expect(screen.getByText('Needs you')).toBeInTheDocument();
    expect(screen.getByText('Yesterday')).toBeInTheDocument();
    expect(screen.getByText(/approve PR #7/)).toBeInTheDocument();
    expect(screen.getByText(/which browser/)).toBeInTheDocument();
    // Nothing selected yet — the reading pane shows the hint, not a report.
    expect(screen.getByText('Nothing open')).toBeInTheDocument();

    fireEvent.keyDown(window, { key: 'Escape' });
    expect(screen.queryByRole('dialog', { name: 'Inbox' })).toBeNull();
  });

  it('legacy solu:open-reports-feed still opens the feed', () => {
    useStore.setState({ schedules: [sched()] });
    render(<InboxHost />);
    act(() => {
      window.dispatchEvent(new CustomEvent('solu:open-reports-feed'));
    });
    expect(screen.getByRole('dialog', { name: 'Inbox' })).toBeInTheDocument();
  });

  it('filters to one source when both are present', () => {
    useStore.setState({ schedules: [sched()] });
    __resetFeedbackInboxForTests([fbEvent()]);
    openHost();
    fireEvent.click(screen.getByRole('tab', { name: /Feedback 1/ }));
    expect(screen.queryByText(/approve PR #7/)).toBeNull();
    expect(screen.getByText(/which browser/)).toBeInTheDocument();
  });

  it('mark all read stamps reports and feedback', async () => {
    const schedRead = vi.spyOn(api, 'markScheduleRead').mockResolvedValue(undefined as never);
    const fbRead = vi.spyOn(api, 'feedbackInboxRead').mockResolvedValue(undefined);
    const s = sched();
    useStore.setState({ schedules: [s] });
    __resetFeedbackInboxForTests([fbEvent()]);
    openHost();

    fireEvent.click(screen.getByText('Mark all read'));
    await waitFor(() => expect(schedRead).toHaveBeenCalledWith('s1', s.runs![0].at));
    expect(fbRead).toHaveBeenCalledWith(42);
  });

  it('warden triage pins as needs-you and its brief opens in the reading pane', async () => {
    const logSpy = vi.spyOn(api, 'wardenRunLog').mockResolvedValue(
      '# Fleet brief\n\n## NEEDS A DECISION\n- rotate the leaked key',
    );
    __resetWardenReadForTests([]);
    __resetWardenInboxForTests([wardenRun()]);
    openHost();

    // A triage is a demand → pins under "Needs you".
    expect(screen.getByText('Needs you')).toBeInTheDocument();
    expect(screen.getByText(/Triage — blocked-pane/)).toBeInTheDocument();

    fireEvent.click(screen.getByText(/Triage — blocked-pane/));
    await waitFor(() => expect(logSpy).toHaveBeenCalledWith('run-w1.log'));
    expect(await screen.findByText(/rotate the leaked key/)).toBeInTheDocument();
  });

  it('viewing a warden brief marks it read (client ledger), clearing mark-all', () => {
    __resetWardenReadForTests([]);
    __resetWardenInboxForTests([wardenRun({ kind: 'brief', logFile: 'run-w2.log', summary: 'all quiet' })]);
    vi.spyOn(api, 'wardenRunLog').mockResolvedValue('# Fleet brief\n\nNothing needs you.');
    openHost();

    // Unread → the Mark-all affordance is present.
    expect(screen.getByText('Mark all read')).toBeInTheDocument();
    fireEvent.click(screen.getByText('Mark all read'));
    // The client ledger flips it read and notifies → no more unread → gone.
    expect(screen.queryByText('Mark all read')).toBeNull();
  });

  it('report row opens the report in the reading pane and viewing marks it read', async () => {
    const logSpy = vi.spyOn(api, 'scheduleRunLog').mockResolvedValue('Full report body text here.');
    const readSpy = vi.spyOn(api, 'markScheduleRead').mockResolvedValue(undefined as never);
    const s = sched();
    useStore.setState({ schedules: [s] });
    openHost();

    fireEvent.click(screen.getByText(/approve PR #7/));
    // The reader mounts in the right pane — no dialog stacked on the window.
    expect(screen.getByText(/Report · Morning digest/)).toBeInTheDocument();
    await waitFor(() => expect(logSpy).toHaveBeenCalledWith('s1', 'run-0.md'));
    expect(await screen.findByText(/Full report body text here/)).toBeInTheDocument();
    // The feed stays visible next to it (two-pane), and viewing stamps read.
    expect(screen.getByText('Needs you')).toBeInTheDocument();
    await waitFor(() => expect(readSpy).toHaveBeenCalledWith('s1', s.runs![0].at));
  });

  it('↓ and j walk the visual order: pinned report first, then the feedback thread', async () => {
    vi.spyOn(api, 'scheduleRunLog').mockResolvedValue('Full report body text here.');
    vi.spyOn(api, 'markScheduleRead').mockResolvedValue(undefined as never);
    const fbRead = vi.spyOn(api, 'feedbackInboxRead').mockResolvedValue(undefined);
    vi.spyOn(api, 'feedbackThread').mockResolvedValue({
      number: 42,
      title: 'Paste fails on Windows',
      filedAt: '2026-07-09T10:00:00Z',
      events: [
        { event: 'filed', message: 'Report filed', at: '2026-07-09T10:00:00Z' },
        { event: 'needs-info', message: 'Needs more info — which browser?', at: '2026-07-10T09:00:00Z' },
      ],
    });
    useStore.setState({ schedules: [sched()] });
    __resetFeedbackInboxForTests([fbEvent()]);
    openHost();

    fireEvent.keyDown(window, { key: 'ArrowDown' });
    expect(await screen.findByText(/Full report body text here/)).toBeInTheDocument();

    fireEvent.keyDown(window, { key: 'j' });
    expect(await screen.findByText('Needs your info', { exact: false })).toBeInTheDocument();
    // Selecting the feedback row marked it read.
    expect(fbRead).toHaveBeenCalledWith(42);
  });
});

describe('InboxHost / thread', () => {
  it('feedback row opens the conversation: timeline renders, reply sends and appends', async () => {
    vi.spyOn(api, 'feedbackInboxRead').mockResolvedValue(undefined);
    const threadSpy = vi.spyOn(api, 'feedbackThread').mockResolvedValue({
      number: 42,
      title: 'Paste fails on Windows',
      filedAt: '2026-07-09T10:00:00Z',
      events: [
        { event: 'filed', message: 'Report filed', at: '2026-07-09T10:00:00Z' },
        { event: 'needs-info', message: 'Needs more info — which browser?', at: '2026-07-10T09:00:00Z' },
      ],
    });
    const replySpy = vi.spyOn(api, 'feedbackReply').mockResolvedValue(undefined as never);
    __resetFeedbackInboxForTests([fbEvent()]);
    openHost();

    fireEvent.click(screen.getAllByText(/which browser/)[0]);
    await waitFor(() => expect(threadSpy).toHaveBeenCalledWith(42));
    expect(screen.getByText('Report #42 · filed', { exact: false })).toBeInTheDocument();
    expect(screen.getByText('Needs your info', { exact: false })).toBeInTheDocument();
    // Two-pane: the feed stays visible beside the conversation — the row
    // (now read, so re-filed under a day bucket) and the thread title both
    // render the report's name.
    expect(screen.getAllByText('Paste fails on Windows').length).toBe(2);

    fireEvent.change(screen.getByPlaceholderText(/Add details/), { target: { value: 'Chrome 126, Ctrl+Shift+V works' } });
    fireEvent.click(screen.getByRole('button', { name: 'Send reply' }));
    await waitFor(() => expect(replySpy).toHaveBeenCalledWith(42, 'Chrome 126, Ctrl+Shift+V works'));
    // Optimistic append shows the reply bubble.
    expect(await screen.findByText(/Ctrl\+Shift\+V works/)).toBeInTheDocument();
    expect(screen.getByText('You replied', { exact: false })).toBeInTheDocument();
  });

  it('a fixed thread hides the reply box', async () => {
    vi.spyOn(api, 'feedbackInboxRead').mockResolvedValue(undefined);
    vi.spyOn(api, 'feedbackThread').mockResolvedValue({
      number: 38,
      title: 'Dark mode flashes white',
      filedAt: '2026-07-05T10:00:00Z',
      events: [
        { event: 'filed', message: 'Report filed', at: '2026-07-05T10:00:00Z' },
        { event: 'fixed', message: 'Your report was fixed', at: '2026-07-10T09:00:00Z' },
      ],
    });
    __resetFeedbackInboxForTests([fbEvent({ number: 38, title: 'Dark mode flashes white', event: 'fixed', message: 'Your report was fixed' })]);
    openHost();
    fireEvent.click(screen.getAllByText(/report was fixed/)[0]);
    await screen.findByText(/This report is fixed/);
    expect(screen.queryByPlaceholderText(/Add details/)).toBeNull();
  });
});

describe('InboxHost / compose', () => {
  it('✎ opens the composer; cancel returns to the feed', () => {
    useStore.setState({ schedules: [sched()] });
    openHost();
    fireEvent.click(screen.getByRole('button', { name: 'Send feedback' }));
    expect(screen.getByLabelText('Title')).toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: 'Cancel' }));
    expect(screen.getByText(/approve PR #7/)).toBeInTheDocument();
  });

  it('setFeedbackOpen (Ctrl+K path) opens the panel on compose', () => {
    render(<InboxHost />);
    act(() => {
      useStore.getState().setFeedbackOpen(true);
    });
    expect(screen.getByRole('button', { name: 'Send feedback' })).toBeInTheDocument();
    expect(screen.getByLabelText('Title')).toBeInTheDocument();
  });
});
