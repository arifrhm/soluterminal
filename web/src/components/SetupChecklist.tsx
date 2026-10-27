import { useEffect, useId, useRef, useState } from 'react';
import { usePhoneAccessAllowed, useOperatorFeatures } from '../entitlements';
import { useFocusTrap } from './useFocusTrap';
import { useStore } from '../store';
import { useCloud } from '../cloud';
import { api, type MuseBootstrapStatus } from '../api';
import type { Health } from '../types';
import { isDesktopShell, usePhoneAccessStatus } from '../cloudPair';
import { useClaudeInstall, ClaudeInstallConfirmCard } from '../claudeBootstrap';
import { useCodexInstall } from '../codexBootstrap';
import { useMuseInstall, MuseInstallConfirmCard } from '../museBootstrap';
import { subscribeDaemonHealthState } from '../lib/useDaemonHealth';
import { canSetUpWslHere, readBackendGlobal, requestWslSetup } from '../wslSetup';
import {
  accountRow,
  agentReady,
  agentRow,
  whereAgentsRunRow,
  type AgentState,
  type ChecklistRow,
  type RowStatus,
} from './setupChecklistRows';

interface Props {
  open: boolean;
  onClose: () => void;
}

// The Account row's detail. A relayed client is reached through the cloud.
export function launchModeDetail(
  mode: 'cloud' | 'local' | 'detecting',
  { signedIn, relayed, phone }: { signedIn: boolean; relayed: boolean; phone: boolean },
): string {
  if (mode === 'detecting') return 'Checking…';
  if (mode !== 'local' && relayed) return 'Signed in, connected to your computer through the cloud';
  if (mode === 'local' || !signedIn) {
    return 'Talk instead of typing, see your agents on your phone, get a push when one needs you. Everything on this PC works without one.';
  }
  return phone ? 'Signed in. You can reach this computer from your phone.' : 'Signed in';
}

const DOT_COLOR: Record<RowStatus, string> = {
  ok: 'var(--ok)',
  warn: 'var(--warn)',
  pending: 'var(--fg-dim)',
  bad: 'var(--danger)',
  optional: 'var(--border)',
};

const DOT_GLYPH: Record<RowStatus, string> = {
  ok: '✓',
  warn: '!',
  pending: '…',
  bad: '✕',
  optional: '–',
};

type Probe<T> = T | 'unsupported' | null;

// Health → per-agent state. Older daemons omit an agent's fields entirely:
// 'unsupported' drops the row rather than stranding it on "Checking…".
function claudeFromHealth(h: Health): Probe<AgentState> {
  if (h.claudeChecked === undefined) return 'unsupported';
  return { checked: !!h.claudeChecked, installed: !!h.claudeInstalled, version: h.claudeVersion ?? '', authed: !!h.claudeAuthed };
}

function codexFromHealth(h: Health): Probe<AgentState> {
  if (h.codexChecked === undefined) return 'unsupported';
  return { checked: !!h.codexChecked, installed: !!h.codexInstalled, version: h.codexVersion ?? '', authed: !!h.codexAuthed };
}

