import { useEffect, useMemo, useRef, useState } from 'react';
import {
  browserPaneClose,
  browserPaneCreate,
  browserPaneNavigate,
  browserPaneSetBounds,
  isDesktopApp,
} from '../tauri-browser';
import { debugLog } from '../debug';
import { browserTileSandbox } from '../lib/portBrowser';
import { toBrowserUrl } from '../lib/urlOrSearch';

// Fullscreen popout browser. Mounted by App.tsx when the URL contains
// ?popout=1 — at that point we bypass the whole sidebar/PaneGrid UI;
// the window exists to be a single browser tile with address bar,
// reload, and close controls.
//
// The target URL + popout-window label come in via query params set by
// openExternalWindow (tauri-browser.ts). The label matches the Tauri
// window label assigned in Rust, so when we call browser_pane_create
// with parent_label=<that label>, the native child webview attaches to
// THIS popout window instead of the main one.
//
// Iframe fallback exists for cloud/PWA users who happen to hit this
// route without a Tauri shell — though in practice the only way to
// reach a popout is through the Tauri-only openExternalWindow path.

function readPopoutParams() {
  // Rust's open_external_window injects window.__SOLU_POPOUT__ via
  // init_script before any page script runs — see browser.rs.
  // Reading the global (instead of URL query params) guarantees the
  // daemon's auth-redirect flow can't strip our popout markers on the
  // way from the Tauri shell to React mount.
  const g = (window as unknown as {
    __SOLU_POPOUT__?: { url?: string; title?: string; parentLabel?: string };
  }).__SOLU_POPOUT__;
  return {
    url: g?.url ?? '',
    title: g?.title ?? '',
    parentLabel: g?.parentLabel ?? undefined,
  };
}

function wrapForCloudInPopout(url: string): string {
  // In Tauri mode the native child webview handles everything, so cloud
  // URL wrapping is only relevant for the iframe-fallback branch. Popouts
  // are almost always desktop-only, so keep this small and inline.
  try {
    const u = new URL(url);
    if (/^(localhost|127\.0\.0\.1)$/i.test(u.hostname)) return url;
  } catch { /* ignore */ }
  return url;
}

// Fixed toolbar height in CSS pixels, mirrored by the .popout-toolbar
// CSS rule in styles.css. Kept as a shared constant because the child
// webview rect falls back to (window.innerWidth, window.innerHeight -
// this) when the body's getBoundingClientRect hasn't settled yet.
const POPOUT_TOOLBAR_HEIGHT = 44;

