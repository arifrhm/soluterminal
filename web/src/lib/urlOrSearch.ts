// Address-bar input normalization. Handles the same input modes as
// Chrome's omnibox:
//   * Explicit scheme (http://…, https://…, file://…)          → navigate
//   * Port shortcut ("3000" / "8080/api")                        → http://localhost:<port>…
//   * Bare path ("/foo") with a current URL                     → current origin + path
//   * Host-looking string ("google.com", "localhost:3000",
//     "192.168.1.1:8080", "foo.dev")                            → prepend scheme, navigate
//   * Everything else (multi-word, no TLD, no port)             → Google search
//
// Keep the search URL a constant so it's easy to find + swap (ddg,
// kagi, etc. in a future preference).

const SEARCH_URL = 'https://www.google.com/search?q=';

export function isLikelyUrl(input: string): boolean {
  const s = input.trim();
  if (!s) return false;

  // Explicit scheme.
  if (/^(https?|ftp|file|about|chrome|edge):/i.test(s)) return true;

  // localhost / 127.0.0.1 / IPv4, with optional port + path.
  if (/^localhost(:\d+)?([/?#]|$)/i.test(s)) return true;
  if (/^127\.0\.0\.1(:\d+)?([/?#]|$)/.test(s)) return true;
  if (/^\d{1,3}(\.\d{1,3}){3}(:\d+)?([/?#]|$)/.test(s)) return true;

  // Port-only shortcut ("3000", "8080/foo"). Intercepted here so it
  // doesn't get caught by the whitespace check below as a search query.
  if (/^\d+(\/|$)/.test(s)) return true;

  // Bare path — only meaningful when a current URL exists.
  if (s.startsWith('/')) return true;

  // Host-looking: no whitespace + at least one dot + letters-only TLD.
  // "foo.com", "foo.com/bar", "a.b.c.d" all qualify. "hello world" and
  // "what is typescript" don't.
  if (!/\s/.test(s) && /^[\w-]+(\.[\w-]+)+(:\d+)?([/?#]|$)/.test(s)) return true;

  return false;
}

// Resolve the raw address-bar text into a navigable URL. `currentUrl`
// is used to resolve bare paths ("/foo") against the current origin.
// `useNative` influences the default scheme for host-only strings —
// https when a native webview is available (desktop app), http when
// falling back to <iframe> (cloud/PWA) since most in-iframe targets
// are local dev servers.
export function toBrowserUrl(
  input: string,
  opts: { currentUrl?: string; useNative: boolean } = { useNative: true },
): string {
  const s = input.trim();
  if (!s) return '';

  if (!isLikelyUrl(s)) {
    return SEARCH_URL + encodeURIComponent(s);
  }

  if (/^(https?|ftp|file|about|chrome|edge):/i.test(s)) return s;

  if (/^\d+(\/|$)/.test(s)) return 'http://localhost:' + s;

  if (s.startsWith('/') && opts.currentUrl) {
    try {
      const cur = new URL(opts.currentUrl);
      return cur.origin + s;
    } catch {
      return 'http://' + s.replace(/^\//, '');
    }
  }

  return (opts.useNative ? 'https://' : 'http://') + s;
}
