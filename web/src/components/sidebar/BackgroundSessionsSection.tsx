import { useRef, useState } from 'react';
import type { PaneInfo } from '../../types';
import { useDismiss } from '../useDismiss';
import { SectionHeader } from './SectionHeader';
import { IconTerminalSquare, IconX } from '../ui/icons';
import { relativeTime } from './time';
import { confirmDialog } from '../../lib/dialog';

interface BackgroundSessionsSectionProps {
  panes: PaneInfo[];
  collapsed: boolean;
  onToggle: () => void;
  onKillAll: () => void;
  onRevealPane: (paneId: string) => void;
  onKillPane: (paneId: string) => void;
}

function abbreviateHome(p: string) {
  return p ? p.replace(/^\/home\/[^/]+/, '~') : p;
}

export function BackgroundSessionsSection({
  panes,
  collapsed,
  onToggle,
  onKillAll,
  onRevealPane,
  onKillPane,
}: BackgroundSessionsSectionProps) {
  // "Kill all" is destructive, so it sits behind a ⋯ menu (and a confirm in
  // onKillAll) rather than as a red button next to the title.
  const [menuOpen, setMenuOpen] = useState(false);
  const menuRef = useRef<HTMLDivElement | null>(null);
  useDismiss(() => setMenuOpen(false), { enabled: menuOpen, inside: [menuRef] });

  if (panes.length === 0) return null;

  return (
    <div className="sidebar-section">
      <SectionHeader
        icon={<IconTerminalSquare size={14} />}
        title="Detached sessions"
        count={panes.length}
        collapsed={collapsed}
        onToggle={onToggle}
        extra={
          <div ref={menuRef} style={{ position: 'relative' }}>
            <button
              type="button"
              className="iconbtn"
              onClick={(e) => { e.stopPropagation(); setMenuOpen((v) => !v); }}
              title="More actions"
              aria-label="Detached sessions actions"
              aria-haspopup="menu"
              aria-expanded={menuOpen}
            >⋯</button>
            {menuOpen && (
              <div
                className="pane-ctx-menu"
                role="menu"
                style={{ position: 'absolute', right: 0, top: '100%', zIndex: 'var(--z-menu)' }}
              >
                <button
                  role="menuitem"
                  className="danger"
                  onClick={() => { setMenuOpen(false); onKillAll(); }}
                >Close all detached sessions…</button>
              </div>
            )}
          </div>
        }
      />
      {!collapsed && (
        <div style={{ fontSize: 10, color: 'var(--fg-dim)', marginBottom: 4 }}>
          Still running, but not in any workspace. Click one to bring it back.
        </div>
      )}
      {!collapsed && panes.map((p) => {
        const shortCwd = abbreviateHome(p.cwd);
        const last = relativeTime(p.lastActivityMs || 0);
        const title = p.title || p.cmd.join(' ');
        const tooltip = [
          `${p.cmd.join(' ')}`,
          `cwd: ${p.cwd}`,
          `pid: ${p.pid}`,
          last ? `last active: ${last}` : null,
        ].filter(Boolean).join('\n');
        return (
          <div key={p.id} className="bg-session-row" title={tooltip}>
            <div
              className="bg-session-main"
              onClick={() => onRevealPane(p.id)}
            >
              <div className="bg-session-title">{title}</div>
              <div className="bg-session-sub">
                <span className="bg-session-cwd">{shortCwd}</span>
                {last && <span className="bg-session-when">· {last}</span>}
                {p.status === 'exited' && p.resumable && (
                  <span className="bg-session-when">· suspended</span>
                )}
              </div>
            </div>
            <button
              className="iconbtn"
              title={`Close "${title}"`}
              onClick={async (e) => {
                e.stopPropagation();
                if (await confirmDialog({ message: `Close "${title}" in ${shortCwd}? Whatever is running in it stops.`, confirmLabel: 'Close', danger: true })) onKillPane(p.id);
              }}
              style={{ padding: '1px 6px', background: 'transparent', border: 'none', display: 'inline-flex' }}
              aria-label={`Close "${title}"`}
            ><IconX size={12} /></button>
          </div>
        );
      })}
    </div>
  );
}
