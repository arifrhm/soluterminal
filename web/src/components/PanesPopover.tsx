import { useState } from 'react';
import { useShallow } from 'zustand/shallow';
import { useStore } from '../store';
import { confirmDialog } from '../lib/dialog';
import { FooterPopover } from './FooterPopover';

// Panes-overview popover for the status bar's panes chip. Two zones:
//
//   1. The running · exited breakdown the old "39/55 panes" text encoded
//      but never explained.
//   2. Bulk cleanup: exited panes have no other batch-close path — pre-fix
//      you closed sixteen dead panes one ✕ at a time. Confirmed first
//      (their scrollback is discarded), then killed sequentially: killPane
//      is optimistic-with-rollback per pane, so a mid-batch failure leaves
//      the survivors visible and retryable rather than half-vanished.
//
// "Who needs me?" is deliberately NOT here — that's the topbar attention
// bell's job (desktop) / MobileTabBar's (mobile). This popover is purely
// pane population + cleanup, so the two don't duplicate the same list.
export function PanesPopover({
  anchor,
  onClose,
}: {
  anchor: HTMLElement;
  onClose: () => void;
}) {
  const { running, exited } = useStore(
    useShallow((s) => {
      let running = 0;
      let exited = 0;
      for (const id in s.panes) {
        if (s.panes[id].status === 'running') running++;
        else exited++;
      }
      return { running, exited };
    }),
  );
  const [closing, setClosing] = useState<{ done: number; total: number } | null>(null);

  const closeExited = async () => {
    const s = useStore.getState();
    const ids = Object.values(s.panes)
      .filter((p) => p.status === 'exited')
      .map((p) => p.id);
    if (ids.length === 0) return;
    const ok = await confirmDialog({
      title: `Close ${ids.length} exited pane${ids.length === 1 ? '' : 's'}?`,
      message:
        'The processes already ended — this removes their tiles and scrollback from every workspace.',
      confirmLabel: 'Close all',
      danger: true,
    });
    if (!ok) return;
    setClosing({ done: 0, total: ids.length });
    let closed = 0;
    for (const id of ids) {
      // Sequential on purpose: killPane snapshots + rolls back per pane and
      // refreshes workspaces after each DELETE — racing sixteen of those
      // interleaves the rollback snapshots.
      if (await useStore.getState().killPane(id, { silent: true })) closed++;
      setClosing({ done: closed, total: ids.length });
    }
    setClosing(null);
    const st = useStore.getState();
    if (closed === ids.length) {
      st.toast(`Closed ${closed} exited pane${closed === 1 ? '' : 's'}`, { kind: 'success' });
    } else {
      st.toast(`Closed ${closed}/${ids.length} exited panes — the rest survived, retry from here`, {
        kind: 'warn',
        icon: '⚠',
        ttlMs: 5000,
      });
    }
  };

  return (
    <FooterPopover anchor={anchor} onClose={onClose} ariaLabel="Panes overview" className="panes-popover">
      <div className="vp-section pp-counts">
        <span>
          {running} running{exited > 0 ? ` · ${exited} exited` : ''}
        </span>
      </div>
      {exited > 0 && (
        <button
          type="button"
          className="pp-close-exited"
          disabled={!!closing}
          onClick={() => void closeExited()}
        >
          {closing
            ? `Closing… ${closing.done}/${closing.total}`
            : `Close ${exited} exited pane${exited === 1 ? '' : 's'}`}
        </button>
      )}
    </FooterPopover>
  );
}
