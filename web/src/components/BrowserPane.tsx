import { useEffect, useRef, useState } from 'react';
import { currentPrefix, currentRoutingScope, relayNeedsTicket } from '../api';
import { browserAccessPath, browserAccessUrl, browserTileSandbox } from '../lib/portBrowser';
import { debugLog } from '../debug';
import { isEmbedded, openEmbedUrl } from '../embed';
import { useStore } from '../store';
import {
  browserPaneClose,
  browserPaneCreate,
  browserPaneNavigate,
  browserPanePing,
  browserPaneSetBounds,
  browserPaneSetVisible,
  isDesktopApp,
  openExternalWindow,
} from '../tauri-browser';
import type { BrowserInfo, BrowserServiceTarget } from '../types';
import { DRAG_MIME, computeZone, type DropZone } from '../lib/paneDnd';
import { toBrowserUrl } from '../lib/urlOrSearch';
import { BrowserStartPage } from './BrowserStartPage';

// BrowserPane renders either a native child webview (desktop app via
// Tauri) or a plain <iframe> (cloud/PWA fallback). The native path
// loads any URL the user throws at it (Google, GitHub, banks) because
// it's not subject to X-Frame-Options / frame-ancestors. The iframe
// path works for localhost dev servers but breaks on most public sites.
//
// Cross-origin caveat in the iframe path: we can't read the iframe's
// document (title, URL, DOM). Refresh and URL changes bust the src so
// it reloads.
//
// Cloud-mode URL rewriting (iframe path only): `http://localhost:<port>`
// is meaningless on a phone connected via the cloud relay. When we
// detect a localhost URL in cloud mode we rewrite it to go through the
// daemon's reverse-proxy route. The URL bar keeps showing the friendly
// localhost form so users know what they're looking at.
export async function wrapForCloud(url: string): Promise<string> {
  return (await browserAccessUrl(url)) ?? '';
}

function useServiceBrowserSrc(url: string, service: BrowserServiceTarget | undefined, nonce: number): string | null {
  const path = browserAccessPath(url,service);
  const proxied = path !== null && relayNeedsTicket(path);
  const key = `${currentRoutingScope().key}|${nonce}|${path}`;
  const [minted,setMinted] = useState<{key:string;src:string|null}|null>(null);
  useEffect(()=>{
    if (!proxied) return;
    let cancelled = false;
    browserAccessUrl(url,service).then(src=>{if(!cancelled)setMinted({key,src});},err=>{
      debugLog('[browser-pane] proxy pass failed',String(err));
      if(!cancelled)setMinted({key,src:null});
    });
    return ()=>{cancelled=true;};
  },[key,proxied,url,service]);
  return proxied ? minted?.key === key ? minted.src : null : path;
}

function canOpenInShinoqueWeb(raw: string): boolean {
  try {
    const url = new URL(raw);
    return (url.protocol === 'http:' || url.protocol === 'https:') && !url.username && !url.password;
  } catch { return false; }
}

