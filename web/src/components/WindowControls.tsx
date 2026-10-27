// Shared custom-titlebar window-control cluster used by Topbar (main/secondary
// workspace windows) and TasksPopout. Hosts must be Tauri windows built with
// decorations(false).

import { isDesktopApp, tauriInvoke } from '../tauri-browser';

export { isDesktopApp as isTauriShell } from '../tauri-browser';

function markDesktopWillBackground(reason: string) {
  window.dispatchEvent(new CustomEvent('solu:desktop-will-background', { detail: { reason } }));
}

async function winMinimize() {
  const inv = tauriInvoke();
  if (!inv) return;
  markDesktopWillBackground('minimize');
  try { await inv('plugin:window|minimize'); } catch { /* ignore */ }
}

async function winToggleMaximize() {
  const inv = tauriInvoke();
  if (!inv) return;
  try { await inv('plugin:window|toggle_maximize'); } catch { /* ignore */ }
}

async function winClose() {
  const inv = tauriInvoke();
  if (!inv) return;
  markDesktopWillBackground('close');
  try { await inv('plugin:window|close'); } catch { /* ignore */ }
}

export function WindowControls({ children }: { children?: React.ReactNode }) {
  if (!isDesktopApp()) return null;
  return (
    <div className="window-controls" data-tauri-drag-region="false">
      {children}
      <button
        type="button"
        className="window-control"
        onClick={winMinimize}
        aria-label="Minimize"
        title="Minimize"
      >
        <svg width="10" height="10" viewBox="0 0 10 10" aria-hidden="true">
          <rect x="1" y="4.5" width="8" height="1" fill="currentColor" />
        </svg>
      </button>
      <button
        type="button"
        className="window-control"
        onClick={winToggleMaximize}
        aria-label="Maximize"
        title="Maximize"
      >
        <svg width="10" height="10" viewBox="0 0 10 10" aria-hidden="true">
          <rect x="1" y="1" width="8" height="8" fill="none" stroke="currentColor" strokeWidth="1" />
        </svg>
      </button>
      <button
        type="button"
        className="window-control window-control-close"
        onClick={winClose}
        aria-label="Close"
        title="Close"
      >
        <svg width="10" height="10" viewBox="0 0 10 10" aria-hidden="true">
          <path d="M1 1 L9 9 M9 1 L1 9" stroke="currentColor" strokeWidth="1" fill="none" />
        </svg>
      </button>
    </div>
  );
}
