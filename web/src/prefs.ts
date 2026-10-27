import { create } from 'zustand';
import { api } from './api';
import { useCloud } from './cloud';
import { useEntitlements } from './entitlements';

// Per-user UI preferences, synced to the cloud (soluterminal_user_prefs) so a
// user's sidebar layout follows them across machines. Architecture:
//
//   • localStorage (solu.userPrefs.v1) is the offline cache + immediate source
//     on boot, so the UI never waits on a network round-trip to render. The
//     cache records WHICH user it belongs to (userId) — on a shared machine we
//     must never adopt or upload another user's cached layout.
//   • the cloud is the source of truth. On a fresh signed-in session we GET the
//     server copy and reconcile by last-write-wins on `updatedAt`.
//   • writes update the cache synchronously and push to the cloud debounced.
//
// If there's no session (local mode, signed-out) or the cloud is unreachable,
// everything still works against the local cache; the next hydrate reconciles.

export interface SidebarPrefs {
  // Middle-zone section keys in display order. Absent → default order.
  order?: string[];
  // Section keys the user has hidden.
  hidden?: string[];
}

export interface UserPrefs {
  sidebar?: SidebarPrefs;
}

const LS_KEY = 'solu.userPrefs.v1';
const PUSH_DEBOUNCE_MS = 800;

interface StoredPrefs {
  prefs: UserPrefs;
  updatedAt: string | null; // ISO 8601, or null when never written
  userId: string | null; // whose prefs these are (Supabase user id)
}

function loadLocal(): StoredPrefs {
  try {
    const raw = localStorage.getItem(LS_KEY);
    if (raw) {
      const parsed = JSON.parse(raw) as StoredPrefs;
      if (parsed && typeof parsed === 'object' && parsed.prefs && typeof parsed.prefs === 'object') {
        return { prefs: parsed.prefs, updatedAt: parsed.updatedAt ?? null, userId: parsed.userId ?? null };
      }
    }
  } catch {
    // Corrupt cache — fall through to defaults.
  }
  return { prefs: {}, updatedAt: null, userId: null };
}

function saveLocal(s: StoredPrefs): void {
  try {
    localStorage.setItem(LS_KEY, JSON.stringify(s));
  } catch {
    // Quota / private mode — the cloud copy still persists.
  }
}

interface PrefsState {
  prefs: UserPrefs;
  updatedAt: string | null;
  // The user these prefs belong to. Guards against a shared machine adopting or
  // uploading a previous user's cached layout.
  userId: string | null;
  // True once a cloud hydration attempt has completed (success or graceful
  // failure). Consumers can render immediately from the cache regardless.
  hydrated: boolean;
  setPrefs: (updater: (prev: UserPrefs) => UserPrefs) => void;
  hydrateFromCloud: (userId: string | null) => Promise<void>;
}

let pushTimer: ReturnType<typeof setTimeout> | null = null;

// "View as personal" shows a stranger's sidebar and makes it read-only, so
// layout sync pauses in both directions while it is on (the daemon refuses
// too). Leaving the view re-syncs.
function syncPaused(): boolean {
  return useEntitlements.getState().viewPersonal;
}

