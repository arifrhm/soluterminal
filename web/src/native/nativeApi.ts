// nativeApi.ts — native-renderer API client for panes with kind==='native'.
//
// These are thin, named wrappers over the `api` object's native methods in
// api.ts. The actual URL/prefix/token plumbing lives there (and is reused
// verbatim): the stream is a WebSocket whose auth rides the subprotocol offer
// (never the URL); the POST helpers go through the private apiFetch
// (Authorization: Bearer <token>) + j<T> decoder. Keeping
// the wrappers here gives NativePane a stable native-only surface to import
// without reaching into the whole `api` object.

import { openWebSocket } from '../lib/signedStreams';
import { api } from '../api';
import type {
  NativeHistoryResponse,
  NativeInputResponse,
  NativeInterruptResponse,
  NativeAllowlistResponse,
  ResolveApprovalResponse,
} from './nativeEvents';

// Re-export the allowlist response type so the hook can import it from this
// native-only surface alongside the helpers below.
export type { NativeAllowlistResponse } from './nativeEvents';

// Open the per-pane native event WebSocket. The daemon sends an epoch control
// frame ({"kind":"epoch","nativeEpoch":N}) FIRST, then the ring snapshot
// (oldest-first, each Seq'd) as raw NativeEvent JSON text frames, then live
// deltas in the same framing. Callers discriminate on `kind === 'epoch'` (a
// NativeEvent never has `kind`), dedup by `seq` (the snapshot tail can overlap
// the first live deltas), and resync via fetchNativeHistory on (re)connect.
//
// WS (not SSE) so native panes use the separate WebSocket browser pool instead
// of the ~6 HTTP/1.1 connections-per-host the SSE stream consumed — a few
// visible native panes used to exhaust that pool and break reload + health.
// Unlike EventSource, WS does NOT auto-reconnect; the caller owns the
// reconnect-with-backoff loop (see useNativePane). Cloud auth rides the
// subprotocol offer (api.wsProtocols), keeping the JWT out of the URL.
export function openNativeStream(paneId: string): WebSocket {
  return openWebSocket(api.nativeWsUrl(paneId), api.wsProtocols());
}

// Backfill events with seq > `since` after a stream drop. If the response is
// `truncated` the ring floor passed `since` (events aged out); the caller must
// drop the backfill and do a fresh full SSE resnapshot instead.
export function fetchNativeHistory(paneId: string, since: number): Promise<NativeHistoryResponse> {
  return api.nativeHistory(paneId, since);
}

// Write a user turn. The daemon encodes one atomic stream-json envelope — no
// PTY Enter-split needed (that's a TUI-paste-burst workaround, PTY-only).
export function sendNativeInput(paneId: string, text: string, localMsgId?: string): Promise<NativeInputResponse> {
  return api.nativeInput(paneId, text, localMsgId);
}

// Answer a permission_request. IDEMPOTENT server-side: an unknown/already-
// resolved reqId returns {resolved:false} at HTTP 200 (not 404), so disable-on-
// click is safe and a lock-screen + in-app double-tap is a benign no-op.
export function resolveNativeApproval(
  paneId: string,
  reqId: string,
  allow: boolean,
  denyReason?: string,
): Promise<ResolveApprovalResponse> {
  return api.resolveNativeApproval(paneId, reqId, allow, denyReason);
}

// Interrupt the agent. The daemon denies any open approval first, then
// interrupts the current turn.
export function interruptNative(paneId: string): Promise<NativeInterruptResponse> {
  return api.nativeInterrupt(paneId);
}

// Read the pane's current "always allow" tool list. A tool here auto-approves
// future permission_requests for that tool (exact-name match) without a card.
export function getAllowlist(paneId: string): Promise<NativeAllowlistResponse> {
  return api.nativeAllowlist(paneId);
}

// Add or remove a tool from the pane's allowlist; the daemon returns the UPDATED
// list, so the caller can reconcile optimistic state against the source of truth.
export function setAllowlist(
  paneId: string,
  tool: string,
  action: 'add' | 'remove',
): Promise<NativeAllowlistResponse> {
  return api.nativeSetAllowlist(paneId, tool, action);
}