export function PopoutBrowser() {
  const { url: initialUrl, title: initialTitle, parentLabel } = useMemo(readPopoutParams, []);
  const [url, setUrl] = useState(initialUrl);
  const [draft, setDraft] = useState(initialUrl);
  const [nonce, setNonce] = useState(0); // iframe reload trigger
  const iframeRef = useRef<HTMLIFrameElement | null>(null);
  const bodyRef = useRef<HTMLDivElement | null>(null);

  // Stable pane id for this popout's native child webview. Not from the
  // layout store — popouts don't participate in the grid.
  const paneId = useMemo(
    () => 'popout_' + Math.random().toString(36).slice(2, 10),
    [],
  );
  const nativeRef = useRef<boolean>(isDesktopApp());
  const [nativeFailed, setNativeFailed] = useState(false);
  const useNative = nativeRef.current && !nativeFailed;

  // Reflect URL changes in the address-bar draft when the user navigates
  // via the address bar (no onNavigation feedback yet from the child
  // webview, so the draft is the source of truth).
  useEffect(() => { setDraft(url); }, [url]);

  // Close the popout window on Escape. Failsafe in case the native
  // child webview ever obscures the in-app close button — the webview
  // doesn't receive this keystroke (it's focused on the child), but if
  // the user clicks the toolbar first, focus returns to the popout's
  // main webview and Esc lands here.
  useEffect(() => {
    const handler = (e: KeyboardEvent) => {
      if (e.key === 'Escape') window.close();
    };
    window.addEventListener('keydown', handler);
    return () => window.removeEventListener('keydown', handler);
  }, []);

  // Native mode: create the child webview on mount + track the body's
  // rect so the webview stays pinned beneath the toolbar as the window
  // resizes. Both sources of truth: prefer the DOM body rect (matches
  // whatever the absolute-positioned layout actually rendered), fall
  // back to window.innerWidth/Height minus the toolbar constant when
  // the body div hasn't laid out yet.
  useEffect(() => {
    if (!useNative) return;

    let cancelled = false;
    let rafId = 0;
    let last = { x: -1, y: -1, w: -1, h: -1 };

    function rect() {
      const el = bodyRef.current;
      if (el) {
        const r = el.getBoundingClientRect();
        if (r.width >= 4 && r.height >= 4) {
          return { x: r.left, y: r.top, w: r.width, h: r.height };
        }
      }
      // Fallback when the body ref isn't ready or the measurement came
      // back degenerate. window.innerHeight is always available after
      // window load, and the toolbar is absolute-positioned at height
      // POPOUT_TOOLBAR_HEIGHT so y=toolbarHeight is the body top.
      return {
        x: 0,
        y: POPOUT_TOOLBAR_HEIGHT,
        w: window.innerWidth,
        h: Math.max(0, window.innerHeight - POPOUT_TOOLBAR_HEIGHT),
      };
    }

    async function waitForStableRect() {
      const started = performance.now();
      let r = rect();
      while (
        !cancelled &&
        (r.w < 4 || r.h < 4 || r.y < POPOUT_TOOLBAR_HEIGHT - 2) &&
        performance.now() - started < 500
      ) {
        await new Promise<void>((resolve) =>
          requestAnimationFrame(() => resolve()),
        );
        r = rect();
      }
      return r;
    }

    async function init() {
      const r = await waitForStableRect();
      if (cancelled) return;
      last = r;
      debugLog('[popout] init id=%s url=%s parent=%s rect=%o inner=%dx%d', paneId, url, parentLabel, r, window.innerWidth, window.innerHeight);
      const ok = await browserPaneCreate(paneId, url, r, { parentLabel });
      debugLog('[popout] create result id=%s ok=%s', paneId, ok);
      if (cancelled) return;
      if (!ok) { setNativeFailed(true); return; }
      rafId = requestAnimationFrame(tick);
    }

    function tick() {
      if (cancelled) return;
      const r = rect();
      if (
        Math.round(r.x) !== Math.round(last.x) ||
        Math.round(r.y) !== Math.round(last.y) ||
        Math.round(r.w) !== Math.round(last.w) ||
        Math.round(r.h) !== Math.round(last.h)
      ) {
        last = r;
        void browserPaneSetBounds(paneId, r);
      }
      rafId = requestAnimationFrame(tick);
    }

    void init();
    return () => {
      cancelled = true;
      if (rafId) cancelAnimationFrame(rafId);
      void browserPaneClose(paneId);
    };
    // initial url is captured above; navigation is handled in the next
    // effect so we don't re-create the webview on every address change.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [useNative, paneId, parentLabel]);

  useEffect(() => {
    if (!useNative) return;
    void browserPaneNavigate(paneId, url);
  }, [useNative, paneId, url, nonce]);

  // Update the OS window title as the URL changes so the tray / taskbar
  // entry reflects what the user is looking at. Purely cosmetic — the
  // underlying native webview handles everything else.
  useEffect(() => {
    const base = initialTitle || 'Browser';
    try {
      document.title = `${base} — ${new URL(url).host}`;
    } catch {
      document.title = base;
    }
  }, [url, initialTitle]);

  function commitUrl(next: string) {
    const u = toBrowserUrl(next, { currentUrl: url, useNative });
    if (!u) return;
    setUrl(u);
  }

  return (
    <div className="popout-root">
      <div className="pane-header browser-toolbar popout-toolbar">
        <span className="activity-dot idle" title={useNative ? 'native browser' : 'iframe fallback'} />
        <button
          type="button"
          className="iconbtn"
          title="Reload"
          onClick={() => setNonce((n) => n + 1)}
        >⟳</button>
        <input
          className="browser-url"
          value={draft}
          onChange={(e) => setDraft(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === 'Enter') {
              e.preventDefault();
              commitUrl(draft);
              setNonce((n) => n + 1);
            }
          }}
          spellCheck={false}
          autoComplete="off"
          placeholder={useNative ? 'Search Google or type a URL' : 'http://localhost:3000'}
        />
        <div className="pane-actions">
          <button
            type="button"
            className="iconbtn"
            title="Close window"
            onClick={() => window.close()}
          >×</button>
        </div>
      </div>
      <div className="pane-body browser-body popout-body" ref={bodyRef}>
        {!useNative && (
          <iframe
            ref={iframeRef}
            key={nonce}
            src={wrapForCloudInPopout(url)}
            title={initialTitle || url}
            sandbox={browserTileSandbox(wrapForCloudInPopout(url))}
          />
        )}
      </div>
    </div>
  );
}
