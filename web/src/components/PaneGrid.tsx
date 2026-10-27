import { useEffect, useMemo, useRef, useState } from 'react';
import { Panel, PanelGroup, PanelResizeHandle } from 'react-resizable-panels';
import type { ImperativePanelGroupHandle } from 'react-resizable-panels';
import { SessionClaimOverlay } from './SessionClaimOverlay';
import { leafCount, useStore } from '../store';
import { isMobileViewport } from '../lib/mobile';
import { collectLeaves } from '../lib/layout';
import type { Layout, PaneInfo } from '../types';
import { PaneRecovery } from './PaneRecovery';
import { PaneSlot } from './PaneSlot';
import { NativePane } from './NativePane';
import { BrowserPane } from './BrowserPane';
import { FileTile } from './FileTile';
import { DiffTile } from './DiffTile';

function renderPaneLeaf(pane: PaneInfo, _paneIndex: number) {
 if (pane.recoveryError) return <PaneRecovery key={pane.id + ':r'} pane={pane} />;
 if (pane.kind === 'native') return <NativePane key={pane.id + ':n'} pane={pane} />;
 return <PaneSlot key={pane.id} paneId={pane.id} />;
}

// Mobile breakpoint. Keep in sync with styles.css and App.tsx.
const MOBILE_BREAKPOINT_PX = 800;
function useIsMobile() {
  const [m, setM] = useState(
    isMobileViewport(MOBILE_BREAKPOINT_PX),
  );
  useEffect(() => {
    const on = () => setM(isMobileViewport(MOBILE_BREAKPOINT_PX));
    window.addEventListener('resize', on);
    window.visualViewport?.addEventListener('resize', on);
    return () => {
      window.removeEventListener('resize', on);
      window.visualViewport?.removeEventListener('resize', on);
    };
  }, []);
  return m;
}

function DeadPane({ paneId }: { paneId: string }) {
  const closePaneInLayout = useStore((s) => s.closePaneInLayout);
  return (
    <div className="pane">
      <div className="pane-header">
        <span className="pane-title">Session ended</span>
        <span className="pane-cwd">{paneId}</span>
        <div className="pane-actions">
          <button className="iconbtn" onClick={() => closePaneInLayout(paneId)}>×</button>
        </div>
      </div>
      <div className="pane-empty">
        This session is no longer running. Close this tile and start a new one.
      </div>
    </div>
  );
}

