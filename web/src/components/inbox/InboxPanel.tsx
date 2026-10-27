import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { IconPencil } from '../ui/icons';
import { createPortal } from 'react-dom';
import { api, type FeedbackThread } from '../../api';
import { useStore } from '../../store';
import { runPreview } from '../../lib/reportsFeed';
import { inboxFeed, type InboxItem } from '../../lib/inboxFeed';
import { useFeedbackInbox, markFeedbackEventRead } from '../../lib/useFeedbackInbox';
import { useWardenInbox, notifyWardenInbox } from '../../lib/useWardenInbox';
import { acknowledgeAllInboxReports, acknowledgeInboxReport, useInboxReports } from '../../extensions';
import { markWardenRunRead, markAllWardenRead, wardenRunId } from '../../lib/wardenRead';
import { renderReportMarkdown } from '../../lib/reportMarkdown';
import { relativeTime } from '../sidebar/time';
import { useFocusTrap } from '../useFocusTrap';
import { useDraggableDialog } from '../useDraggableDialog';
import { TaskReportView } from '../ScheduleHistoryDialog';
import { FeedbackComposer } from './FeedbackComposer';
import './inbox.css';

// The inbox — soluterminal's mail, with mail-client anatomy. One centered
// two-pane window on the Settings sizing contract (centered card, header
// drag, corner resize, 88vh cap): the feed list on the left — scheduled-task
// reports + feedback events, needs-you pinned, day-grouped, filterable,
// mark-all-read — and a reading pane on the right that swaps between the
// report reader (TaskReportView), a feedback conversation, and the composer.
// Selection IS opening: click a row or move with ↑/↓ (j/k), and viewing
// marks read — there is no separate "open" step and no dialog stacked on a
// panel (the old slide-over + reader-above-it z dance is gone).
// On phones the window is a full-screen sheet: the list pushes to the
// detail, ‹ Inbox backs out.
//
// Entry points all funnel through the `solu:open-inbox` event (the topbar
// bubble, the footer chip, notification taps, Ctrl+K); the legacy
// `solu:open-reports-feed` event still opens the feed. The host is ALWAYS
// mounted in App — on phones the drawer and status bar may not exist.

type Sel =
  | { kind: 'report'; scheduleId: string; runAt: string }
  | { kind: 'thread'; number: number }
  | { kind: 'warden'; id: string; logFile?: string }
  | { kind: 'agentreport'; id: string; runId: string; logFile?: string }
  | { kind: 'compose' }
  | null;

export function openInbox(view?: 'feed' | 'compose', thread?: number) {
  window.dispatchEvent(new CustomEvent('solu:open-inbox', { detail: { view, thread } }));
}

function selFor(it: InboxItem): Sel {
  if (it.report) {
    return { kind: 'report', scheduleId: it.report.schedule.id, runAt: it.report.run.at };
  }
  if (it.feedback) return { kind: 'thread', number: it.feedback.number };
  if (it.warden) return { kind: 'warden', id: wardenRunId(it.warden), logFile: it.warden.logFile };
  if (it.agentReport) return { kind: 'agentreport', id: it.agentReport.agentId, runId: it.agentReport.runId, logFile: it.agentReport.logFile };
  return null;
}

function itemSelected(it: InboxItem, sel: Sel): boolean {
  if (!sel) return false;
  if (sel.kind === 'report') {
    return !!it.report && it.report.schedule.id === sel.scheduleId && it.report.run.at === sel.runAt;
  }
  if (sel.kind === 'thread') return !!it.feedback && it.feedback.number === sel.number;
  if (sel.kind === 'warden') return !!it.warden && wardenRunId(it.warden) === sel.id;
  if (sel.kind === 'agentreport') return !!it.agentReport && it.agentReport.agentId === sel.id && it.agentReport.runId === sel.runId;
  return false;
}

// Phone-width tracker. jsdom-safe: no matchMedia → desktop.
function useIsPhone(): boolean {
  const [is, setIs] = useState(() =>
    typeof window.matchMedia === 'function' && window.matchMedia('(max-width: 700px)').matches);
  useEffect(() => {
    if (typeof window.matchMedia !== 'function') return;
    const mq = window.matchMedia('(max-width: 700px)');
    const on = () => setIs(mq.matches);
    mq.addEventListener?.('change', on);
    return () => mq.removeEventListener?.('change', on);
  }, []);
  return is;
}

// ---------------------------------------------------------------- rows ----

