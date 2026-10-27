export interface PaneRestoreSnapshot {
  viewportY: number;
  linesFromBottom: number;
  rows: number;
  cols: number;
  rectTop: number;
  rectBottom: number;
  wasAtBottom: boolean;
  followFromBottom: boolean;
}

export interface PaneRestoreCurrent {
  ydisp: number;
  length: number;
  rows: number;
  cols: number;
  rectTop?: number;
  rectBottom?: number;
}

export interface PaneRestoreTarget {
  targetY: number;
  skipYdisp: boolean;
  isUserScrolling: boolean;
  mode: string;
}

export function computePaneRestoreTarget(
  saved: PaneRestoreSnapshot,
  current: PaneRestoreCurrent,
): PaneRestoreTarget {
  const maxY = Math.max(0, current.length - current.rows);
  const topDelta = current.rectTop === undefined ? 0 : Math.abs(current.rectTop - saved.rectTop);
  const bottomDelta = current.rectBottom === undefined ? 0 : Math.abs(current.rectBottom - saved.rectBottom);
  const bottomAnchored = topDelta > bottomDelta + 1;
  const rowsDelta = current.rows - saved.rows;
  const colsChanged = current.cols !== saved.cols;

  let targetY: number;
  let skipYdisp = false;
  if (saved.wasAtBottom) {
    targetY = maxY;
  } else if (saved.followFromBottom) {
    targetY = Math.max(0, Math.min(current.length - current.rows - saved.linesFromBottom, maxY));
  } else if (colsChanged) {
    targetY = current.ydisp;
    skipYdisp = true;
  } else if (bottomAnchored) {
    targetY = Math.max(0, Math.min(saved.viewportY - rowsDelta, maxY));
  } else {
    targetY = Math.max(0, Math.min(saved.viewportY, maxY));
  }

  const restoredAtBottom = skipYdisp ? current.ydisp >= maxY : targetY >= maxY;
  const mode = saved.wasAtBottom ? 'bottom'
    : saved.followFromBottom ? `follow-bot(${saved.linesFromBottom})`
    : colsChanged ? `reflow-trust(xterm yd=${current.ydisp})`
    : bottomAnchored ? `abs-botA(${saved.viewportY}-${rowsDelta})`
    : `abs-topA(${saved.viewportY})`;

  return {
    targetY,
    skipYdisp,
    isUserScrolling: !restoredAtBottom,
    mode,
  };
}
