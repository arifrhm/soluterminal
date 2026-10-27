import { useEffect, useRef, useState } from 'react';
import { useStore } from '../../store';
import { api } from '../../api';
import { SettingsGroup, SettingsCheck, SettingsRow } from './SettingsRow';
import { relativeTime } from '../../native/nativeTranscript';
import type { AriaAgent, AriaResponderSettings, AriaResponderStatus, PaneInfo } from '../../types';

// The Aria agent tab: run your Aria comms-gateway agents from THIS machine on
// your Claude subscription, instead of (not alongside!) Aria's hosted
// auto-reply. One card per agent key — Aria allows up to ten per account,
// each with its own @handle/persona/answering mode — and each card routes to
// its own pane (or fresh headless runs). The status line on every card is
// the feedback loop: a dead key, a wrong Aria-side mode, or a gateway outage
// shows HERE, not only in daemon.log.

const STATUS_POLL_MS = 5000;

type SaveBody = Partial<{
  id: string;
  enabled: boolean;
  agentKey: string;
  gatewayUrl: string;
  permission: string;
  model: string;
  paneId: string;
}>;

function statusLine(st: AriaResponderStatus, enabled: boolean, nowTick: number): { dot: string; text: string } {
  const handle = st.handle ? `@${st.handle}` : 'your agent';
  switch (st.state) {
    case 'listening': {
      let text = `Listening as ${handle}`;
      if (st.lastPollAt) text += ` · checked ${relativeTime(Date.parse(st.lastPollAt), nowTick)}`;
      if (st.lastReplyAt) text += ` · last answered ${relativeTime(Date.parse(st.lastReplyAt), nowTick)}`;
      if (st.queueLen > 0) text += ` · ${st.queueLen} waiting for the pane`;
      return { dot: 'ok', text };
    }
    case 'standby':
      return {
        dot: 'dim',
        text:
          st.mode === 'hosted'
            ? `Standing by — Aria's cloud responder answers ${handle} right now`
            : `Standing by — the key's answering mode is “Nobody”`,
      };
    case 'auth-failed':
      return { dot: 'err', text: 'The gateway rejected the key — it was likely rotated or disabled in Aria' };
    case 'gateway-down':
      return { dot: 'warn', text: 'Gateway unreachable — retrying with backoff' };
    default:
      return { dot: 'dim', text: enabled ? 'Starting…' : 'Off' };
  }
}

// A legacy daemon (endpoint, but no `agents` array) still edits its single
// agent through the same card — saves just omit `id`.
function agentsOf(s: AriaResponderSettings): AriaAgent[] {
  if (s.agents) return s.agents;
  if (!s.hasKey && !s.enabled) return [];
  return [
    {
      id: '',
      enabled: s.enabled,
      hasKey: s.hasKey,
      keyPrefix: s.keyPrefix,
      handle: s.handle,
      gatewayUrl: s.gatewayUrl,
      permission: s.permission,
      model: s.model,
      paneId: s.paneId,
      mcpName: 'aria-comms',
      status: s.status,
    },
  ];
}