function InboxRow({ it, isSel, onSelect }: {
  it: InboxItem;
  isSel: boolean;
  onSelect: (it: InboxItem) => void;
}) {
  const failed = it.report?.run.status === 'error' || (it.warden ? it.warden.status !== 'ok' : false);
  const preview = it.report ? runPreview(it.report.run) : it.preview;
  const ref = useRef<HTMLButtonElement | null>(null);
  // Keyboard selection must stay in view (jsdom has no scrollIntoView).
  useEffect(() => {
    if (isSel) ref.current?.scrollIntoView?.({ block: 'nearest' });
  }, [isSel]);
  const when = new Date(it.at).toLocaleString();
  const title = it.source === 'feedback'
    ? `Your report "${it.title}" · ${when} — open the conversation`
    : it.source === 'warden'
      ? `${it.title} · ${when} — open the brief`
      : it.source === 'agentreport'
        ? `${it.sender} · ${when} — open the Needs you report`
      : `${it.sender} · ${when} — open the report`;
  return (
    <button
      ref={ref}
      type="button"
      className={`inbox-row${it.unread ? ' is-unread' : ''}${isSel ? ' is-sel' : ''}`}
      title={title}
      onClick={() => onSelect(it)}
    >
      <span className="inbox-row-gutter">
        {it.unread && <span className="inbox-dot" aria-label="Unread" />}
      </span>
      <span className="inbox-row-main">
        <span className="inbox-row-top">
          <span className="inbox-row-name">{it.source === 'report' || it.source === 'agentreport' ? it.sender : it.title}</span>
          {it.source === 'feedback' && <span className="inbox-row-src">feedback</span>}
          {it.source === 'warden' && <span className="inbox-row-src">warden</span>}
          {it.source === 'agentreport' && <span className="inbox-row-src">agent</span>}
          <span className="inbox-row-when">{relativeTime(new Date(it.at).getTime())}</span>
        </span>
        <span className="inbox-row-sub">
          {failed ? (
            <span className="inbox-badge inbox-badge-error">failed</span>
          ) : it.needsYou ? (
            <span className="inbox-badge inbox-badge-needs">needs you</span>
          ) : it.feedback?.event === 'fixed' ? (
            <span className="inbox-badge inbox-badge-ok">fixed</span>
          ) : null}
          <span className="inbox-row-preview">{preview || 'Report ready'}</span>
        </span>
      </span>
    </button>
  );
}

// ---------------------------------------------------------------- feed ----

// "Today" / "Yesterday" / "Mon 7 Jul" — the day bucket a timestamp renders
// under. Local time on purpose: the reader's sense of "today" is theirs.
function dayLabel(iso: string): string {
  const d = new Date(iso);
  const now = new Date();
  const startOfDay = (x: Date) => new Date(x.getFullYear(), x.getMonth(), x.getDate()).getTime();
  const diffDays = Math.round((startOfDay(now) - startOfDay(d)) / 86400000);
  if (diffDays <= 0) return 'Today';
  if (diffDays === 1) return 'Yesterday';
  return d.toLocaleDateString(undefined, { weekday: 'short', day: 'numeric', month: 'short' });
}

type Filter = 'all' | 'report' | 'feedback' | 'warden' | 'agentreport';

// Needs-you AND unread pins to the top — that's the "act on this" pile.
// Once read it re-files chronologically (a handled ask shouldn't nag).
// This is also the ↑/↓ traversal order: exactly what the eye sees.
function visualOrder(items: InboxItem[], filter: Filter): { pinned: InboxItem[]; rest: InboxItem[] } {
  const shown = filter === 'all' ? items : items.filter((i) => i.source === filter);
  return {
    pinned: shown.filter((i) => i.unread && i.needsYou),
    rest: shown.filter((i) => !(i.unread && i.needsYou)),
  };
}

