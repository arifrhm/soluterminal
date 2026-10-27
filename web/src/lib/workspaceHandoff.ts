import { embedSurfaceVisible } from '../embed';
import {
  checkOwnedByOther,
  claimWorkspace,
  isOwnable,
  readOwners,
  refreshWorkspaceVisibility,
  releaseWorkspace,
  subscribeOwners,
} from './workspaceOwnership';

const CHANNEL = 'solu-workspace-handoff';
const suspended = new Map<string, string>();
const preparingViews = new Set<string>();
const listeners = new Set<() => void>();
const notify = () => listeners.forEach((listener) => listener());
export const workspaceMovedTo = (name: string) => suspended.get(name) || null;
export const workspaceTransferring = (name: string) => preparingViews.has(name);
export function suspendWorkspaceView(name: string, owner: string) {
  suspended.set(name, owner);
  notify();
}
export function resumeWorkspaceView(name: string) {
  suspended.delete(name);
  notify();
}
export function subscribeWorkspaceHandoff(listener: () => void) {
  listeners.add(listener);
  return () => {
    listeners.delete(listener);
  };
}
export const workspaceViewVisible = () =>
  embedSurfaceVisible() && document.visibilityState !== 'hidden';

type Request = {
  kind: 'request';
  id: string;
  name: string;
  owner: string;
  requester: string;
  force: boolean;
  expires: number;
};

/** Save and suspend the owning view before allowing another window to attach. */
export function installWorkspaceHandoff(options: {
  label: string;
  active: () => string;
  prepare: () => Promise<void>;
  suspended: () => void;
}) {
  if (typeof BroadcastChannel === 'undefined') return () => {};
  const channel = new BroadcastChannel(CHANNEL);
  let transferring = false;
  let disposed = false;
  const refresh = () => {
    const name = options.active();
    if (!workspaceMovedTo(name))
      refreshWorkspaceVisibility(name, options.label, workspaceViewVisible());
  };
  const observe = () => {
    const name = options.active();
    const owner = checkOwnedByOther(name, options.label);
    if (owner && !workspaceMovedTo(name)) {
      suspended.set(name, owner);
      options.suspended();
      notify();
    }
  };
  const stop = subscribeOwners(observe);
  channel.onmessage = async (event: MessageEvent) => {
    const message = event.data as Request;
    if (
      !message ||
      message.kind !== 'request' ||
      typeof message.id !== 'string' ||
      typeof message.name !== 'string' ||
      typeof message.requester !== 'string' ||
      typeof message.force !== 'boolean' ||
      !Number.isSafeInteger(message.expires) ||
      message.expires <= Date.now() ||
      message.owner !== options.label ||
      message.requester === options.label ||
      message.name !== options.active() ||
      readOwners()[message.name]?.label !== options.label ||
      transferring
    )
      return;
    if (workspaceViewVisible() && !message.force) {
      refresh();
      channel.postMessage({ kind: 'reply', id: message.id, result: 'visible' });
      return;
    }
    transferring = true;
    preparingViews.add(message.name);
    notify();
    try {
      // Freeze pane interaction before taking the final snapshot, so typing,
      // resizing or closing a tile during the save cannot escape that snapshot.
      await new Promise<void>((resolve) => setTimeout(resolve, 0));
      if (disposed) return;
      await options.prepare();
      if (disposed) return;
      if (message.name !== options.active() || message.expires <= Date.now()) {
        channel.postMessage({ kind: 'reply', id: message.id, result: 'unavailable' });
        return;
      }
      if (workspaceViewVisible() && !message.force) {
        refresh();
        channel.postMessage({ kind: 'reply', id: message.id, result: 'visible' });
        return;
      }
      suspended.set(message.name, message.requester);
      options.suspended();
      notify();
      // Give React a commit before acknowledging: the old grid becomes inert,
      // while its mounted editors and terminal clients retain local drafts.
      await new Promise<void>((resolve) => setTimeout(resolve, 0));
      if (disposed) return;
      releaseWorkspace(message.name, options.label);
      channel.postMessage({
        kind: 'reply',
        id: message.id,
        result: 'released',
      });
    } catch {
      if (disposed) return;
      channel.postMessage({
        kind: 'reply',
        id: message.id,
        result: 'save-failed',
      });
    } finally {
      preparingViews.delete(message.name);
      notify();
      transferring = false;
    }
  };
  window.addEventListener('solu:embed-visibility', refresh);
  document.addEventListener('visibilitychange', refresh);
  refresh();
  return () => {
    disposed = true;
    stop();
    channel.close();
    window.removeEventListener('solu:embed-visibility', refresh);
    document.removeEventListener('visibilitychange', refresh);
  };
}

export type WorkspaceClaim = 'acquired' | 'visible' | 'unavailable' | 'save-failed';

/** Serialize competing requests; heartbeat/refocus is never a takeover gesture. */
export async function acquireWorkspaceView(
  name: string,
  label: string,
  force = false,
  current: () => boolean = () => true,
): Promise<WorkspaceClaim> {
  if (!isOwnable(name)) return 'acquired';
  const acquire = async (): Promise<WorkspaceClaim> => {
    if (!current()) return 'unavailable';
    const owner = checkOwnedByOther(name, label);
    if (owner) {
      if (!force && readOwners()[name]?.visible !== false) return 'visible';
      if (typeof BroadcastChannel === 'undefined') return 'unavailable';
      const channel = new BroadcastChannel(CHANNEL);
      const id = crypto.randomUUID();
      const result = await new Promise<string>((resolve) => {
        const timer = setTimeout(() => resolve('unavailable'), 5000);
        channel.onmessage = (event: MessageEvent) => {
          if (event.data?.kind !== 'reply' || event.data.id !== id) return;
          clearTimeout(timer);
          resolve(event.data.result);
        };
        channel.postMessage({
          kind: 'request',
          id,
          name,
          owner,
          requester: label,
          force,
          expires: Date.now() + 5000,
        });
      });
      channel.close();
      if (result !== 'released')
        return result === 'visible' || result === 'save-failed' ? result : 'unavailable';
      if (checkOwnedByOther(name, label)) return 'visible';
    }
    if (!current()) return 'unavailable';
    if (!claimWorkspace(name, label, Date.now(), true)) return 'visible';
    refreshWorkspaceVisibility(name, label, workspaceViewVisible());
    return 'acquired';
  };
  return navigator.locks ? navigator.locks.request('solu-workspace-switch', acquire) : acquire();
}
