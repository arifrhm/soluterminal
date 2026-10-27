import { cancelTerminalRetirement } from './terminalInteractionState';
// Keys are terminalSessionKey(currentRoutingScope(), paneId): routing scope
// includes the principal epoch, so logout/account replacement cannot reuse drafts.
// Drafts belong to an exact scoped terminal process, including while hidden.
// This is read-only interaction evidence, never permission to close a pane.
const drafts = new Map<string, { identity: string; text: string }>();
const pending = new Map<string, number>();
export function quickReplyDraft(key: string, identity: string): string {
  return drafts.get(key)?.identity === identity ? drafts.get(key)!.text : '';
}
export function setQuickReplyDraft(key: string, identity: string, text: string): void {
  if (text) cancelTerminalRetirement(key);
  if (text) drafts.set(key, { identity, text }); else drafts.delete(key);
}
export function beginQuickReplyAttachment(key: string): () => void {
  cancelTerminalRetirement(key);
  pending.set(key, (pending.get(key) ?? 0) + 1);
  let active = true;
  return () => { if (!active) return; active = false; const n = (pending.get(key) ?? 1) - 1; if (n) pending.set(key, n); else pending.delete(key); };
}
export function quickReplyHasPendingInteraction(key: string, identity?: string): boolean {
  const draft = drafts.get(key);
  return !!draft?.text && (identity === undefined || draft.identity === identity) || (pending.get(key) ?? 0) > 0;
}