function FeedView({ items, filter, setFilter, sel, onSelect }: {
  items: InboxItem[];
  filter: Filter;
  setFilter: (f: Filter) => void;
  sel: Sel;
  onSelect: (it: InboxItem) => void;
}) {
  const { pinned, rest } = visualOrder(items, filter);
  const groups: { label: string; items: InboxItem[] }[] = [];
  for (const it of rest) {
    const label = dayLabel(it.at);
    const last = groups[groups.length - 1];
    if (last && last.label === label) last.items.push(it);
    else groups.push({ label, items: [it] });
  }

  const counts = {
    all: items.length,
    report: items.filter((i) => i.source === 'report').length,
    feedback: items.filter((i) => i.source === 'feedback').length,
    warden: items.filter((i) => i.source === 'warden').length,
    agentreport: items.filter((i) => i.source === 'agentreport').length,
  };
  // The filter strip appears only when at least TWO sources have items (one
  // source needs no filter). Tabs are data-driven off the sources present, so
  // adding a source doesn't require touching the render below.
  const present = ([
    { key: 'report', label: 'Reports' },
    { key: 'feedback', label: 'Feedback' },
    { key: 'warden', label: 'Warden' },
    { key: 'agentreport', label: 'Agents' },
  ] as const).filter((s) => counts[s.key] > 0);
  const filterTabs: { key: Filter; label: string }[] = [
    { key: 'all', label: `All ${counts.all}` },
    ...present.map((s) => ({ key: s.key as Filter, label: `${s.label} ${counts[s.key]}` })),
  ];
  const empty = pinned.length === 0 && groups.length === 0;

  return (
    <>
      {present.length >= 2 && (
        <div className="inbox-filters" role="tablist" aria-label="Filter inbox">
          {filterTabs.map((t) => (
            <button
              key={t.key}
              type="button"
              role="tab"
              aria-selected={filter === t.key}
              className={`inbox-filter${filter === t.key ? ' is-active' : ''}`}
              onClick={() => setFilter(t.key)}
            >{t.label}</button>
          ))}
        </div>
      )}
      <div className="inbox-scroll">
        {empty && (
          <div className="inbox-empty">
            <div className="inbox-empty-title">Nothing here yet</div>
            <p>Automations that run an agent file a report every run. Try a
            Morning digest from the Automations recipe shelf.</p>
            <p>Feedback you send reports back here when it needs you or gets
            fixed. Use “New” above to send one.</p>
          </div>
        )}
        {pinned.length > 0 && (
          <div className="inbox-pinned">
            <div className="inbox-day inbox-day-needs">Needs you</div>
            {pinned.map((it) => (
              <InboxRow key={it.key} it={it} isSel={itemSelected(it, sel)} onSelect={onSelect} />
            ))}
          </div>
        )}
        {groups.map((g) => (
          <div key={g.label}>
            <div className="inbox-day">{g.label}</div>
            {g.items.map((it) => (
              <InboxRow key={it.key} it={it} isSel={itemSelected(it, sel)} onSelect={onSelect} />
            ))}
          </div>
        ))}
      </div>
    </>
  );
}

// -------------------------------------------------------------- thread ----

const EVENT_LABEL: Record<string, string> = {
  'filed': 'Report filed',
  'needs-info': 'Needs your info',
  'fixed': 'Fixed',
  'reply': 'You replied',
};

// The Warden brief reader — fetches the run's markdown (api.wardenRunLog) and
// renders it with the same reader as task reports. Simpler than TaskReportView:
// one log file, no run cursor, no meta split (the daemon writes plain report
// markdown). A run with no log file has nothing to open.
function WardenReadView({ logFile }: { logFile?: string }) {
  const toast = useStore((s) => s.toast);
  const [text, setText] = useState<string | null>(null);
  const [err, setErr] = useState<string | null>(null);

  useEffect(() => {
    if (!logFile) return;
    let alive = true;
    setText(null);
    setErr(null);
    api.wardenRunLog(logFile)
      .then((t) => { if (alive) setText(t); })
      .catch((e) => {
        if (!alive) return;
        setErr((e as Error).message || 'Brief unavailable.');
        toast('Brief unavailable — its log may have rotated out.', { kind: 'info', ttlMs: 5000 });
      });
    return () => { alive = false; };
  }, [logFile, toast]);

  if (!logFile) {
    return (
      <div className="inbox-read-empty">
        <div className="inbox-empty-title">No brief to show</div>
        <p>This run produced no readable log.</p>
      </div>
    );
  }
  if (err) {
    return (
      <div className="inbox-read-empty">
        <div className="inbox-empty-title">Brief unavailable</div>
        <p>{err}</p>
      </div>
    );
  }
  return (
    <div className="inbox-scroll">
      {text === null ? (
        <div className="inbox-read-empty"><p>Loading brief…</p></div>
      ) : (
        <div className="report-md report-md-reader">{renderReportMarkdown(text)}</div>
      )}
    </div>
  );
}