function AgentCard({
  agent,
  paneList,
  panes,
  nowTick,
  onSave,
  onDelete,
}: {
  agent: AriaAgent;
  paneList: PaneInfo[];
  panes: Record<string, PaneInfo>;
  nowTick: number;
  onSave: (body: SaveBody) => Promise<AriaResponderSettings | null>;
  onDelete?: () => void;
}) {
  const toast = useStore((s) => s.toast);
  const [keyDraft, setKeyDraft] = useState('');
  const [confirmDelete, setConfirmDelete] = useState(false);
  const confirmTimer = useRef<ReturnType<typeof setTimeout> | null>(null);
  useEffect(() => () => {
    if (confirmTimer.current) clearTimeout(confirmTimer.current);
  }, []);

  const st = agent.status;
  const line = st ? statusLine(st, agent.enabled, nowTick) : null;
  const paneGone = !!agent.paneId && !panes[agent.paneId];
  const title = agent.handle ? `@${agent.handle}` : agent.hasKey ? (agent.keyPrefix ?? 'Agent') + '…' : 'Agent';

  const saveKey = async () => {
    const next = await onSave({ agentKey: keyDraft.trim() });
    if (!next) return;
    setKeyDraft('');
    const saved = agent.id
      ? next.agents?.find((a) => a.id === agent.id)
      : (next.agents?.[0] ?? next);
    const v = saved?.verify ?? next.verify;
    if (v?.ok) {
      const chats = v.grantedChats === 1 ? '1 chat granted' : `${v.grantedChats} chats granted`;
      const mode =
        v.mode === 'self' ? '' : ' — set its mode to “My computer” in Aria so this machine answers';
      toast(`Connected as @${v.handle ?? '?'} · ${chats}${mode}`, { ttlMs: 6000 });
    } else if (v) {
      toast(v.error ?? 'Saved, but could not verify with the gateway', { kind: 'error', icon: '!', ttlMs: 6000 });
    } else {
      toast('Agent key saved', { ttlMs: 3000 });
    }
  };

  return (
    <SettingsGroup title={title}>
      {agent.hasKey && line && (
        <div className="aria-status" role="status">
          <span className={`aria-status-dot ${line.dot}`} aria-hidden />
          <span className="aria-status-text">{line.text}</span>
          {st?.state === 'auth-failed' && st.lastError && (
            <span className="aria-status-err" title={st.lastError}>
              {st.lastError}
            </span>
          )}
        </div>
      )}
      <SettingsRow
        label="Agent key"
        hint={
          agent.hasKey
            ? `Key on file: ${agent.keyPrefix ?? ''}… — paste a new one to replace it.`
            : 'Paste the ariak_… key from Aria → Settings → Agents (shown once at creation).'
        }
      >
        <span className="aria-key-controls">
          <input
            type="password"
            className="settings-input aria-key-input"
            placeholder="ariak_…"
            value={keyDraft}
            onChange={(e) => setKeyDraft(e.target.value)}
          />
          <button
            type="button"
            className="settings-btn"
            disabled={keyDraft.trim() === ''}
            onClick={() => void saveKey()}
          >
            Save key
          </button>
        </span>
      </SettingsRow>
      <SettingsCheck
        label="Answer mentions from this machine"
        checked={agent.enabled}
        onChange={(v) => void onSave({ enabled: v })}
        hint="Off by default. When on, the daemon watches the Aria gateway and answers whenever someone @mentions this agent in a chat you've made readable. Needs this machine (and daemon) running."
      />
      <SettingsRow
        label="Permission"
        hint="What the reply run may do besides the gateway tools. Plan = read-only (recommended); Edit can also write files in the daemon's data dir."
      >
        <select
          className="settings-select"
          value={agent.permission}
          onChange={(e) => void onSave({ permission: e.target.value })}
        >
          <option value="plan">Plan (read-only)</option>
          <option value="edit">Edit</option>
        </select>
      </SettingsRow>
      <SettingsRow
        label="Answer with"
        hint="Fresh headless run = clean, always available. A live pane answers with that session's full context — this agent's MCP is registered automatically when you save its key, but a pane whose Claude was already running needs a restart there (/exit, then claude) to pick it up. If the pane is mid-task, mentions queue and it answers as soon as it's free; only a pane that's gone (or a mention waiting 30+ min) is covered by a fresh run, which says so in its reply."
      >
        <select
          className="settings-select"
          value={agent.paneId ?? ''}
          onChange={(e) => void onSave({ paneId: e.target.value })}
        >
          <option value="">Fresh headless run</option>
          {paneGone && (
            <option value={agent.paneId}>Closed pane — answering with fresh runs until re-pointed</option>
          )}
          {paneList.map((p) => (
            <option key={p.id} value={p.id}>
              {p.title || p.cmd.join(' ') || p.id}
            </option>
          ))}
        </select>
      </SettingsRow>
      <SettingsRow label="Model" hint="Model for reply runs. Account default is fine; Haiku is fastest.">
        <select
          className="settings-select"
          value={agent.model ?? ''}
          onChange={(e) => void onSave({ model: e.target.value })}
        >
          <option value="">Account default</option>
          <option value="claude-haiku-4-5-20251001">Haiku (fast, cheap)</option>
          <option value="claude-sonnet-5">Sonnet</option>
        </select>
      </SettingsRow>
      {onDelete && (
        <SettingsRow
          label="Remove"
          hint="Stops the responder for this agent, tells its queued chats, and removes its MCP registration. The key itself lives on in Aria."
        >
          <button
            type="button"
            className="settings-btn aria-remove-btn"
            onClick={() => {
              if (!confirmDelete) {
                setConfirmDelete(true);
                confirmTimer.current = setTimeout(() => setConfirmDelete(false), 3000);
                return;
              }
              onDelete();
            }}
          >
            {confirmDelete ? 'Really remove?' : 'Remove agent'}
          </button>
        </SettingsRow>
      )}

      {st && (st.activity?.length ?? 0) > 0 && (
        <div className="aria-activity-wrap">
          <p className="aria-activity-title">Recent activity</p>
          <ul className="aria-activity">
            {st.activity.map((a, i) => (
              <li key={`${a.at}-${i}`} className={`aria-activity-row ${a.kind}`}>
                <span className="aria-activity-when">{relativeTime(Date.parse(a.at), nowTick)}</span>
                <span className="aria-activity-kind">{a.kind}</span>
                <span className="aria-activity-detail">
                  {a.chat ? `${a.chat} — ` : ''}
                  {a.detail}
                </span>
              </li>
            ))}
          </ul>
        </div>
      )}
    </SettingsGroup>
  );
}