function LayoutNode({ node, paneIndexById }: { node: Layout; paneIndexById: Record<string, number> }) {
  // IMPORTANT: All hooks MUST be called unconditionally at the top of
  // the function, before any early returns. React reconciliation can
  // reuse the SAME LayoutNode instance across renders where `node`
  // changes type (e.g. a split becoming a pane after a close, or
  // positional reuse during workspace-switch reconciliation). If a
  // hook is only called when node.type === 'split', React sees the
  // hook count change and throws Error #310. Keep the refs/effects
  // up here and only CONSUME them in the split branch below.
  const pane = useStore((s) => (node.type === 'pane' ? s.panes[node.paneId] : null));
  const browser = useStore((s) => (node.type === 'browser' ? s.browsers[node.browserId] : null));
  const file = useStore((s) => (node.type === 'file' ? s.files[node.fileId] : null));
  const setLayout = useStore((s) => s.setLayout);
  const presentationActive = useStore(s=>s.mainPresentation.kind === 'workspace');
  const layoutMode = useStore((s) => s.paneLayoutMode);
  const groupRef = useRef<ImperativePanelGroupHandle>(null);

  // In auto mode the on-screen ratio is computed from leaf counts so
  // every cell has the same area, regardless of how the binary tree
  // happens to be nested. File and diff tiles take part in that: they
  // are opened by splitting whichever leaf has focus (stored ratio 0.5),
  // so without equalising they land at half their anchor's slot — ~1/6
  // of the window next to three even columns. Only BROWSERS opt out:
  // that split keeps its stored ratio so the user-chosen browser width
  // (default 0.6/0.4 from openBrowserHere) is preserved, and it stays
  // draggable in auto mode — every other gutter is locked.
  const splitTouchesBrowser = node.type === 'split'
    && (containsBrowserLeaf(node.a) || containsBrowserLeaf(node.b));
  const autoRatio = node.type === 'split'
    ? leafCount(node.a) / Math.max(1, leafCount(node))
    : null;
  const splitRatio = node.type === 'split'
    ? (layoutMode === 'auto' && !splitTouchesBrowser ? autoRatio! : node.ratio)
    : null;
  // Lock the handle only when this split is the equalised kind in
  // auto mode. Browser-touching splits are draggable in both modes.
  const handleLocked = node.type === 'split'
    && layoutMode === 'auto'
    && !splitTouchesBrowser;
  // react-resizable-panels only applies `defaultSize` on initial mount.
  // When a workspace switch changes `node.ratio`, React reuses the same
  // PanelGroup instance in the same tree position — so the library
  // keeps the PREVIOUS workspace's pixel sizes. Without this effect,
  // onLayout would then fire with the stale sizes and overwrite the
  // target workspace's saved ratio with the outgoing workspace's
  // ratio. Imperatively setLayout on the ref matches the component to
  // whatever the store says `splitRatio` is, without remounting panes.
  useEffect(() => {
    if (splitRatio === null) return;
    const target = splitRatio * 100;
    const cur = groupRef.current?.getLayout();
    if (!cur || cur.length < 2) return;
    if (Math.abs(cur[0] - target) < 0.1) return;
    groupRef.current?.setLayout([target, 100 - target]);
  }, [splitRatio, presentationActive]);

  if (node.type === 'pane') {
    return pane
      ? renderPaneLeaf(pane, paneIndexById[pane.id] ?? 0)
      : <DeadPane paneId={node.paneId} />;
  }
  if (node.type === 'browser') {
    return browser ? <BrowserPane info={browser} /> : <div className="pane" />;
  }
  if (node.type === 'file') {
    return file ? <FileTile info={file} /> : <div className="pane" />;
  }
  if (node.type === 'diff') {
    // Diff leaves are self-contained — render straight from the node.
    return <DiffTile leaf={node} />;
  }
  if (node.type === 'tasks') return null;

  const direction = node.direction === 'horizontal' ? 'horizontal' : 'vertical';
  const initialA = (splitRatio ?? node.ratio) * 100;

  const onLayout = (sizes: number[]) => {
    if (!presentationActive || useStore.getState().mainPresentation.kind !== 'workspace') return;
    // Read at event time instead of subscribing: a per-node rootLayout
    // subscription meant every LayoutNode re-ran its selector (and
    // re-rendered) on every drag-frame setLayout, at every tree depth.
    const rootLayout = useStore.getState().layout;
    if (!rootLayout || sizes.length < 2) return;
    // Auto mode + equalised split: the handle is locked so the only
    // "drag" is the library settling pixels at mount time. Skip the
    // save so we don't overwrite the stored ratio that free mode
    // might rely on later.
    if (layoutMode === 'auto' && !splitTouchesBrowser) return;
    const newRatio = sizes[0] / (sizes[0] + sizes[1]);
    // Ignore rounding / pixel-snap drift (<0.5%) — those come from
    // mount-time layout settling and would otherwise save a slightly
    // different ratio than the one on disk, accumulating error over
    // many sessions.
    if (Math.abs(newRatio - node.ratio) < 0.005) return;
    const updated = replaceNode(rootLayout, node, { ...node, ratio: newRatio });
    if (updated !== rootLayout) setLayout(updated);
  };

  return (
    <PanelGroup ref={groupRef} direction={direction} onLayout={onLayout}>
      <Panel defaultSize={initialA} minSize={5}>
        <LayoutNode node={node.a} paneIndexById={paneIndexById} />
      </Panel>
      <PanelResizeHandle disabled={handleLocked} className={handleLocked ? 'pane-resize-handle locked' : undefined} />
      <Panel defaultSize={100 - initialA} minSize={5}>
        <LayoutNode node={node.b} paneIndexById={paneIndexById} />
      </Panel>
    </PanelGroup>
  );
}