export const usePrefs = create<PrefsState>((set, get) => {
  const initial = loadLocal();

  const schedulePush = () => {
    if (pushTimer) clearTimeout(pushTimer);
    pushTimer = setTimeout(() => {
      pushTimer = null;
      if (syncPaused()) return;
      void api
        .putUserPrefs(get().prefs as unknown as Record<string, unknown>)
        .then((res) => {
          // Adopt the authoritative server timestamp so later LWW compares are
          // exact. Guard against a racing local edit landing meanwhile.
          if (res?.updated_at && pushTimer === null) {
            set({ updatedAt: res.updated_at });
            saveLocal({ prefs: get().prefs, updatedAt: res.updated_at, userId: get().userId });
          }
        })
        .catch(() => {
          // Offline / unauthenticated — the local cache stands; a later
          // hydrate or the next write reconciles.
        });
    }, PUSH_DEBOUNCE_MS);
  };

  return {
    prefs: initial.prefs,
    updatedAt: initial.updatedAt,
    userId: initial.userId,
    hydrated: false,

    setPrefs: (updater) => {
      const next = updater(get().prefs);
      const updatedAt = new Date().toISOString();
      set({ prefs: next, updatedAt });
      saveLocal({ prefs: next, updatedAt, userId: get().userId });
      schedulePush();
    },

    hydrateFromCloud: async (userId) => {
      if (syncPaused()) {
        set({ hydrated: true });
        return;
      }
      // If the cached prefs belong to a different user (or none), never adopt or
      // upload them — reset to empty for this user, then let the GET fill from
      // their own cloud row. This is the shared-machine safety boundary.
      if (get().userId !== userId) {
        set({ prefs: {}, updatedAt: null, userId });
        saveLocal({ prefs: {}, updatedAt: null, userId });
      }

      let remote: { prefs: Record<string, unknown>; updated_at: string | null };
      try {
        remote = await api.getUserPrefs();
      } catch {
        // No session or cloud unreachable — the local cache is authoritative.
        set({ hydrated: true });
        return;
      }
      const localTs = get().updatedAt ? Date.parse(get().updatedAt as string) : NaN;
      const remoteTs = remote.updated_at ? Date.parse(remote.updated_at) : NaN;
      const localValid = !Number.isNaN(localTs);
      const remoteValid = !Number.isNaN(remoteTs);

      // Cloud wins when we have no local copy, or the remote is at least as new.
      if (!localValid || (remoteValid && remoteTs >= localTs)) {
        if (remoteValid) {
          const prefs = (remote.prefs ?? {}) as unknown as UserPrefs;
          set({ prefs, updatedAt: remote.updated_at, hydrated: true });
          saveLocal({ prefs, updatedAt: remote.updated_at as string, userId });
          return;
        }
        // Neither side has anything — just mark hydrated.
        set({ hydrated: true });
        return;
      }

      // Local is newer than the cloud (or the cloud is empty) — push it up.
      set({ hydrated: true });
      try {
        const res = await api.putUserPrefs(get().prefs as unknown as Record<string, unknown>);
        if (res?.updated_at) {
          set({ updatedAt: res.updated_at });
          saveLocal({ prefs: get().prefs, updatedAt: res.updated_at, userId });
        }
      } catch {
        // Stay local.
      }
    },
  };
});

// Hydrate whenever a signed-in session's token appears or changes; drop the
// in-memory prefs on sign-out so a signed-out user's layout doesn't linger.
// Deferred a microtask because cloud.ts's applySession calls set({ session })
// BEFORE it wires the token into the api layer on the same synchronous call —
// the microtask runs after applySession returns, so the GET carries the token.
let lastToken: string | null = null;
useCloud.subscribe((s) => {
  const tok = s.session?.access_token ?? null;
  const uid = s.session?.user?.id ?? null;
  if (tok === lastToken) return; // only act on a token transition
  lastToken = tok;
  if (tok) {
    queueMicrotask(() => {
      void usePrefs.getState().hydrateFromCloud(uid);
    });
  } else {
    // Signed out. Clear the in-memory layout; the cache is user-scoped and is
    // left in place for the next sign-in to reconcile by userId.
    usePrefs.setState({ prefs: {}, updatedAt: null, userId: null, hydrated: false });
  }
});

// Leaving the personal view resumes sync for the signed-in user.
let lastViewPersonal = useEntitlements.getState().viewPersonal;
useEntitlements.subscribe((s) => {
  const was = lastViewPersonal;
  lastViewPersonal = s.viewPersonal;
  if (was && !s.viewPersonal) {
    const uid = useCloud.getState().session?.user?.id ?? null;
    if (uid) void usePrefs.getState().hydrateFromCloud(uid);
  }
});
