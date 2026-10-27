import { useEffect, useState } from 'react';
import { useStore } from '../store';
import { getShellVersion, isDesktopApp } from '../tauri-browser';
import { DESKTOP_DOWNLOAD_URL } from './shellUpdateFallback';

// Strict "is a newer than b" for dotted numeric versions (0.0.835 > 0.0.803).
// Exported for tests. Missing/short components read as 0 so "0.0.9" < "0.0.10".
export function isVersionNewer(a: string, b: string): boolean {
  const pa = a.split('.').map((n) => parseInt(n, 10) || 0);
  const pb = b.split('.').map((n) => parseInt(n, 10) || 0);
  for (let i = 0; i < Math.max(pa.length, pb.length); i++) {
    const x = pa[i] ?? 0;
    const y = pb[i] ?? 0;
    if (x > y) return true;
    if (x < y) return false;
  }
  return false;
}

export function isCloudLaunchMode(): boolean {
  const g = (window as unknown as { __SOLU_BACKEND__?: { launchMode?: string } }).__SOLU_BACKEND__;
  return g?.launchMode === 'cloud';
}

export interface CloudShellUpdate {
  available: boolean;
  current: string | null; // this machine's installed Tauri shell version
  latest: string | null;  // manifest shell version (from the daemon status)
  releaseUrl: string | null; // the public download page, for a manual install
}

// Detect a desktop-shell (installer) update while running in CLOUD launch mode.
//
// Why this exists: the installer-update badge is normally computed by the
// daemon (updater.go recomputeShellLocked — compares the manifest's shell
// version to the locally-installed shell). In CLOUD mode the desktop app runs
// NO local daemon (lib.rs: DaemonStart::single → cloud URL), so
// /api/update/status is answered by the REMOTE daemon, which computes
// shellAvailable for the REMOTE machine's platform — the local Windows shell's
// outdatedness is invisible, so the badge never lights and cloud users get
// stranded on whatever shell they first installed.
//
// The remote daemon still reports `shellLatest` (it reads the same manifest),
// so we recover the check web-side: ping the LOCAL shell version
// (browser_pane_ping → app.package_info().version; present since v0.0.284) and
// compare. This lives in the web bundle, which auto-updates from the cloud even
// in cloud mode — so the fix reaches already-stranded shells (a Rust-side fix
// could not: they can't get the new shell, which is the whole bug).
// Shared core: ping the local shell version and compare to the manifest's shell
// version. `active` decides WHEN this runs (see the two public hooks below).
function useShellUpdateCore(active: boolean): CloudShellUpdate {
  const shellLatest = useStore((s) => s.updateStatus?.shellLatest ?? null);
  const [current, setCurrent] = useState<string | null>(null);

  useEffect(() => {
    if (!active) return;
    let alive = true;
    void getShellVersion().then((v) => {
      if (alive && v) setCurrent(v);
    });
    return () => {
      alive = false;
    };
  }, [active]);

  if (!active || !current || !shellLatest) {
    return { available: false, current, latest: shellLatest, releaseUrl: null };
  }
  return {
    available: isVersionNewer(shellLatest, current),
    current,
    latest: shellLatest,
    releaseUrl: DESKTOP_DOWNLOAD_URL,
  };
}

// Cloud-launch-mode-only variant — drives the ShellUpdateBanner (the loud nudge
// that only exists because cloud mode has no local daemon to compute the badge).
export function useCloudShellUpdate(): CloudShellUpdate {
  return useShellUpdateCore(isDesktopApp() && isCloudLaunchMode());
}

// General desktop-shell update detector for the AMBIENT indicators (the
// bottom-right version chip + the top update pill). Runs in ANY desktop launch
// mode, so a stale installer surfaces without the daemon needing to see the
// shell — which it can't on a WSL/Linux daemon under a Windows shell. The daemon
// still supplies shellLatest from the same manifest it always reads.
export function useShellUpdate(): CloudShellUpdate {
  return useShellUpdateCore(isDesktopApp());
}