export function AgentTab() {
  const toast = useStore((s) => s.toast);
  const panes = useStore((s) => s.panes);
  const [s, setS] = useState<AriaResponderSettings | null>(null);
  const [unavailable, setUnavailable] = useState(false);
  const [nowTick, setNowTick] = useState(() => Date.now());
  const [addKeyDraft, setAddKeyDraft] = useState('');

  useEffect(() => {
    let alive = true;
    const load = () =>
      api
        .getAriaResponder()
        .then((v) => {
          if (!alive) return;
          setS(v);
          setNowTick(Date.now());
        })
        .catch(() => {
          if (alive) setUnavailable(true); // older daemon without the endpoint
        });
    void load();
    // Live status while the tab is open — the whole point of the card.
    const t = setInterval(() => void load(), STATUS_POLL_MS);
    return () => {
      alive = false;
      clearInterval(t);
    };
  }, []);

  const save = async (body: SaveBody): Promise<AriaResponderSettings | null> => {
    try {
      const next = await api.saveAriaResponder(body);
      setS(next);
      return next;
    } catch (e) {
      toast(`Couldn't save: ${(e as Error).message}`, { kind: 'error', icon: '!', ttlMs: 6000 });
      return null;
    }
  };

  if (unavailable) {
    return (
      <SettingsGroup title="Aria agent">
        <p className="settings-row-hint">
          This daemon build doesn't have the Aria agent responder yet — update soluterminal and reopen
          Settings.
        </p>
      </SettingsGroup>
    );
  }
  if (!s) {
    return (
      <SettingsGroup title="Aria agent">
        <p className="settings-row-hint">Loading…</p>
      </SettingsGroup>
    );
  }

  const agents = agentsOf(s);
  const multiAgent = !!s.agents; // pre-multi-agent daemons get one card, no add/remove
  const paneList = Object.values(panes)
    // Stable alphabetical order — the store re-keys panes on every
    // daemon event, so unsorted Object.values() shuffles the list
    // under the user's cursor mid-selection.
    .sort(
      (a, b) =>
        (a.title || a.cmd.join(' ') || a.id).localeCompare(b.title || b.cmd.join(' ') || b.id) ||
        a.id.localeCompare(b.id),
    );

  const addAgent = async () => {
    const body: SaveBody =
      agents.length === 0 ? { agentKey: addKeyDraft.trim() } : { id: 'new', agentKey: addKeyDraft.trim() };
    const next = await save(body);
    if (!next) return;
    setAddKeyDraft('');
    const created = next.agents?.find((a) => a.verify) ?? next.agents?.[next.agents.length - 1];
    const v = created?.verify ?? next.verify;
    if (v?.ok) {
      toast(`Connected as @${v.handle ?? '?'} · ${v.grantedChats} chat(s) granted — now enable it below`, {
        ttlMs: 6000,
      });
    } else if (v && !v.ok) {
      toast(v.error ?? 'Saved, but could not verify with the gateway', { kind: 'error', icon: '!', ttlMs: 6000 });
    }
  };

  return (
    <>
      <SettingsGroup title="Aria agent">
        <p className="settings-row-hint">
          Answer @mentions of your Aria chat agents from this machine, using your Claude subscription
          instead of Aria's hosted (API-billed) auto-reply. Mint keys and grant chats in Aria →
          Settings → Agents (its Terminal pairing can also do this whole setup in one click), and set
          each key's answering mode there to “My computer” — a responder stands by until that mode is
          selected. Each agent answers with its own pane or fresh headless runs.
        </p>
      </SettingsGroup>

      {agents.map((a) => (
        <AgentCard
          key={a.id || 'default'}
          agent={a}
          paneList={paneList}
          panes={panes}
          nowTick={nowTick}
          onSave={(body) => save(a.id ? { id: a.id, ...body } : body)}
          onDelete={
            multiAgent && a.id
              ? () =>
                  void api
                    .deleteAriaAgent(a.id)
                    .then((next) => setS(next))
                    .catch((e) =>
                      toast(`Couldn't remove: ${(e as Error).message}`, { kind: 'error', icon: '!', ttlMs: 6000 }),
                    )
              : undefined
          }
        />
      ))}

      {(multiAgent || agents.length === 0) && (
        <SettingsGroup title={agents.length === 0 ? 'Add your agent' : 'Add another agent'}>
          <SettingsRow
            label="Agent key"
            hint={
              agents.length === 0
                ? 'Paste the ariak_… key from Aria → Settings → Agents to set up your first agent.'
                : 'Each Aria key is its own agent identity (@handle, persona, granted chats) — add one here to answer for it from this machine too, with its own pane.'
            }
          >
            <span className="aria-key-controls">
              <input
                type="password"
                className="settings-input aria-key-input"
                placeholder="ariak_…"
                value={addKeyDraft}
                onChange={(e) => setAddKeyDraft(e.target.value)}
              />
              <button
                type="button"
                className="settings-btn"
                disabled={addKeyDraft.trim() === ''}
                onClick={() => void addAgent()}
              >
                Add agent
              </button>
            </span>
          </SettingsRow>
        </SettingsGroup>
      )}
    </>
  );
}
