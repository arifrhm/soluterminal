import { useSyncExternalStore } from "react";

// These leases follow the exact machine/session key, including retained views.
// Pending clipboard, composition and voice work must reject retirement before
// the server freezes the old owner. They never alter view placement or sizing.
const pending = new Map<string, Set<symbol>>();
const listeners = new Set<() => void>();
const recovered = new Map<string, Uint8Array[]>();
interface Fence {
  recoveryKey?: string;
  token: string;
  expiresAt: number;
  valid: () => boolean;
  cancel: () => void;
  flush: (bytes: Uint8Array) => boolean;
  cancelled: boolean;
  bytes: Uint8Array[];
  timer: ReturnType<typeof setTimeout>;
}
const fences = new Map<string, Fence>();
const publish = () => listeners.forEach((listener) => listener());
function preserve(key: string, bytes: Uint8Array[]) {
  if (bytes.length)
    recovered.set(key, [...(recovered.get(key) ?? []), ...bytes]);
}
function finish(key: string, fence: Fence, flush: boolean) {
  if (fences.get(key) !== fence) return;
  fences.delete(key);
  clearTimeout(fence.timer);
  for (const bytes of fence.bytes) {
    if (!flush || !fence.valid() || !fence.flush(bytes))
      preserve(fence.recoveryKey ?? key, [bytes]);
  }
  publish();
}
function current(key: string) {
  const fence = fences.get(key);
  // Background WebViews throttle timers. Check the deadline on every action.
  if (fence && (Date.now() >= fence.expiresAt || !fence.valid())) {
    finish(key, fence, false);
    return undefined;
  }
  return fence;
}
export function terminalInputAllowed(key: string): boolean {
  return !current(key);
}
export function cancelTerminalRetirement(key: string): void {
  const fence = current(key);
  if (fence && !fence.cancelled) {
    fence.cancelled = true;
    fence.cancel();
  }
}
export function beginTerminalInteraction(key: string): (() => void) | null {
  if (!terminalInputAllowed(key)) {
    cancelTerminalRetirement(key);
    return null;
  }
  const leases = pending.get(key) ?? new Set<symbol>();
  const token = Symbol();
  leases.add(token);
  pending.set(key, leases);
  let released = false;
  return () => {
    if (released) return;
    released = true;
    leases.delete(token);
    if (!leases.size) pending.delete(key);
  };
}
export function terminalHasPendingInteraction(key: string): boolean {
  return !!pending.get(key)?.size || !!recovered.get(key)?.length;
}
export function beginTerminalRetirement(
  key: string,
  options: {
    token: string;
    recoveryKey?: string;
    expiresAtMs?: number;
    leaseMs?: number;
    valid: () => boolean;
    pending: () => boolean;
    cancel: () => void;
    flush: (bytes: Uint8Array) => boolean;
  },
): boolean {
  // Resolve an expired/replaced connection before checking preserved input.
  // Its queued bytes can become a recovery draft during this call.
  const prior = current(key);
  // Relative duration avoids daemon/browser wall-clock skew. The server keeps
  // its independent absolute deadline; repeated probes never extend our fence.
  const expiresAt =
    options.leaseMs === undefined
      ? options.expiresAtMs
      : Number.isFinite(options.leaseMs) &&
          options.leaseMs > 0 &&
          options.leaseMs <= 5000
        ? Date.now() + options.leaseMs
        : NaN;
  if (
    !options.token ||
    options.token.length > 256 ||
    typeof expiresAt !== "number" ||
    !Number.isFinite(expiresAt) ||
    expiresAt <= Date.now() ||
    expiresAt > Date.now() + 5000 ||
    !options.valid() ||
    terminalHasPendingInteraction(key) ||
    !!recovered.get(options.recoveryKey ?? key)?.length ||
    options.pending()
  )
    return false;
  if (prior)
    return (
      prior.token === options.token &&
      (options.leaseMs !== undefined || prior.expiresAt === expiresAt) &&
      !prior.cancelled
    );
  const fence: Fence = {
    ...options,
    expiresAt,
    cancelled: false,
    bytes: [],
    timer: 0 as unknown as ReturnType<typeof setTimeout>,
  };
  fence.timer = setTimeout(
    () => finish(key, fence, false),
    Math.max(0, expiresAt - Date.now()),
  );
  fences.set(key, fence);
  publish();
  return true;
}
export function releaseTerminalRetirement(key: string, token: string): void {
  const fence = fences.get(key);
  if (fence?.token === token) finish(key, fence, true);
}
export function abandonTerminalRetirement(key: string): void {
  const fence = fences.get(key);
  if (fence) finish(key, fence, false);
}
// Only raw terminal bytes use this queue. Structured composers receive false
// from their sender and retain their own draft. A socket/process change never
// replays queued keystrokes into a different terminal incarnation.
export function holdTerminalInput(key: string, bytes: Uint8Array): boolean {
  const fence = current(key);
  if (!fence) return false;
  fence.bytes.push(bytes.slice());
  cancelTerminalRetirement(key);
  return true;
}
export function terminalRecoveredInput(key: string): string {
  const bytes = recovered.get(key) ?? [];
  const decoder = new TextDecoder();
  return (
    bytes.map((chunk) => decoder.decode(chunk, { stream: true })).join("") +
    decoder.decode()
  );
}
export function clearTerminalRecoveredInput(
  key: string,
  copiedText?: string,
): void {
  if (copiedText !== undefined && terminalRecoveredInput(key) !== copiedText)
    return;
  recovered.delete(key);
  publish();
}
export function useTerminalRetirementPaused(key: string): boolean {
  return useSyncExternalStore(
    (listener) => {
      listeners.add(listener);
      return () => listeners.delete(listener);
    },
    () => fences.has(key),
  );
}
export function useTerminalRecoveredInput(key: string): string {
  return useSyncExternalStore(
    (listener) => {
      listeners.add(listener);
      return () => listeners.delete(listener);
    },
    () => terminalRecoveredInput(key),
  );
}