// Does this subtree contain a browser leaf? Used by auto mode to decide
// whether a split should be equalised (the default — panes, file, diff
// and tasks tiles alike) or left alone with its stored ratio (the
// browser column, whose width is deliberate).
function containsBrowserLeaf(node: Layout): boolean {
  if (node.type === 'browser') return true;
  if (node.type !== 'split') return false;
  return containsBrowserLeaf(node.a) || containsBrowserLeaf(node.b);
}

function replaceNode(root: Layout, target: Layout, replacement: Layout): Layout {
  if (root === target) return replacement;
  if (root.type === 'split') {
    const a = replaceNode(root.a, target, replacement);
    const b = replaceNode(root.b, target, replacement);
    if (a !== root.a || b !== root.b) return { ...root, a, b };
  }
  return root;
}


export function PaneGrid() {
  const layout = useStore((s) => s.layout);
  const zoomedPaneId = useStore((s) => s.zoomedPaneId);
  const isMobile = useIsMobile();
  // Which leaf to show in "single-leaf" mode. On mobile we always run in
  // this mode (pane switching is via tab bar); on desktop we only hit it
  // when the user explicitly hit Alt+Z to zoom.
  const mobileFallbackId = useStore((s) => {
    const leaves = collectLeaves(s.layout);
    const ids = leaves.flatMap((leaf) => {
      if (leaf.type === 'pane') return [leaf.paneId];
      if (leaf.type === 'browser') return [leaf.browserId];
      if (leaf.type === 'file') return [leaf.fileId];
      if (leaf.type === 'diff') return [leaf.diffId];
      if (leaf.type === 'tasks') return [leaf.tasksId];
      return [];
    });
    if (s.focusedPaneId && ids.includes(s.focusedPaneId)) return s.focusedPaneId;
    return ids[0] ?? null;
  });
  const singleId = zoomedPaneId ?? (isMobile ? mobileFallbackId : null);
  const singlePane = useStore((s) => (singleId ? s.panes[singleId] : null));
  const singleBrowser = useStore((s) => (singleId ? s.browsers[singleId] : null));
  const singleFile = useStore((s) => (singleId ? s.files[singleId] : null));
  // Diff leaves live in the layout, not a map — find the node by id. The
  // returned node is the actual tree object (stable ref while layout is
  // unchanged), so this selector doesn't churn React.
  const singleDiff = useStore((s) => {
    if (!singleId) return null;
    const leaf = collectLeaves(s.layout).find((l) => l.type === 'diff' && l.diffId === singleId);
    return leaf && leaf.type === 'diff' ? leaf : null;
  });
  const paneIndexById = useMemo(() => {
    const out: Record<string, number> = {};
    for (const [idx, leaf] of collectLeaves(layout).entries()) {
      if (leaf.type === 'pane') out[leaf.paneId] = idx + 1;
    }
    return out;
  }, [layout]);
  if (!layout) return null;
  // Render only one leaf — either because of zoom (desktop) or because
  // we're on mobile where split rendering isn't useful on small screens.
  if (singleId) {
    return (
      <div style={{ width: '100%', height: '100%', position: 'relative' }} className="zoomed">
        {singlePane ? renderPaneLeaf(singlePane, paneIndexById[singlePane.id] ?? 0)
          : singleBrowser ? <BrowserPane info={singleBrowser} />
          : singleFile ? <FileTile info={singleFile} />
          : singleDiff ? <DiffTile leaf={singleDiff} />
          : <DeadPane paneId={singleId} />}
        <SessionClaimOverlay />
      </div>
    );
  }
  return (
    <div style={{ width: '100%', height: '100%', position: 'relative' }}>
      <LayoutNode node={layout} paneIndexById={paneIndexById} />
      <SessionClaimOverlay />
    </div>
  );
}
