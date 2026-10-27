// Shared Preview/Stable channel state: one poller for the status-bar chip and
// the version popover. Polls slowly (5 min) and quickly (5 s) while a switch
// is building; stops for good on machines where Preview isn't available.
import { useSyncExternalStore } from 'react';
import { api } from '../api';
import type { PreviewChannelStatus } from '../types';

const SLOW_MS = 5 * 60_000;
const FAST_MS = 5_000;

let state: PreviewChannelStatus | null = null;
let timer: ReturnType<typeof setTimeout> | null = null;
let unavailable = false;
const listeners = new Set<() => void>();

function emit() {
  for (const l of listeners) l();
}

export async function refreshPreviewChannel(): Promise<void> {
  try {
    state = await api.getPreviewChannel();
    unavailable = !state.available;
  } catch {
    // Older daemons (no route) or offline: treat as unavailable for now.
    state = null;
  }
  emit();
  schedule();
}

function schedule() {
  if (timer) clearTimeout(timer);
  timer = null;
  if (unavailable || listeners.size === 0) return;
  timer = setTimeout(() => void refreshPreviewChannel(), state?.job ? FAST_MS : SLOW_MS);
}

function subscribe(l: () => void) {
  listeners.add(l);
  if (listeners.size === 1 && !unavailable) void refreshPreviewChannel();
  return () => {
    listeners.delete(l);
    if (listeners.size === 0 && timer) {
      clearTimeout(timer);
      timer = null;
    }
  };
}

export function usePreviewChannel(): PreviewChannelStatus | null {
  return useSyncExternalStore(subscribe, () => state, () => null);
}

export async function switchPreviewChannel(target: 'preview' | 'stable'): Promise<void> {
  await api.installPreviewChannel(target);
  await refreshPreviewChannel();
}

/** A 4-part version (0.0.1168.15) is a Preview build. */
export function isPreviewVersion(version: string | undefined): boolean {
  return !!version && version.split('.').length >= 4;
}

// Test hook.
export function __resetPreviewChannelForTests() {
  state = null;
  unavailable = false;
  if (timer) clearTimeout(timer);
  timer = null;
  listeners.clear();
}
