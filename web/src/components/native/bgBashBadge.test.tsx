// Render tests for the "background" badge on a run_in_background Bash — so the
// fire-and-forget nature of a detached shell is legible (a ✓ on the row means
// "launched", not "finished"). Mirrors how Term mode shows Claude's own
// background-shell chrome; the native renderer otherwise flattened it to a plain
// $ bash row.

import { render, screen } from '@testing-library/react';
import { describe, expect, it } from 'vitest';
import { ToolCallView, ToolGroupView } from './NativeEventViews';
import type { NativeEvent } from '../../native/nativeEvents';

function ev(partial: Partial<NativeEvent> & { type: NativeEvent['type']; seq: number }): NativeEvent {
  return { paneId: 'p', fromReplay: false, ...partial } as NativeEvent;
}

const bgBash = (seq: number, callId: string) =>
  ev({ type: 'tool_call', seq, callId, name: 'Bash', toolKind: 'exec', args: { command: 'bash build.sh', run_in_background: true } });
const fgBash = (seq: number, callId: string) =>
  ev({ type: 'tool_call', seq, callId, name: 'Bash', toolKind: 'exec', args: { command: 'ls -la' } });

describe('background-bash badge', () => {
  it('flags a run_in_background Bash row "background · running" by default (no completion seen)', () => {
    render(<ToolCallView ev={bgBash(1, 'a')} />);
    expect(screen.getByText(/background · running/)).toBeInTheDocument();
  });

  it('flips to "background · exited 0" when the launch result carries a done bgShell', () => {
    const result = ev({ type: 'tool_result', seq: 2, callId: 'a', bgShell: { state: 'exited', exitCode: 0 } });
    render(<ToolCallView ev={bgBash(1, 'a')} result={result} />);
    expect(screen.getByText(/background · exited 0/)).toBeInTheDocument();
  });

  it('does NOT flag a normal (foreground) Bash row', () => {
    render(<ToolCallView ev={fgBash(1, 'a')} />);
    expect(screen.queryByText(/background/)).not.toBeInTheDocument();
  });

  it('surfaces the background status on a collapsed tool-group header before expand', () => {
    render(<ToolGroupView events={[bgBash(1, 'a')]} resultByCallId={new Map()} />);
    expect(screen.getByText(/· background · running/)).toBeInTheDocument();
  });

  it('a foreground-only group header carries no background pip', () => {
    render(<ToolGroupView events={[fgBash(1, 'a')]} resultByCallId={new Map()} />);
    expect(screen.queryByText(/background/)).not.toBeInTheDocument();
  });

  it('aggregates MULTIPLE background shells in a header and surfaces the failure (not just the first)', () => {
    const results = new Map<string, NativeEvent>([
      ['a', ev({ type: 'tool_result', seq: 3, callId: 'a', bgShell: { state: 'exited', exitCode: 0 } })],
      ['b', ev({ type: 'tool_result', seq: 4, callId: 'b', bgShell: { state: 'exited', exitCode: 7 } })],
    ]);
    render(<ToolGroupView events={[bgBash(1, 'a'), bgBash(2, 'b')]} resultByCallId={results} />);
    // Header must surface that ONE failed (exit 7), not show only the first shell's "exited 0".
    expect(screen.getByText(/2 background · 1 failed/)).toBeInTheDocument();
    expect(screen.queryByText(/exited 0/)).not.toBeInTheDocument();
  });
});
