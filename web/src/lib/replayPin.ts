export interface TermSize {
  cols: number;
  rows: number;
}

// The daemon aligns a full reattach repaint to the terminal size the client
// announced at connect. When the layout has moved since (a notice or banner
// appeared), the replay must be written at the announced size and the pane
// refitted afterwards. Exact resumes carry no repaint, so they never pin.
export function replayPinTarget(
  isResume: boolean,
  announced: TermSize,
  current: TermSize,
): TermSize | null {
  if (isResume) return null;
  if (announced.cols <= 1 || announced.rows <= 0) return null;
  if (current.cols === announced.cols && current.rows === announced.rows) return null;
  return { cols: announced.cols, rows: announced.rows };
}
