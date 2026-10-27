import { render, screen } from '@testing-library/react';
import { describe, expect, it } from 'vitest';
import type { NativeEvent } from '../../native/nativeEvents';
import { EditGroupView, StepGroupView, ThinkingView, ToolGroupView } from './NativeEventViews';

function ev(partial: Partial<NativeEvent> & { type: NativeEvent['type']; seq: number }): NativeEvent {
  return { paneId: 'focus-pane', fromReplay: false, ...partial } as NativeEvent;
}

function tool(seq: number, callId: string, name = 'Bash'): NativeEvent {
  return ev({
    type: 'tool_call',
    seq,
    callId,
    name,
    toolKind: name === 'Bash' ? 'exec' : 'file',
    args: name === 'Edit'
      ? { file_path: `/tmp/${callId}.txt`, old_string: 'before', new_string: 'after' }
      : { command: 'printf ok' },
  });
}

function result(seq: number, callId: string, isError = false): NativeEvent {
  return ev({ type: 'tool_result', seq, callId, content: isError ? 'failed' : 'successful output', isError });
}

describe('native foldable defaults', () => {
  it('keeps successful tool groups collapsed even when output is available', () => {
    const call = tool(1, 'tool-1');
    const { container } = render(
      <ToolGroupView events={[call]} resultByCallId={new Map([['tool-1', result(2, 'tool-1')]])} />,
    );
    expect(screen.getByRole('button', { name: /Expand 1 tool call/ })).toHaveAttribute('aria-expanded', 'false');
    expect(container.querySelector('.native-tool-group-body')).toBeNull();
  });

  it('keeps successful interleaved step groups collapsed', () => {
    const thinking = ev({ type: 'thinking', seq: 1, text: 'I should inspect the files.' });
    const call = tool(2, 'tool-2');
    const { container } = render(
      <StepGroupView events={[thinking, call]} resultByCallId={new Map([['tool-2', result(3, 'tool-2')]])} />,
    );
    expect(screen.getByRole('button', { name: /Expand 1 tool call with reasoning/ })).toHaveAttribute('aria-expanded', 'false');
    expect(container.querySelector('.native-tool-group-body')).toBeNull();
  });

  it('keeps successful edit groups collapsed', () => {
    const calls = [tool(1, 'edit-1', 'Edit'), tool(2, 'edit-2', 'Edit')];
    const results = new Map([
      ['edit-1', result(3, 'edit-1')],
      ['edit-2', result(4, 'edit-2')],
    ]);
    const { container } = render(<EditGroupView events={calls} resultByCallId={results} />);
    expect(screen.getByRole('button', { name: /Expand 2 edits/ })).toHaveAttribute('aria-expanded', 'false');
    expect(container.querySelector('.native-tool-group-body')).toBeNull();
  });

  it('force-opens an edit group when one result failed', () => {
    const calls = [tool(1, 'edit-1', 'Edit'), tool(2, 'edit-2', 'Edit')];
    const results = new Map([
      ['edit-1', result(3, 'edit-1')],
      ['edit-2', result(4, 'edit-2', true)],
    ]);
    render(<EditGroupView events={calls} resultByCallId={results} />);
    expect(screen.getByRole('button', { name: /Collapse edits/ })).toHaveAttribute('aria-expanded', 'true');
  });

  it('keeps thinking collapsed by default', () => {
    const { container } = render(
      <ThinkingView ev={ev({ type: 'thinking', seq: 1, text: 'A compact reasoning block.' })} />,
    );
    expect(screen.getByRole('button', { name: /Show reasoning/ })).toHaveAttribute('aria-expanded', 'false');
    expect(container.querySelector('.native-thinking-body')).toBeNull();
  });
});
