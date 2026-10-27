import { afterEach, describe, expect, it } from 'vitest';
import { agentReportToInbox, inboxFeed, wardenToInbox } from './inboxFeed';
import type { AgentReport } from '../api';
import {
  __resetWardenReadForTests,
  markWardenRunRead,
  isWardenRunRead,
  pruneWardenRead,
} from './wardenRead';
import type { WardenRun } from '../types';

function run(over: Partial<WardenRun> = {}): WardenRun {
  return {
    kind: 'brief',
    at: '2026-07-20T10:00:00Z',
    status: 'ok',
    summary: 'all quiet',
    logFile: 'run-1.log',
    ...over,
  };
}

afterEach(() => __resetWardenReadForTests([]));

describe('wardenToInbox', () => {
  it('maps kind to a label, keys by run id, and only triage needs you', () => {
    __resetWardenReadForTests([]);
    const brief = wardenToInbox(run());
    expect(brief.source).toBe('warden');
    expect(brief.title).toBe('Fleet brief');
    expect(brief.key).toBe('warden:run-1.log');
    expect(brief.needsYou).toBe(false);
    expect(brief.unread).toBe(true);

    const triage = wardenToInbox(run({ kind: 'triage', paneTitle: 'p', logFile: 'run-2.log' }));
    expect(triage.title).toBe('Triage — p');
    expect(triage.needsYou).toBe(true);
  });

  it('reflects the read ledger in unread', () => {
    const r = run({ logFile: 'run-3.log' });
    __resetWardenReadForTests([]);
    expect(wardenToInbox(r).unread).toBe(true);
    markWardenRunRead(r);
    expect(wardenToInbox(r).unread).toBe(false);
  });

  it('falls back to the timestamp id when a run has no log file', () => {
    const r = run({ logFile: undefined, at: '2026-07-20T11:00:00Z' });
    expect(wardenToInbox(r).key).toBe('warden:2026-07-20T11:00:00Z');
  });
});

describe('inboxFeed with warden runs', () => {
  it('merges warden runs alongside reports/feedback, newest first, capped', () => {
    const items = inboxFeed(
      [],
      [],
      [run({ at: '2026-07-20T09:00:00Z', logFile: 'a.log' }), run({ at: '2026-07-20T12:00:00Z', logFile: 'b.log' })],
    );
    expect(items).toHaveLength(2);
    expect(items[0].warden?.logFile).toBe('b.log'); // newest first
    expect(items.every((i) => i.source === 'warden')).toBe(true);
  });

  it('is a no-op for the warden source when none are passed (back-compat)', () => {
    expect(inboxFeed([], [])).toEqual([]);
  });
});

describe('agent Needs-you reports', () => {
  const report: AgentReport = {
    agentId: 'agt_steward', agentName: 'Board Steward', runId: 'run_1',
    at: '2026-08-21T10:00:00Z', title: 'Needs you — I did not resume it',
    preview: 'wft_6ed95586 needs a decision', items: [{ taskId: 'wft_6ed95586', decision: 'decide-next-step' }],
  };

  it('maps the durable server report into the decision inbox source', () => {
    const item = agentReportToInbox(report);
    expect(item.source).toBe('agentreport');
    expect(item.sender).toBe('Board Steward');
    expect(item.needsYou).toBe(true);
    expect(item.unread).toBe(true);
    expect(item.agentReport).toBe(report);
  });

  it('treats a pending report from an older daemon zero timestamp as unread', () => {
    const legacyPayload = { ...report, ackedAt: '0001-01-01T00:00:00Z' } as AgentReport;
    expect(agentReportToInbox(legacyPayload).unread).toBe(true);
  });

  it('merges agent reports newest-first with the other inbox sources', () => {
    const items = inboxFeed([], [], [run({ at: '2026-08-20T09:00:00Z' })], [report]);
    expect(items[0].source).toBe('agentreport');
    expect(items).toHaveLength(2);
  });

  it('keeps a volume of agent decisions uniquely keyed and interleaved newest-first', () => {
    const reports = Array.from({ length: 10 }, (_, index): AgentReport => ({
      ...report,
      agentId: `agt_steward_${index}`,
      runId: `run_${index}`,
      at: `2026-08-21T10:${String(index).padStart(2, '0')}:00Z`,
    }));
    const items = inboxFeed(
      [],
      [{
        number: 42, title: 'Feedback decision', event: 'needs-info', message: 'Please clarify',
        at: '2026-08-21T10:05:30Z',
      }],
      [run({ at: '2026-08-21T10:07:30Z', logFile: 'interleaved.log' })],
      reports,
      20,
    );

    expect(items.map((item) => item.at)).toEqual([...items.map((item) => item.at)].sort().reverse());
    expect(new Set(items.map((item) => item.key)).size).toBe(items.length);
    expect(items.filter((item) => item.source === 'agentreport').map((item) => item.key)).toEqual([
      'agentreport:agt_steward_9:run_9',
      'agentreport:agt_steward_8:run_8',
      'agentreport:agt_steward_7:run_7',
      'agentreport:agt_steward_6:run_6',
      'agentreport:agt_steward_5:run_5',
      'agentreport:agt_steward_4:run_4',
      'agentreport:agt_steward_3:run_3',
      'agentreport:agt_steward_2:run_2',
      'agentreport:agt_steward_1:run_1',
      'agentreport:agt_steward_0:run_0',
    ]);
  });
});

describe('wardenRead ledger', () => {
  it('prunes read-marks for runs that no longer exist', () => {
    const a = run({ logFile: 'a.log' });
    const b = run({ logFile: 'b.log' });
    __resetWardenReadForTests([]);
    markWardenRunRead(a);
    markWardenRunRead(b);
    // Only b still exists → a's mark is pruned.
    pruneWardenRead([b]);
    expect(isWardenRunRead(b)).toBe(true);
    expect(isWardenRunRead(a)).toBe(false);
  });
});