export function SetupChecklist({ open, onClose }: Props) {
  const cardRef = useRef<HTMLDivElement>(null);
  const headingRef = useRef<HTMLHeadingElement>(null);
  const titleId = useId();
  useFocusTrap(cardRef, open);
  useEffect(() => { if (open) headingRef.current?.focus(); }, [open]);
  const cloudMode = useCloud((s) => s.mode);
  const signedIn = useCloud((s) => !s.withoutAccount && !!s.session);
  const relayed = useCloud((s) => !s.isLocal);
  const accountOptional = useCloud((s) => s.accountOptional);
  const requestSignIn = useCloud((s) => s.requestSignIn);
  // Muse Code is an internal runtime: team installs only.
  const team = useOperatorFeatures();
  const daemonOnline = useStore((s) => s.daemonOnline);
  const setPhoneAccessOpen = useStore((s) => s.setPhoneAccessOpen);

  // An install keeps the checklist open so its row can turn green in place
  // (the installer needs no typing). Signing in closes it: the agent asks
  // questions in its pane.
  const [installingAgent, setInstallingAgent] = useState<{ claude?: boolean; codex?: boolean }>({});
  const markInstalling = (agent: 'claude' | 'codex') => {
    setInstallingAgent((s) => ({ ...s, [agent]: true }));
    // The post-install poll gives up after ~9 min; so does "Installing…", so a
    // failed install offers "Review install" again.
    window.setTimeout(() => setInstallingAgent((s) => ({ ...s, [agent]: false })), 9 * 60_000);
  };
  const claudeFlow = useClaudeInstall({
    onSpawn: (kind) => (kind === 'install' ? markInstalling('claude') : onClose()),
  });
  const codexFlow = useCodexInstall({
    onSpawn: (kind) => (kind === 'install' ? markInstalling('codex') : onClose()),
  });
  const {
    installing: museInstalling,
    pendingInstall: musePendingInstall,
    startInstall: startMuseInstall,
    confirmInstall: confirmMuseInstall,
    cancelInstall: cancelMuseInstall,
    loginMuse,
  } = useMuseInstall({ onSpawn: onClose });

  const [health, setHealth] = useState<Health | null>(null);
  const [claude, setClaude] = useState<Probe<AgentState>>(null);
  const [codex, setCodex] = useState<Probe<AgentState>>(null);
  const [muse, setMuse] = useState<MuseBootstrapStatus | 'unsupported' | null>(null);
  useEffect(() => {
    if (!open) return;
    let cancelled = false;
    const applyHealth = (h: Health) => {
      setHealth(h);
      setClaude(claudeFromHealth(h));
      setCodex(codexFromHealth(h));
      if (h.museChecked === undefined) {
        setMuse('unsupported');
      } else {
        setMuse({
          museChecked: !!h.museChecked,
          museInstalled: !!h.museInstalled,
          museVersion: h.museVersion ?? '',
          museAuthed: !!h.museAuthed,
        });
      }
    };
    void api.health().then((h) => { if (!cancelled) applyHealth(h); }).catch(() => { /* rows fall back */ });
    // Force FRESH probes on open: /api/health returns a verdict cached up to
    // ~5 min. 403 over the relay or 404 on an older daemon keeps the health
    // value above.
    void api.claudeRecheck().then((s) => {
      if (!cancelled) setClaude({ checked: s.claudeChecked, installed: s.claudeInstalled, version: s.claudeVersion, authed: s.claudeAuthed });
    }).catch(() => { /* keep health value */ });
    void api.codexRecheck().then((s) => {
      if (!cancelled) setCodex({ checked: s.codexChecked, installed: s.codexInstalled, version: s.codexVersion, authed: s.codexAuthed });
    }).catch(() => { /* keep health value */ });
    if (team) void api.museRecheck().then((s) => { if (!cancelled) setMuse(s); }).catch(() => { /* keep health value */ });
    // Live ticks: the post-install rechecks push fresh health through the
    // shared poll; follow it while open.
    const unsubscribe = subscribeDaemonHealthState((st) => {
      if (!cancelled && st.health) applyHealth(st.health);
    });
    return () => { cancelled = true; unsubscribe(); };
  }, [open, team]);

  // Phone pairing is desktop-shell only — usePhoneAccessStatus internally
  // no-ops (returns null) outside the shell, so the row is simply omitted
  // below. enabled:open avoids polling while the overlay is closed.
  const desktopShell = isDesktopShell();
  const phoneAllowed = usePhoneAccessAllowed();
  const { status: pairStatus } = usePhoneAccessStatus({ enabled: open });

  if (!open) return null;

  const rows: ChecklistRow[] = [];

  // The service row only appears when it is down; while this overlay opens
  // only when online, the flag can still flip mid-session.
  if (!daemonOnline) {
    rows.push({
      key: 'daemon',
      label: 'SoluTerminal service',
      detail: 'Not reachable. Restart SoluTerminal from the tray icon.',
      status: 'bad',
    });
  }

  // Agents. Each row is re-probed on open, then follows the shared poll.
  if (claude !== 'unsupported') {
    rows.push(agentRow('claude', claude, {
      installing: !!installingAgent.claude,
      preparing: claudeFlow.installing,
      onInstall: claudeFlow.startInstall,
      onLogin: claudeFlow.loginClaude,
    }));
  }
  if (codex !== 'unsupported') {
    rows.push(agentRow('codex', codex, {
      installing: !!installingAgent.codex,
      preparing: codexFlow.installing,
      onInstall: codexFlow.startInstall,
      onLogin: codexFlow.loginCodex,
    }));
  }

  // Muse Code CLI — the team workflow runtime.
  if (team && muse !== 'unsupported') {
    if (muse === null || !muse.museChecked) {
      rows.push({ key: 'muse', label: 'Muse Code', detail: 'Checking…', status: 'pending' });
    } else if (!muse.museInstalled) {
      rows.push({
        key: 'muse',
        label: 'Muse Code',
        detail: 'Not installed — Muse workflow runs need the `muse` CLI on this machine',
        status: 'bad',
        fix: { label: museInstalling ? 'Preparing…' : 'Review install', onClick: startMuseInstall },
      });
    } else if (!muse.museAuthed) {
      rows.push({
        key: 'muse',
        label: 'Muse Code',
        detail: `Installed${muse.museVersion ? ` (${muse.museVersion})` : ''} — not signed in`,
        status: 'warn',
        fix: { label: 'Sign in', onClick: loginMuse },
      });
    } else {
      rows.push({
        key: 'muse',
        label: 'Muse Code',
        detail: `Ready${muse.museVersion ? ` (${muse.museVersion})` : ''} — installed and signed in`,
        status: 'ok',
      });
    }
  }

  const where = whereAgentsRunRow({
    backend: readBackendGlobal(),
    healthBackendID: health?.backendID,
    paneEnvironment: health?.paneEnvironment,
    shellPlatform: health?.shellPlatform,
    onSetUpWsl: canSetUpWslHere() ? () => { void requestWslSetup(); } : undefined,
  });
  if (where) rows.push(where);

  const canSignIn = cloudMode === 'cloud' && accountOptional && !signedIn && !relayed;
  rows.push(accountRow(
    cloudMode,
    launchModeDetail(cloudMode, { signedIn, relayed, phone: phoneAllowed }),
    signedIn,
    cloudMode !== 'local' && relayed,
    canSignIn ? { label: 'Create free account', onClick: () => { onClose(); requestSignIn(); } } : undefined,
  ));

  // Phone pairing (optional). Desktop-shell only.
  if (desktopShell && phoneAllowed) {
    const paired = pairStatus?.relayState === 'online';
    const pairing = pairStatus?.pairing?.active === true;
    rows.push({
      key: 'pairing',
      label: 'Phone pairing',
      detail: paired
        ? 'Paired — reach this machine from your phone'
        : pairing
          ? 'Pairing in progress…'
          : 'Optional — pair to use your phone',
      status: paired ? 'ok' : pairing ? 'pending' : 'warn',
      fix: paired
        ? undefined
        : {
            label: pairing ? 'Open' : 'Pair',
            onClick: () => { onClose(); setPhoneAccessOpen(true); },
          },
    });
  }

  // One working agent is enough to start.
  const anyAgentReady = agentReady(claude) || agentReady(codex);
  const showContinue = claude !== 'unsupported' || codex !== 'unsupported';

  return (
    <div className="dialog-backdrop" onMouseDown={onClose}>
      <div
        ref={cardRef}
        className="dialog"
        role="dialog"
        aria-modal="true"
        aria-labelledby={titleId}
        onKeyDown={(e) => {
          e.stopPropagation();
          if (e.key === 'Escape') { e.preventDefault(); onClose(); }
        }}
        onMouseDown={(e) => e.stopPropagation()}
        style={{ width: 480, maxWidth: '92vw' }}
      >
        <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between' }}>
          <h2 id={titleId} ref={headingRef} tabIndex={-1} style={{ margin: 0, fontSize: 16 }}>Setup checklist</h2>
          <button
            type="button"
            onClick={onClose}
            title="Close (Esc)"
            aria-label="Close"
            style={{ fontSize: 11, padding: '2px 8px' }}
          >Close</button>
        </div>
        <div style={{ marginTop: 4, marginBottom: 4, fontSize: 12, color: 'var(--fg-dim)', lineHeight: 1.5 }}>
          You need Claude Code or Codex to start. This list updates by itself.
        </div>
        {claudeFlow.pendingInstall && (
          <ClaudeInstallConfirmCard
            spec={claudeFlow.pendingInstall}
            onConfirm={claudeFlow.confirmInstall}
            onCancel={claudeFlow.cancelInstall}
          />
        )}
        {codexFlow.pendingInstall && (
          <ClaudeInstallConfirmCard
            agentName="Codex"
            spec={codexFlow.pendingInstall}
            onConfirm={codexFlow.confirmInstall}
            onCancel={codexFlow.cancelInstall}
          />
        )}
        {musePendingInstall && (
          <MuseInstallConfirmCard
            spec={musePendingInstall}
            onConfirm={confirmMuseInstall}
            onCancel={cancelMuseInstall}
          />
        )}
        <div style={{ display: 'flex', flexDirection: 'column', gap: 10, marginTop: 6 }}>
          {rows.map((r) => (
            <div
              key={r.key}
              data-row={r.key}
              style={{ display: 'flex', alignItems: 'flex-start', gap: 10 }}
            >
              <span
                aria-hidden
                style={{
                  flex: '0 0 auto',
                  width: 16,
                  height: 16,
                  marginTop: 2,
                  borderRadius: '50%',
                  display: 'inline-flex',
                  alignItems: 'center',
                  justifyContent: 'center',
                  fontSize: 10,
                  fontWeight: 700,
                  color: r.status === 'optional' ? 'var(--fg-dim)' : 'var(--accent-fg)',
                  background: DOT_COLOR[r.status],
                }}
              >{DOT_GLYPH[r.status]}</span>
              <div style={{ flex: '1 1 auto', minWidth: 0 }}>
                <div style={{ fontSize: 13, fontWeight: 600 }}>
                  {r.label}
                  {r.badge && (
                    <span
                      style={{
                        marginLeft: 6,
                        fontSize: 10,
                        fontWeight: 500,
                        color: 'var(--fg-dim)',
                        border: '1px solid var(--border)',
                        borderRadius: 8,
                        padding: '0 6px',
                      }}
                    >{r.badge}</span>
                  )}
                </div>
                <div style={{ fontSize: 11, color: 'var(--fg-dim)', lineHeight: 1.45 }}>{r.detail}</div>
              </div>
              {r.fix && (
                <button
                  type="button"
                  onClick={r.fix.onClick}
                  style={{ flex: '0 0 auto', fontSize: 11, padding: '2px 8px' }}
                >{r.fix.label}</button>
              )}
            </div>
          ))}
        </div>
        {showContinue && (
          <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'flex-end', gap: 10, marginTop: 14 }}>
            {!anyAgentReady && (
              <div style={{ flex: '1 1 auto', fontSize: 11, color: 'var(--fg-dim)', lineHeight: 1.45 }}>
                Continue unlocks when Claude Code or Codex is ready. You can also close this and set them up later.
              </div>
            )}
            <button
              type="button"
              className="primary"
              onClick={onClose}
              disabled={!anyAgentReady}
              style={{ flex: '0 0 auto', fontSize: 12, padding: '4px 14px' }}
            >Continue</button>
          </div>
        )}
      </div>
    </div>
  );
}
