import { describe, expect, it } from 'vitest';
import { __useNativePaneTest as T } from './useNativePane';
import type { NativeEvent } from './nativeEvents';

function ev(seq: number, type: NativeEvent['type'], extra: Partial<NativeEvent> = {}): NativeEvent {
  return { seq, paneId: 'pane-1', type, fromReplay: false, ...extra } as NativeEvent;
}

const permission = (seq: number, requestId: string, callId: string) =>
  ev(seq, 'permission_request', { requestId, callId, toolName: 'Bash' });

describe('native approval resolution', () => {
  it('dismisses a server-resolved card before the long-running tool returns', () => {
    const request = permission(1, 'req-1', 'call-1');
    expect(T.derivePendingApprovals([request], new Set())).toEqual([request]);

    const resolved = ev(2, 'permission_resolved', {
      requestId: 'req-1',
      callId: 'call-1',
      toolName: 'Bash',
    });
    expect(T.derivePendingApprovals([request, resolved], new Set())).toEqual([]);
  });

  it('resolves by exact request id when parallel approvals are open', () => {
    const first = permission(1, 'req-read', 'call-read');
    const second = permission(2, 'req-bash', 'call-bash');
    const resolved = ev(3, 'permission_resolved', {
      requestId: 'req-read',
      callId: 'call-read',
    });

    expect(T.derivePendingApprovals([first, second, resolved], new Set())).toEqual([second]);
  });

  it('keeps tool_result as the fallback for older daemons', () => {
    const request = permission(1, 'req-1', 'call-1');
    const result = ev(2, 'tool_result', { callId: 'call-1', content: 'done' });
    expect(T.derivePendingApprovals([request, result], new Set())).toEqual([]);
  });
});
