import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const presentation = vi.hoisted(() => ({ visible: true }));
vi.mock('../embed', () => ({
  embedSurfaceVisible: () => presentation.visible,
}));

class Channel {
  static peers = new Set<Channel>();
  onmessage: ((event: { data: unknown }) => void) | null = null;
  listeners = new Set<(event: { data: unknown }) => void>();
  addEventListener(_kind: string, listener: (event: { data: unknown }) => void) {
    this.listeners.add(listener);
  }
  removeEventListener(_kind: string, listener: (event: { data: unknown }) => void) {
    this.listeners.delete(listener);
  }
  constructor(readonly name: string) {
    Channel.peers.add(this);
  }
  postMessage(data: unknown) {
    for (const peer of Channel.peers)
      if (peer !== this && peer.name === this.name) {
        queueMicrotask(() => {
          if (Channel.peers.has(peer)) {
            peer.onmessage?.({ data });
            for (const listener of peer.listeners) listener({ data });
          }
        });
      }
  }
  close() {
    Channel.peers.delete(this);
  }
}

let ownership: typeof import('./workspaceOwnership');
let handoff: typeof import('./workspaceHandoff');
let stop: (() => void) | undefined;

beforeEach(async () => {
  vi.resetModules();
  localStorage.clear();
  presentation.visible = true;
  vi.stubGlobal('BroadcastChannel', Channel);
  vi.spyOn(document, 'visibilityState', 'get').mockReturnValue('visible');
  ownership = await import('./workspaceOwnership');
  handoff = await import('./workspaceHandoff');
});
afterEach(() => {
  stop?.();
  stop = undefined;
  Channel.peers.clear();
  vi.useRealTimers();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

function owner(prepare = vi.fn(async () => {})) {
  ownership.claimWorkspace('Project', 'first');
  const suspended = vi.fn();
  stop = handoff.installWorkspaceHandoff({
    label: 'first',
    active: () => 'Project',
    prepare,
    suspended,
  });
  return { prepare, suspended };
}

describe('workspace view handoff', () => {
  it('moves a hidden workspace only after pending persistence completes', async () => {
    let save!: () => void;
    const prepare = vi.fn(
      () =>
        new Promise<void>((resolve) => {
          save = resolve;
        }),
    );
    const first = owner(prepare);
    presentation.visible = false;
    window.dispatchEvent(new CustomEvent('solu:embed-visibility'));
    const move = handoff.acquireWorkspaceView('Project', 'second');
    await vi.waitFor(() => expect(prepare).toHaveBeenCalledOnce());
    expect(ownership.readOwners().Project.label).toBe('first');
    expect(first.suspended).not.toHaveBeenCalled();
    expect(handoff.workspaceTransferring('Project')).toBe(true);
    save();
    expect(await move).toBe('acquired');
    expect(ownership.readOwners().Project.label).toBe('second');
    expect(first.suspended).toHaveBeenCalledOnce();
    expect(handoff.workspaceTransferring('Project')).toBe(false);
    expect(handoff.workspaceMovedTo('Project')).toBe('second');
    // Showing the old host again only refreshes its own claim, if any.
    presentation.visible = true;
    window.dispatchEvent(new CustomEvent('solu:embed-visibility'));
    expect(ownership.readOwners().Project.label).toBe('second');
  });

  it('requires an explicit move when the workspace is visible, regardless of keyboard focus', async () => {
    const first = owner();
    window.dispatchEvent(new Event('blur'));
    expect(await handoff.acquireWorkspaceView('Project', 'second')).toBe('visible');
    expect(first.prepare).not.toHaveBeenCalled();
    expect(await handoff.acquireWorkspaceView('Project', 'second', true)).toBe('acquired');
    expect(first.prepare).toHaveBeenCalledOnce();
  });

  it('keeps ownership and the old view if saving fails', async () => {
    const first = owner(
      vi.fn(async () => {
        throw new Error('disk unavailable');
      }),
    );
    expect(await handoff.acquireWorkspaceView('Project', 'second', true)).toBe('save-failed');
    expect(ownership.readOwners().Project.label).toBe('first');
    expect(first.suspended).not.toHaveBeenCalled();
    expect(handoff.workspaceMovedTo('Project')).toBeNull();
    expect(handoff.workspaceTransferring('Project')).toBe(false);
  });

  it('does not let a late heartbeat or reload claim undo another window’s move', () => {
    vi.useFakeTimers();
    ownership.claimWorkspace('Project', 'first');
    const heartbeat = ownership.startOwnershipHeartbeat(() => 'Project', 'first');
    ownership.writeOwners({
      Project: { label: 'second', ts: Date.now(), visible: true },
    });
    vi.advanceTimersByTime(5000);
    expect(ownership.readOwners().Project.label).toBe('second');
    expect(ownership.claimWorkspace('Project', 'first')).toBe(false);
    heartbeat();
  });

  it('keeps different workspaces owned by different windows', async () => {
    owner();
    expect(await handoff.acquireWorkspaceView('Other', 'second')).toBe('acquired');
    expect(ownership.readOwners().Project.label).toBe('first');
    expect(ownership.readOwners().Other.label).toBe('second');
  });
  it('keeps the outgoing claim until the destination actually mounts', async () => {
    ownership.claimWorkspace('Outgoing', 'second');
    owner();
    expect(await handoff.acquireWorkspaceView('Project', 'second', true)).toBe('acquired');
    expect(ownership.readOwners().Outgoing.label).toBe('second');
    ownership.claimWorkspace('Project', 'second');
    expect(ownership.readOwners().Outgoing).toBeUndefined();
  });

  it('does not claim a destination after its requesting navigation is cancelled', async () => {
    let save!: () => void;
    let current = true;
    owner(
      vi.fn(
        () =>
          new Promise<void>((resolve) => {
            save = resolve;
          }),
      ),
    );
    const move = handoff.acquireWorkspaceView('Project', 'second', true, () => current);
    await vi.waitFor(() => expect(save).toBeTypeOf('function'));
    current = false;
    save();
    expect(await move).toBe('unavailable');
    expect(ownership.readOwners().Project).toBeUndefined();
  });

  it('does not suspend an owner when saving outlives the request', async () => {
    let save!: () => void;
    const now = Date.now();
    const clock = vi.spyOn(Date, 'now').mockReturnValue(now);
    const first = owner(
      vi.fn(
        () =>
          new Promise<void>((resolve) => {
            save = resolve;
          }),
      ),
    );
    const move = handoff.acquireWorkspaceView('Project', 'second', true);
    await vi.waitFor(() => expect(save).toBeTypeOf('function'));
    clock.mockReturnValue(now + 6000);
    save();
    expect(await move).toBe('unavailable');
    expect(ownership.readOwners().Project.label).toBe('first');
    expect(first.suspended).not.toHaveBeenCalled();
  });

  it('asks before moving a hidden workspace that becomes visible during saving', async () => {
    let save!: () => void;
    const first = owner(
      vi.fn(
        () =>
          new Promise<void>((resolve) => {
            save = resolve;
          }),
      ),
    );
    presentation.visible = false;
    window.dispatchEvent(new CustomEvent('solu:embed-visibility'));
    const move = handoff.acquireWorkspaceView('Project', 'second');
    await vi.waitFor(() => expect(save).toBeTypeOf('function'));
    presentation.visible = true;
    save();
    expect(await move).toBe('visible');
    expect(ownership.readOwners().Project.label).toBe('first');
    expect(first.suspended).not.toHaveBeenCalled();
  });
});