export function BrowserPane({ info }: { info: BrowserInfo }) {
  const setBrowserUrl = useStore((s) => s.setBrowserUrl);
  const closeBrowser = useStore((s) => s.closeBrowserInLayout);
  const setFocus = useStore((s) => s.setFocus);
  const focused = useStore((s) => s.focusedPaneId === info.id);
  const movePane = useStore((s) => s.movePaneInLayout);
  const isDraggingPane = useStore((s) => s.isDraggingPane);
  const setDraggingPane = useStore((s) => s.setDraggingPane);

  const iframeRef = useRef<HTMLIFrameElement | null>(null);
  const bodyRef = useRef<HTMLDivElement | null>(null);
  const [draft, setDraft] = useState(info.url);
  const [nonce, setNonce] = useState(0); // iframe reload trigger
  const [dropZone, setDropZone] = useState<DropZone | null>(null);
  const [beingDragged, setBeingDragged] = useState(false);

  // Lock the initial preference to use native vs. iframe to the lifetime
  // of this pane so hot-reload doesn't flip mid-session. `nativeFailed`
  // flips to true if the Rust command is denied by ACL (happens when
  // the Tauri shell MSI is older than the web bundle's expectations)
  // or any other failure path — then we fall back to iframe rendering
  // so the pane isn't just a blank white box.
  const nativeRef = useRef<boolean>(isDesktopApp());
  const [nativeFailed, setNativeFailed] = useState(false);
  const accessPath = browserAccessPath(info.url,info.service);
  const accessUrl = useServiceBrowserSrc(info.url,info.service,nonce);
  const useNative = nativeRef.current && !nativeFailed && !currentPrefix() && !info.service?.backend && accessPath !== null;
  // No URL yet: show the local start page, with no webview or iframe behind it.
  const showStart = !info.url;

  // One-time diagnostic per pane mount. Tells us whether the native
  // path will even be attempted, what Tauri globals are visible, and
  // which Tauri shell version is actually running (via ping). If the
  // ping log doesn't appear in the console, the shell is stale (older
  // MSI without the ping command) and we know to tell the user to
  // reinstall before anything else.
  useEffect(() => {
    const w = window as unknown as {
      __SOLU_TAURI__?: unknown;
      __TAURI_INTERNALS__?: { invoke?: unknown };
    };
    debugLog(
      '[browser-pane] mount id=%s isDesktopApp=%s __SOLU_TAURI__=%s __TAURI_INTERNALS__.invoke=%s',
      info.id,
      nativeRef.current,
      typeof w.__SOLU_TAURI__,
      typeof w.__TAURI_INTERNALS__?.invoke,
    );
    if (nativeRef.current) {
      void browserPanePing().then((v) =>
        debugLog('[browser-pane] ping result shell_version=%s', v),
      );
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  useEffect(() => { setDraft(info.url); }, [info.url]);

  // Native mode: create the child webview on mount, sync bounds each
  // frame, close on unmount. Navigation (URL changes) handled in a
  // separate effect so the webview isn't torn down on every URL edit.
  useEffect(() => {
    if (!useNative || showStart) return;
    const body = bodyRef.current;
    if (!body) return;

    const id = info.id;
    let cancelled = false;
    let rafId = 0;
    let last = { x: -1, y: -1, w: -1, h: -1 };

    function rect() {
      const r = body!.getBoundingClientRect();
      return { x: r.left, y: r.top, w: r.width, h: r.height };
    }

    // Wait up to ~500 ms for react-resizable-panels + flex layout to settle
    // to a non-zero rect that has ALSO STOPPED CHANGING, then create the
    // webview at that final rect. Two problems this guards against:
    //  1. zero-size on cold mount — a webview created at 0×0 or full-window
    //     paints over the app ("fake fullscreen" bug, the original reason this
    //     wait existed).
    //  2. the "opens big then snaps small" flash: on a fresh mount the
    //     PanelGroup briefly reports the new pane at FULL window width before
    //     the split ratio applies, so creating right away lands the webview big
    //     and the tick loop then shrinks it — a visible resize. Requiring the
    //     rect to be unchanged for a couple of frames means we create it at its
    //     FINAL bounds, so it appears already-sized.
    async function waitForStableRect() {
      const started = performance.now();
      let r = rect();
      let prev = r;
      let stableFrames = 0;
      while (!cancelled && performance.now() - started < 500) {
        const sane = r.w >= 4 && r.h >= 4;
        const unchanged =
          Math.round(r.x) === Math.round(prev.x) &&
          Math.round(r.y) === Math.round(prev.y) &&
          Math.round(r.w) === Math.round(prev.w) &&
          Math.round(r.h) === Math.round(prev.h);
        if (sane && unchanged) {
          if (++stableFrames >= 2) break; // sane AND settled for 2 frames
        } else {
          stableFrames = 0;
        }
        prev = r;
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
      debugLog('[browser-pane] init id=%s url=%s rect=%o', id, info.url, r);
      const ok = await browserPaneCreate(id, info.url, r);
      debugLog('[browser-pane] create result id=%s ok=%s', id, ok);
      if (cancelled) return;
      if (!ok) {
        // ACL denied / command missing / runtime error. Fall back to
        // iframe so the user sees SOMETHING instead of a blank pane.
        setNativeFailed(true);
        return;
      }
      rafId = requestAnimationFrame(tick);
    }

    function tick() {
      if (cancelled) return;
      const r = rect();
      // Integer-quantize so sub-pixel jitter doesn't spam IPC.
      if (
        Math.round(r.x) !== Math.round(last.x) ||
        Math.round(r.y) !== Math.round(last.y) ||
        Math.round(r.w) !== Math.round(last.w) ||
        Math.round(r.h) !== Math.round(last.h)
      ) {
        last = r;
        void browserPaneSetBounds(id, r);
      }
      rafId = requestAnimationFrame(tick);
    }

    void init();
    return () => {
      cancelled = true;
      if (rafId) cancelAnimationFrame(rafId);
      void browserPaneClose(id);
    };
    // info.id is stable per pane; don't re-run on info.url changes.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [info.id, useNative, showStart]);

  // Native mode: navigate when the URL or reload nonce changes.
  useEffect(() => {
    if (!useNative || showStart) return;
    void browserPaneNavigate(info.id, info.url);
  }, [info.id, info.url, info.service?.previewPath, nonce, useNative, showStart]);

  // Hide the native child webview while ANY pane drag is in progress.
  // The native webview is an OS-level window that paints above the
  // React DOM, so it would otherwise obscure the drop-zone overlay.
  // Also gives the user visual confirmation that the browser tile is
  // participating in the drag surface.
  useEffect(() => {
    if (!useNative || showStart) return;
    void browserPaneSetVisible(info.id, !isDraggingPane);
  }, [info.id, isDraggingPane, useNative, showStart]);

  function commitUrl(next: string) {
    const url = toBrowserUrl(next, { currentUrl: info.url, useNative });
    if (!url) return;
    setBrowserUrl(info.id, url);
  }

  return (
    <div
      className={`pane browser-pane ${focused ? 'focused' : ''} ${beingDragged ? 'dragging' : ''}`}
      onMouseDownCapture={() => setFocus(info.id)}
      onDragOver={(e) => {
        if (!e.dataTransfer.types.includes(DRAG_MIME)) return;
        e.preventDefault();
        e.dataTransfer.dropEffect = 'move';
        setDropZone(computeZone(e));
      }}
      onDragLeave={(e) => {
        const related = e.relatedTarget as Node | null;
        if (!e.currentTarget.contains(related)) setDropZone(null);
      }}
      onDrop={(e) => {
        if (!e.dataTransfer.types.includes(DRAG_MIME)) return;
        e.preventDefault();
        const src = e.dataTransfer.getData(DRAG_MIME);
        const zone = computeZone(e);
        setDropZone(null);
        if (src && src !== info.id) movePane(src, info.id, zone);
      }}
    >
      <div
        className="pane-header browser-toolbar"
        draggable
        onDragStart={(e) => {
          e.dataTransfer.setData(DRAG_MIME, info.id);
          e.dataTransfer.effectAllowed = 'move';
          setBeingDragged(true);
          setDraggingPane(true);
        }}
        onDragEnd={() => {
          setBeingDragged(false);
          setDraggingPane(false);
        }}
      >
        <span className="activity-dot idle" title={useNative ? 'native browser' : 'internal browser'} />
        <button
          className="iconbtn"
          title="Reload"
          onClick={(e) => {
            e.stopPropagation();
            if (info.service) void useStore.getState().refreshPorts();
            setNonce((n) => n + 1);
          }}
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
          placeholder={useNative ? 'Type a URL or search' : 'http://localhost:3000'}
        />
        {isEmbedded() && (
          <button
            type="button"
            className="iconbtn"
            title="Open in shinoque Web"
            aria-label="Open in shinoque Web"
            disabled={!canOpenInShinoqueWeb(info.url)}
            onClick={(e) => { e.stopPropagation(); openEmbedUrl(info.url); }}
          >↗ Web</button>
        )}
        <button
          className="iconbtn"
          title="Open in a new window"
          onClick={(e) => {
            e.stopPropagation();
            void browserAccessUrl(info.url,info.service).then(url=>{if(url)return openExternalWindow(url,info.title || info.url);}).catch(err=>debugLog('[browser-pane] popout failed',String(err)));
          }}
        >↗</button>
        <div className="pane-actions">
          <button
            className="iconbtn"
            title="Close browser tile"
            onClick={(e) => { e.stopPropagation(); closeBrowser(info.id); }}
          >×</button>
        </div>
      </div>
      <div className="pane-body browser-body" ref={bodyRef}>
        {showStart && <BrowserStartPage onOpen={commitUrl} />}
        {accessPath === null && <p role="status" className="port-notice">This service belongs to a different machine. Open it again from Services on that machine.</p>}
        {!useNative && !showStart && accessUrl !== null && (
          <iframe
            ref={iframeRef}
            key={`${nonce}:${info.service?.previewPath ?? ''}`}
            src={accessUrl}
            title={info.title || info.url}
            sandbox={browserTileSandbox(accessUrl)}
          />
        )}
        {dropZone && !beingDragged && (
          <div className={`drop-zone-overlay zone-${dropZone}`} aria-hidden>
            <span className="drop-zone-hint">
              {dropZone === 'center' ? 'swap' : `insert ${dropZone}`}
            </span>
          </div>
        )}
      </div>
    </div>
  );
}