function AgentReportReadView({ agentId, logFile }: { agentId: string; logFile?: string }) {
  const toast = useStore((s) => s.toast);
  const [text, setText] = useState<string | null>(null);
  const [err, setErr] = useState<string | null>(null);
  useEffect(() => {
    if (!logFile) return;
    let alive = true;
    setText(null);
    setErr(null);
    api.agentRunLog(agentId, logFile)
      .then((value) => { if (alive) setText(value); })
      .catch((cause) => {
        if (!alive) return;
        setErr((cause as Error).message || 'Report unavailable.');
        toast('Agent report unavailable — its log may have rotated out.', { kind: 'info', ttlMs: 5000 });
      });
    return () => { alive = false; };
  }, [agentId, logFile, toast]);
  if (!logFile || err) {
    return <div className="inbox-read-empty"><div className="inbox-empty-title">Report unavailable</div><p>{err ?? 'This run produced no readable log.'}</p></div>;
  }
  return <div className="inbox-scroll">{text === null
    ? <div className="inbox-read-empty"><p>Loading report…</p></div>
    : <div className="report-md report-md-reader">{renderReportMarkdown(text)}</div>}</div>;
}

function ThreadView({ number }: { number: number }) {
  const toast = useStore((s) => s.toast);
  const [thread, setThread] = useState<FeedbackThread | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [replyText, setReplyText] = useState('');
  const [sending, setSending] = useState(false);
  const scrollRef = useRef<HTMLDivElement | null>(null);

  useEffect(() => {
    let alive = true;
    setThread(null);
    setErr(null);
    api.feedbackThread(number)
      .then((t) => { if (alive) setThread(t); })
      .catch((e) => { if (alive) setErr((e as Error).message || 'Could not load the conversation.'); });
    return () => { alive = false; };
  }, [number]);

  // New content (load or reply) → show the latest.
  useEffect(() => {
    const el = scrollRef.current;
    if (el) el.scrollTop = el.scrollHeight;
  }, [thread?.events.length]);

  async function sendReply() {
    const text = replyText.trim();
    if (!text || sending) return;
    setSending(true);
    try {
      await api.feedbackReply(number, text);
      toast('Reply added — your report will re-triage automatically', { kind: 'success', icon: '✓' });
      setReplyText('');
      // Optimistic append; the server stored the same event.
      setThread((t) => t && ({ ...t, events: [...t.events, { event: 'reply', message: text, at: new Date().toISOString() }] }));
    } catch (e) {
      toast(`Couldn't send reply: ${(e as Error).message || 'try again'}`, { kind: 'error', icon: '⚠', ttlMs: 5000 });
    } finally {
      setSending(false);
    }
  }

  const lastState = [...(thread?.events ?? [])].reverse().find((e) => e.event !== 'reply')?.event;
  const fixed = lastState === 'fixed';

  return (
    <>
      <div className="inbox-scroll inbox-thread" ref={scrollRef}>
        {err && <div className="dialog-error">{err}</div>}
        {!thread && !err && <div className="inbox-empty">Loading…</div>}
        {thread && (
          <>
            <div className="inbox-thread-head">
              <div className="inbox-thread-title">{thread.title}</div>
              <div className="inbox-thread-sub">Report #{thread.number} · filed {relativeTime(new Date(thread.filedAt).getTime())}</div>
            </div>
            {thread.events.map((e, i) => (
              <div key={i} className={`inbox-msg inbox-msg-${e.event === 'reply' ? 'you' : e.event}`}>
                <div className="inbox-msg-meta">
                  {EVENT_LABEL[e.event] ?? e.event} · {relativeTime(new Date(e.at).getTime())}
                </div>
                {e.message && e.message !== 'Report filed' && (
                  <div className="inbox-msg-body">{e.message}</div>
                )}
              </div>
            ))}
            {fixed && (
              <div className="inbox-thread-done">
                This report is fixed — the fix ships in the next update. Something
                still off? File a new report from “New”.
              </div>
            )}
          </>
        )}
      </div>
      {thread && !fixed && (
        <div className="inbox-reply">
          <textarea
            value={replyText}
            onChange={(e) => setReplyText(e.target.value)}
            rows={3}
            placeholder="Add details — this goes straight into your report and re-triage runs automatically."
            onKeyDown={(e) => {
              if (e.key === 'Enter' && (e.metaKey || e.ctrlKey)) { e.preventDefault(); void sendReply(); }
            }}
          />
          <div className="inbox-reply-actions">
            <span className="dialog-help" style={{ margin: 0 }}>Ctrl+Enter to send</span>
            <button
              type="button"
              className="primary"
              disabled={sending || !replyText.trim()}
              onClick={() => void sendReply()}
            >{sending ? 'Sending…' : 'Send reply'}</button>
          </div>
        </div>
      )}
    </>
  );
}

