import { useEffect } from 'react';
import { useAttentionBadge } from '../components/AttentionInbox';

// Surfaces the "N panes waiting on you" count OUTSIDE the app chrome, so a
// backgrounded tab / PWA shows it without switching back:
//   - document.title  → "(2) soluterminal"
//   - favicon         → app icon with a dot (amber = waiting, red = approval)
//   - app badge       → navigator.setAppBadge where the platform supports it
//                       (installed PWAs on Chromium/Edge, macOS Safari PWAs)
// Same single source of truth as the topbar bell (useAttentionBadge), so all
// surfaces agree by construction.

export function attentionTitle(base: string, count: number): string {
  const clean = base.replace(/^\(\d+\+?\)\s+/, '');
  if (count <= 0) return clean;
  return `(${count > 99 ? '99+' : count}) ${clean}`;
}

let baseIcon: HTMLImageElement | null = null;
let baseIconLoaded: Promise<HTMLImageElement> | null = null;

// Loads the page's own icon (the first <link rel=icon>, resolved against the
// document — the app may be served under a relay prefix, not at /).
function loadBaseIcon(src: string): Promise<HTMLImageElement> {
  if (baseIconLoaded) return baseIconLoaded;
  baseIconLoaded = new Promise((resolve, reject) => {
    const img = new Image();
    img.onload = () => { baseIcon = img; resolve(img); };
    img.onerror = () => { baseIconLoaded = null; reject(new Error('icon load failed')); };
    img.src = src;
  });
  return baseIconLoaded;
}

function iconLinks(): HTMLLinkElement[] {
  return Array.from(document.querySelectorAll<HTMLLinkElement>('link[rel="icon"]'));
}

let paintGen = 0;

async function paintFavicon(count: number, choice: boolean) {
  const gen = ++paintGen;
  const links = iconLinks();
  if (links.length === 0) return;
  if (count <= 0) {
    for (const l of links) {
      if (l.dataset.soluOrig) l.href = l.dataset.soluOrig;
      if (l.dataset.soluOrigType !== undefined) l.type = l.dataset.soluOrigType;
    }
    return;
  }
  for (const l of links) {
    if (!l.dataset.soluOrig) l.dataset.soluOrig = l.href;
    if (l.dataset.soluOrigType === undefined) l.dataset.soluOrigType = l.type;
  }
  let img = baseIcon;
  try { img = img ?? await loadBaseIcon(links[0].dataset.soluOrig ?? links[0].href); } catch { return; }
  if (gen !== paintGen) return; // a newer count arrived while the icon loaded
  const size = 64;
  const canvas = document.createElement('canvas');
  canvas.width = size;
  canvas.height = size;
  const ctx = canvas.getContext('2d');
  if (!ctx) return;
  ctx.drawImage(img, 0, 0, size, size);
  const r = size * 0.24;
  ctx.beginPath();
  ctx.arc(size - r - 1, r + 1, r, 0, Math.PI * 2);
  ctx.fillStyle = choice ? '#e5484d' : '#f5a524';
  ctx.fill();
  ctx.lineWidth = 3;
  ctx.strokeStyle = '#ffffff';
  ctx.stroke();
  let url: string;
  try { url = canvas.toDataURL('image/png'); } catch { return; }
  // The favicon.svg link would otherwise announce SVG for this PNG.
  for (const l of links) {
    l.type = 'image/png';
    l.href = url;
  }
}

function setAppBadge(count: number) {
  const nav = navigator as Navigator & {
    setAppBadge?: (n?: number) => Promise<void>;
    clearAppBadge?: () => Promise<void>;
  };
  try {
    if (count > 0) void nav.setAppBadge?.(count)?.catch(() => {});
    else void nav.clearAppBadge?.()?.catch(() => {});
  } catch { /* unsupported */ }
}

export function useAttentionOutsideBadges() {
  const { count, hasChoice } = useAttentionBadge();
  useEffect(() => {
    document.title = attentionTitle(document.title, count);
    void paintFavicon(count, hasChoice);
    setAppBadge(count);
  }, [count, hasChoice]);
}