// --------------------------------------------------------------- window ---

function InboxWindow({ sel, setSel, onClose }: {
  sel: Sel;
  setSel: (s: Sel) => void;
  onClose: () => void;
}) {
  const schedules = useStore((s) => s.schedules);
  const feedback = useFeedbackInbox();
  const warden = useWardenInbox();
  const agentReports = useInboxReports();
  const isPhone = useIsPhone();
  const [filter, setFilter] = useState<Filter>('all');
  const items = useMemo(() => inboxFeed(schedules, feedback, warden, agentReports), [schedules, feedback, warden, agentReports]);
  const unread = useMemo(() => items.filter((i) => i.unread), [items]);

  // Settings' window contract: flex-centered by the backdrop, then pinned so
  // the header drags and the corner grip tracks 1:1. Phones skip the pin —
  // the sheet is full-screen and must not nudge to the 8px clamp edge.
  const { cardRef, cardStyle, dragHandleProps } = useDraggableDialog(!isPhone);
  useFocusTrap(cardRef as React.RefObject<HTMLElement | null>, true);

  const select = useCallback((it: InboxItem) => {
    // Viewing marks read — feedback stamps here; reports stamp inside
    // TaskReportView when the run actually renders; warden stamps its
    // client-side ledger here.
    if (it.feedback) markFeedbackEventRead(it.feedback.number);
    if (it.warden && markWardenRunRead(it.warden)) notifyWardenInbox();
    if (it.agentReport) void acknowledgeInboxReport(it.agentReport).catch(() => {});
    setSel(selFor(it));
  }, [setSel]);

  // ↑/↓ (and j/k — mail muscle memory) move the selection; selection IS
  // opening. Esc closes (phone: backs out of the detail first). Movement
  // keys are ignored while typing in the reply box or composer.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') {
        e.preventDefault();
        if (isPhone && sel) setSel(null);
        else onClose();
        return;
      }
      // e.target can be window/document (jsdom fires there; blurred focus in
      // browsers) — neither has closest().
      const t = e.target as HTMLElement | null;
      if (t && typeof t.closest === 'function'
        && t.closest('textarea, input, select, [contenteditable="true"]')) return;
      const down = e.key === 'ArrowDown' || e.key === 'j';
      const up = e.key === 'ArrowUp' || e.key === 'k';
      if (!down && !up) return;
      const { pinned, rest } = visualOrder(items, filter);
      const order = [...pinned, ...rest];
      if (order.length === 0) return;
      e.preventDefault();
      const at = order.findIndex((i) => itemSelected(i, sel));
      const next = down
        ? Math.min(order.length - 1, at + 1)
        : Math.max(0, at === -1 ? 0 : at - 1);
      const it = order[next];
      if (it && !itemSelected(it, sel)) select(it);
    };
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
  }, [isPhone, sel, setSel, items, filter, select, onClose]);

  // Mark everything read: reports stamp their newest run, feedback events
  // stamp per issue — both fire-and-forget with optimistic local state.
  function markAllRead() {
    const stamped = new Set<string>();
    for (const it of unread) {
      if (it.report) {
        const id = it.report.schedule.id;
        const newest = it.report.schedule.runs?.[0];
        if (newest && !stamped.has(id)) {
          stamped.add(id);
          void api.markScheduleRead(id, newest.at).catch(() => {});
        }
      } else if (it.feedback) {
        markFeedbackEventRead(it.feedback.number);
      }
    }
    // Warden runs stamp the client-side ledger in one pass, then notify once.
    const wardenRuns = unread.filter((i) => i.warden).map((i) => i.warden!);
    if (markAllWardenRead(wardenRuns)) notifyWardenInbox();
    void acknowledgeAllInboxReports(unread.filter((i) => i.agentReport).map((i) => i.agentReport!)).catch(() => {});
  }

  const selReport = sel?.kind === 'report' ? sel : null;
  const selSchedule = selReport
    ? schedules.find((s) => s.id === selReport.scheduleId) ?? null
    : null;
  // The list stays mounted on phones too — display:none via .is-detail — so
  // scroll position and filter survive the push/back round trip.
  const showListChrome = !isPhone || !sel;

  return createPortal(
    <div className="dialog-backdrop" onMouseDown={onClose}>
      <div
        ref={cardRef}
        className="dialog inbox-window"
        style={cardStyle}
        role="dialog"
        aria-label="Inbox"
        onMouseDown={(e) => e.stopPropagation()}
      >
        <div
          className={`inbox-header${!isPhone ? ' settings-drag' : ''}`}
          {...(!isPhone ? dragHandleProps : {})}
        >
          {isPhone && sel && (
            <button
              type="button"
              className="reader-btn"
              onClick={() => setSel(null)}
              aria-label="Back to inbox"
            >‹ Inbox</button>
          )}
          <div className="inbox-title">Inbox</div>
          <div className="inbox-header-actions">
            {showListChrome && unread.length > 0 && (
              <button
                type="button"
                className="reader-btn"
                title={`Mark all ${unread.length} read`}
                onClick={markAllRead}
              >Mark all read</button>
            )}
            {showListChrome && sel?.kind !== 'compose' && (
              <button
                type="button"
                className="reader-btn"
                title="Send feedback — report a bug or share an idea"
                aria-label="Send feedback"
                onClick={() => setSel({ kind: 'compose' })}
              ><IconPencil size={12} style={{ verticalAlign: '-2px' }} /> New</button>
            )}
            <button
              type="button"
              className="reader-btn"
              onClick={onClose}
              title="Close (Esc)"
              aria-label="Close"
              style={{ padding: '5px 9px' }}
            >×</button>
          </div>
        </div>
        <div className={`inbox-body${sel ? ' is-detail' : ''}`}>
          <div className="inbox-list">
            <FeedView items={items} filter={filter} setFilter={setFilter} sel={sel} onSelect={select} />
          </div>
          <div className="inbox-read">
            {selReport && selSchedule && (
              <TaskReportView
                // Keyed so selecting another run remounts the reader — its
                // run cursor is initial-state, not a live prop.
                key={`${selReport.scheduleId}|${selReport.runAt}`}
                schedule={selSchedule}
                initialRunAt={selReport.runAt}
                embedded
                onClose={onClose}
              />
            )}
            {sel?.kind === 'thread' && <ThreadView number={sel.number} />}
            {sel?.kind === 'warden' && <WardenReadView key={sel.id} logFile={sel.logFile} />}
            {sel?.kind === 'agentreport' && <AgentReportReadView key={`${sel.id}:${sel.runId}`} agentId={sel.id} logFile={sel.logFile} />}
            {sel?.kind === 'compose' && (
              <div className="inbox-scroll is-flush">
                <FeedbackComposer
                  onDone={onClose}
                  onCancel={() => setSel(null)}
                />
              </div>
            )}
            {(!sel || (selReport && !selSchedule)) && (
              <div className="inbox-read-empty">
                <div className="inbox-empty-title">Nothing open</div>
                <p>Pick something on the left to read it here.</p>
                <p className="inbox-read-keys">↑ ↓ or j / k move the selection</p>
              </div>
            )}
          </div>
        </div>
      </div>
    </div>,
    document.body,
  );
}

// Always-mounted host (App). Also serves `solu:open-reports-feed` (the
// sidebar chip's original event) so older surfaces keep working.
export function InboxHost() {
  const [state, setState] = useState<{ open: boolean; sel: Sel }>({ open: false, sel: null });

  useEffect(() => {
    const onOpen = (e: Event) => {
      const detail = (e as CustomEvent).detail as { view?: 'feed' | 'compose'; thread?: number } | undefined;
      const sel: Sel = detail?.thread
        ? { kind: 'thread', number: detail.thread }
        : detail?.view === 'compose'
          ? { kind: 'compose' }
          : null;
      setState({ open: true, sel });
    };
    window.addEventListener('solu:open-inbox', onOpen);
    window.addEventListener('solu:open-reports-feed', onOpen);
    return () => {
      window.removeEventListener('solu:open-inbox', onOpen);
      window.removeEventListener('solu:open-reports-feed', onOpen);
    };
  }, []);

  if (!state.open) return null;
  return (
    <InboxWindow
      sel={state.sel}
      setSel={(sel) => setState((s) => ({ ...s, sel }))}
      onClose={() => setState({ open: false, sel: null })}
    />
  );
}
