package main

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// absPathAttrRE matches HTML attributes whose value is an absolute path
// reference (e.g. `src="/foo.js"`, `href='/main.css'`). We rewrite
// these to carry the full proxy prefix so the browser fetches them
// through our proxy instead of hitting the cloud origin root. Covers
// the common attribute names used by browsers to resolve URLs; CSS
// `url(...)` gets its own pass (cssURLAbsRE — stylesheet responses and
// inline <style> blocks); inline SVG and JS `import '/foo'` statements
// inside script bodies remain a known limitation (see package doc).
var absPathAttrRE = regexp.MustCompile(
	`(?i)(\b(?:src|href|action|formaction|cite|longdesc|usemap|data|background|poster)\s*=\s*)(["'])(/[^"'/][^"']*)(["'])`,
)

// cssURLAbsRE matches CSS url(...) references to absolute paths —
// url(/img/x.png), url("/fonts/a.woff2"), url('/x.svg') — while leaving
// protocol-relative (url(//cdn…)), data:/blob: URIs, full URLs, and
// relative paths alone.
var cssURLAbsRE = regexp.MustCompile(`(?i)\burl\(\s*(["']?)(/[^"'()/][^"'()]*)(["']?)\s*\)`)

// styleBlockRE isolates inline <style> elements so the CSS pass can run
// on exactly their contents — running it on the whole HTML body could
// corrupt a JS `url(...)` call inside an inline script.
var styleBlockRE = regexp.MustCompile(`(?is)(<style[^>]*>)(.*?)(</style>)`)

// Reverse-proxy endpoint — `/api/proxy/{port}/*` forwards to
// `http://127.0.0.1:{port}/*`, so a browser connected via the cloud
// relay can load a dev server running on the daemon host. The relay
// already tunnels HTTP (and hijacked WebSocket upgrades) to the
// daemon, so "just add a proxy route" gets remote localhost access
// for free.
//
// Security: we only proxy ports the PortRegistry knows as PANE-OWNED —
// listening sockets started inside a soluterminal pane (that's what
// PortRegistry.Has gates on; List still shows every same-user listener
// for the panel, but only pane-owned ones pass here) — so the proxy
// can't be abused to hit arbitrary localhost services (databases,
// admin consoles) the user didn't start inside soluterminal. If the
// user needs to proxy a service running in e.g. Docker, the registry
// sees it but won't proxy it; that's an intentional limit for v1. Add
// an opt-in allow-any-port toggle later if the Docker case becomes
// common.

// proxyPort handles /api/proxy/{port}/*. The {port} path segment is
// parsed and checked against the registry; the rest of the path +
// query is forwarded to 127.0.0.1:{port}. Supports WebSocket
// upgrades: httputil.ReverseProxy handles Connection/Upgrade
// preservation automatically since Go 1.12.
func (a *apiServer) proxyPort(w http.ResponseWriter, r *http.Request) {
	// Extract {port} from r.URL.Path. The router pattern is
	// "/api/proxy/{port}/" which doesn't use Go 1.22's {var} because
	// we also want to match "/api/proxy/{port}" (no trailing slash,
	// no rest path) cleanly. Easier to parse manually.
	path := strings.TrimPrefix(r.URL.Path, "/api/proxy/")
	if path == "" || path == r.URL.Path {
		http.Error(w, "missing port in path", http.StatusBadRequest)
		return
	}
	portStr := path
	rest := "/"
	if idx := strings.IndexByte(path, '/'); idx >= 0 {
		portStr = path[:idx]
		rest = path[idx:]
	}
	portText, selector, _ := strings.Cut(portStr, "~")
	portNum, err := strconv.ParseUint(portText, 10, 16)
	if err != nil || portNum == 0 {
		http.Error(w, "invalid port", http.StatusBadRequest)
		return
	}

	// Allowlist — see package comment.
	var listener *Port
	if a.ports != nil {
		listener = a.ports.resolve(uint16(portNum), selector)
	}
	if listener == nil {
		http.Error(w,
			"This listener is unavailable or is not owned by a soluterminal pane. Refresh Services and open the current service.",
			http.StatusForbidden)
		return
	}

	// The "public-facing" prefix for rewrites — local mode is empty,
	// cloud mode is "/m/<mid>" forwarded by the cloud relay. Combined
	// with our /api/proxy/<port>/ path this is what the browser's base
	// URL-resolution actually sees.
	forwardedPrefix := r.Header.Get("X-Forwarded-Prefix")
	proxyBase := forwardedPrefix + "/api/proxy/" + portStr
	if handleCloudProxyPreflight(w, r, forwardedPrefix) {
		return
	}

	// Path-rewritten target. Build the Director closure so each
	// request uses its own rest+port (the shared registry means the
	// same route may serve many ports).
	scheme := listener.Scheme
	if scheme != "https" {
		scheme = "http"
	}
	target := &url.URL{Scheme: scheme, Host: portHost(listener)}
	transport := a.ports.proxyTransport(listener)
	rp := &httputil.ReverseProxy{
		Transport: transport,
		Director: func(req *http.Request) {
			rewriteProxyPortRequest(req, target, rest, forwardedPrefix)
		},
		ModifyResponse: func(resp *http.Response) error {
			// Strip headers that block iframe embedding. Dev servers
			// sometimes set these via frameworks (e.g. Next.js with
			// `poweredByHeader: true`, or a proxy in front) even
			// though at dev time it's unwanted. Every response gets a
			// replacement CSP sandbox below so no document from it runs
			// as the app's (daemon, router or cloud) same origin.
			resp.Header.Del("X-Frame-Options")
			resp.Header.Del("Content-Security-Policy")
			resp.Header.Del("Content-Security-Policy-Report-Only")
			if proxyRouteSandboxed(forwardedPrefix) {
				applyProxyDocumentSandbox(resp)
			}
			applyProxyCORS(resp.Header, forwardedPrefix)

			// Rewrite absolute redirect Locations that point back at
			// localhost — dev servers sometimes do this on 3xx and
			// it would break the proxy chain. Leave relative paths
			// and external URLs alone.
			if loc := resp.Header.Get("Location"); loc != "" {
				if rewritten := rewriteLocation(loc, portStr, r); rewritten != "" {
					resp.Header.Set("Location", rewritten)
				}
			}

			// HTML rewriting — fix absolute-path src/href attributes
			// so dev-server assets load through our proxy instead of
			// resolving against the cloud origin root. Only applies
			// to text/html bodies; binary responses stream through.
			if isHTML(resp.Header.Get("Content-Type")) {
				if err := rewriteHTMLBody(resp, proxyBase, proxyAccessToken(r)); err != nil {
					return err
				}
			} else if isCSS(resp.Header.Get("Content-Type")) {
				// Stylesheets: url(/img/x.png) resolves against the
				// origin root — the one asset class neither the HTML
				// attribute pass nor the fetch shim can reach.
				if err := rewriteCSSBody(resp, proxyBase, proxyAccessToken(r)); err != nil {
					return err
				}
			}
			return nil
		},
		// Tell the proxy which errors to surface to the client. The
		// default writes a bland 502; we include the target info for
		// quick diagnosis.
		ErrorHandler: func(rw http.ResponseWriter, _ *http.Request, err error) {
			http.Error(rw,
				"preview of "+target.Host+" failed: "+err.Error()+
					" — is the dev server actually listening?",
				http.StatusBadGateway)
		},
	}
	rp.ServeHTTP(w, r)
}

func rewriteProxyPortRequest(req *http.Request, target *url.URL, rest string, forwardedPrefix string) {
	routerAuthenticated := req.Header.Get("X-Solu-Proxy-Auth") == "router"
	req.Header.Del("X-Solu-Proxy-Auth")
	req.URL.Scheme = target.Scheme
	req.URL.Host = target.Host
	req.URL.Path = rest
	req.URL.RawPath = ""
	// Preserve query string from the original request — it's already on
	// req.URL at this point; no-op but explicit. Make sure the dev server
	// sees a Host header matching the target so any Host-based routing
	// works. Many dev servers don't care; Vite and Next.js do look at it.
	req.Host = target.Host
	// Request uncompressed bodies so we can rewrite HTML bodies in
	// ModifyResponse without a gzip round-trip. Dev-server payloads are
	// small; the transfer cost is negligible and the code is simpler.
	req.Header.Set("Accept-Encoding", "identity")
	// Drop the proxy-facing X-Forwarded-* so the dev server doesn't see
	// "localhost" through a weird lens; the ReverseProxy would otherwise
	// add them pointing at the cloud relay's origin, which confuses stuff
	// like Next.js redirect handling.
	req.Header.Del("X-Forwarded-For")
	req.Header.Del("X-Forwarded-Host")
	req.Header.Del("X-Forwarded-Proto")
	req.Header.Del("X-Forwarded-Prefix")
	if isCloudForwardedPrefix(forwardedPrefix) {
		req.Header.Del("Authorization")
		req.Header.Del("Cookie")
	} else if routerAuthenticated || strings.HasPrefix(forwardedPrefix, "/b/") {
		// The desktop router supplies the daemon's master bearer credential,
		// which must never be forwarded into a user's development server.
		req.Header.Del("Authorization")
		cookies := req.Cookies()
		req.Header.Del("Cookie")
		for _, cookie := range cookies {
			if cookie.Name != "solu_session" {
				req.AddCookie(cookie)
			}
		}
	}
	q := req.URL.Query()
	q.Del("access_token")
	req.URL.RawQuery = q.Encode()
}

// Pool only within one listener identity. A replacement process on the same
// port must not reuse a connection retained from the previous application.
func (pr *PortRegistry) proxyTransport(p *Port) *http.Transport {
	key := portSelector(p)
	pr.mu.RLock()
	transport := pr.transports[key]
	pr.mu.RUnlock()
	if transport != nil {
		return transport
	}
	pr.mu.Lock()
	defer pr.mu.Unlock()
	if pr.transports == nil {
		pr.transports = map[string]*http.Transport{}
	}
	if transport := pr.transports[key]; transport != nil {
		return transport
	}
	transport = http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	transport.MaxIdleConnsPerHost = 8
	transport.IdleConnTimeout = 30 * time.Second
	pr.transports[key] = transport
	return transport
}

// isHTML is a relaxed content-type match — covers `text/html`,
// `text/html; charset=utf-8`, and the occasional `application/xhtml+xml`.
func isHTML(ct string) bool {
	ct = strings.ToLower(ct)
	return strings.HasPrefix(ct, "text/html") || strings.HasPrefix(ct, "application/xhtml+xml")
}

// isCSS is the stylesheet equivalent — `text/css` with or without params.
func isCSS(ct string) bool {
	return strings.HasPrefix(strings.ToLower(ct), "text/css")
}

// rewriteCSSBytes prefixes absolute-path url(...) references with the
// proxy base. Shared by whole-stylesheet responses and the inline
// <style> pass inside rewriteHTMLBody.
func rewriteCSSBytes(css []byte, proxyBase string, accessToken string) []byte {
	return cssURLAbsRE.ReplaceAllFunc(css, func(match []byte) []byte {
		parts := cssURLAbsRE.FindSubmatch(match)
		if len(parts) != 4 {
			return match
		}
		if bytes.HasPrefix(parts[2], []byte(proxyBase)) {
			return match
		}
		u := appendProxyAccessToken(proxyBase+string(parts[2]), accessToken)
		return []byte("url(" + string(parts[1]) + u + string(parts[3]) + ")")
	})
}

// rewriteCSSBody applies rewriteCSSBytes to a whole text/css response,
// updating Content-Length to the new byte count.
func rewriteCSSBody(resp *http.Response, proxyBase string, accessToken string) error {
	body, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		return err
	}
	rewritten := rewriteCSSBytes(body, proxyBase, accessToken)
	resp.Body = io.NopCloser(bytes.NewReader(rewritten))
	resp.ContentLength = int64(len(rewritten))
	resp.Header.Set("Content-Length", strconv.Itoa(len(rewritten)))
	return nil
}

const proxyDocumentSandboxCSP = "sandbox allow-downloads allow-forms allow-modals allow-pointer-lock allow-popups allow-popups-to-escape-sandbox allow-presentation allow-scripts allow-top-navigation-by-user-activation"

// applyProxyDocumentSandbox gives whatever a pane's server sends an opaque
// origin. The proxy is served from the app's own origin (the daemon on
// loopback, the desktop router, or the cloud), so a page or SVG from it
// would otherwise run as the app: read its storage and make same-origin
// requests the daemon takes for the person's (an agent only has to serve one
// and open_browser it). Every response, not only HTML: a sandbox directive
// is ignored on subresources, and SVG or XML documents run scripts too.
// proxyRouteSandboxed: every proxy route except the desktop router's
// /b/<backend> one, whose previews need an origin of their own first (the
// follow-up), or they lose their scripts and data.
func proxyRouteSandboxed(forwardedPrefix string) bool {
	return !strings.HasPrefix(forwardedPrefix, "/b/")
}

func applyProxyDocumentSandbox(resp *http.Response) {
	resp.Header.Set("Content-Security-Policy", proxyDocumentSandboxCSP)
}

func handleCloudProxyPreflight(w http.ResponseWriter, r *http.Request, forwardedPrefix string) bool {
	if !isCloudForwardedPrefix(forwardedPrefix) || r.Method != http.MethodOptions || r.Header.Get("Access-Control-Request-Method") == "" {
		return false
	}
	applyProxyCORS(w.Header(), forwardedPrefix)
	if headers := r.Header.Get("Access-Control-Request-Headers"); headers != "" {
		w.Header().Set("Access-Control-Allow-Headers", headers)
	}
	w.WriteHeader(http.StatusNoContent)
	return true
}

// applyProxyCORS lets a sandboxed (opaque-origin) preview reach its own
// server through the cloud's /m/<machine> route, which the app opens remote
// previews through. Unprefixed local /api/proxy is not how the app opens a
// preview (it uses the localhost URL), and the desktop router's route is not
// sandboxed yet, so neither gets cross-origin access.
func applyProxyCORS(h http.Header, forwardedPrefix string) {
	if !isCloudForwardedPrefix(forwardedPrefix) {
		return
	}
	h.Set("Access-Control-Allow-Origin", "null")
	h.Set("Access-Control-Allow-Credentials", "true")
	h.Set("Access-Control-Allow-Methods", "GET, HEAD, POST, PUT, PATCH, DELETE, OPTIONS")
	h.Set("Access-Control-Allow-Private-Network", "true")
	h.Add("Vary", "Origin")
	h.Add("Vary", "Access-Control-Request-Method")
	h.Add("Vary", "Access-Control-Request-Headers")
}

// proxyAccessToken is the credential the cloud relay passed through for this
// dev-server page. Current web clients send a proxy pass scoped to this one
// machine and port (cloud/relay_ticket.go), not the sign-in token, because the
// value is copied into every sub-resource URL and the cloud host logs URLs.
func proxyAccessToken(r *http.Request) string {
	if !isCloudForwardedPrefix(r.Header.Get("X-Forwarded-Prefix")) {
		return ""
	}
	return r.URL.Query().Get("access_token")
}

func appendProxyAccessToken(raw string, accessToken string) string {
	if accessToken == "" {
		return raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	q := u.Query()
	if q.Get("access_token") == "" {
		q.Set("access_token", accessToken)
		u.RawQuery = q.Encode()
	}
	return u.String()
}

func isCloudForwardedPrefix(forwardedPrefix string) bool {
	return strings.HasPrefix(forwardedPrefix, "/m/")
}

// runtimeRewriterScript is injected at the top of every HTML response
// we proxy. It monkey-patches fetch / XMLHttpRequest / EventSource /
// WebSocket so absolute-path URLs (fetch('/api/thing'),
// `new EventSource('/events')`, etc.) made from the app's JS get the
// proxy prefix prepended at call time.
//
// Without this, HTML-attribute rewriting only helps static tags —
// a SPA's runtime fetch / SSE / WS calls to `/…` escape the proxy and
// hit the cloud origin's SPA fallback, getting index.html and crashing
// the caller (SyntaxError on JSON parse, MIME mismatch on EventSource,
// etc.).
//
// Templated with {{BASE}} so the same script covers both local mode
// (`/api/proxy/PORT`) and cloud mode (`/m/<mid>/api/proxy/PORT`).
// We inject right after <head> so it runs before any of the app's
// own scripts — order matters: if the app's script reads
// `window.fetch` at module-init time, our patch has to already be in
// place.
const runtimeRewriterScript = `<script>
(function() {
  var BASE = {{BASE}};
  var ACCESS_TOKEN = "";
  try {
    ACCESS_TOKEN = new URLSearchParams(window.location.search).get("access_token") || "";
  } catch (e) { /* leave empty */ }
  function appendAccessToken(u) {
    if (!ACCESS_TOKEN) return u;
    try {
      var parsed = new URL(u, window.location.href);
      if (parsed.pathname.indexOf(BASE) !== 0 || parsed.searchParams.has("access_token")) return u;
      parsed.searchParams.set("access_token", ACCESS_TOKEN);
      if (u.charAt(0) === "/") return parsed.pathname + parsed.search + parsed.hash;
      return parsed.toString();
    } catch (e) {
      return u;
    }
  }
  // rewriteStr handles both shapes an app might build:
  //   (a) origin-relative: "/events", "/api/foo"
  //   (b) full same-origin URL: "https://host/events" or "http://host:443/x"
  // Leaves untouched: protocol-relative ("//cdn…"), cross-origin URLs,
  // and relative paths ("./foo", "bar"). Silent on non-string inputs.
  function rewriteStr(u) {
    if (typeof u !== 'string' || u.length === 0) return u;
    // Case (a): bare absolute path, not protocol-relative.
    if (u.charAt(0) === '/' && (u.length < 2 || u.charAt(1) !== '/')) {
      if (u.indexOf(BASE) !== 0) return appendAccessToken(BASE + u);
      return appendAccessToken(u);
    }
    // Case (b): might be a full URL. Try parsing; if same origin and
    // not already prefixed, prepend BASE to the pathname.
    try {
      var parsed = new URL(u, window.location.href);
      if (parsed.host === window.location.host && parsed.pathname.indexOf(BASE) === 0) {
        return appendAccessToken(parsed.toString());
      }
      if (parsed.host === window.location.host && parsed.pathname.charAt(0) === '/' && parsed.pathname.indexOf(BASE) !== 0) {
        parsed.pathname = BASE + parsed.pathname;
        if (ACCESS_TOKEN && !parsed.searchParams.has("access_token")) parsed.searchParams.set("access_token", ACCESS_TOKEN);
        return parsed.toString();
      }
    } catch (e) { /* not a URL — leave alone */ }
    return u;
  }
  // fetch — both string URLs and Request objects. For Request, we
  // rebuild with the rewritten URL using the old Request as init so
  // most options (method, headers, mode, credentials…) are preserved.
  // For requests with a streaming body (POST with FormData / blob etc.)
  // we leave the Request alone — rebuilding would consume the body.
  if (typeof fetch === 'function') {
    var origFetch = fetch;
    window.fetch = function(input, init) {
      if (typeof input === 'string') {
        return origFetch.call(this, rewriteStr(input), init);
      }
      if (typeof Request !== 'undefined' && input instanceof Request) {
        try {
          var newUrl = rewriteStr(input.url);
          if (newUrl !== input.url) {
            // bodyUsed check avoids re-reading a consumed stream.
            // For a bodyless request (GET/HEAD), the rebuild is safe.
            if (!input.bodyUsed) {
              input = new Request(newUrl, input);
            }
          }
        } catch (e) { /* fall through to original */ }
      }
      return origFetch.call(this, input, init);
    };
  }
  // XMLHttpRequest.open — second arg is the URL.
  if (typeof XMLHttpRequest !== 'undefined') {
    var origOpen = XMLHttpRequest.prototype.open;
    XMLHttpRequest.prototype.open = function(method, url) {
      arguments[1] = rewriteStr(url);
      return origOpen.apply(this, arguments);
    };
  }
  // EventSource constructor.
  if (typeof EventSource !== 'undefined') {
    var OrigES = EventSource;
    function PatchedES(url, opts) { return new OrigES(rewriteStr(url), opts); }
    PatchedES.prototype = OrigES.prototype;
    PatchedES.CONNECTING = OrigES.CONNECTING;
    PatchedES.OPEN = OrigES.OPEN;
    PatchedES.CLOSED = OrigES.CLOSED;
    window.EventSource = PatchedES;
  }
  // WebSocket — rewriteStr handles both bare paths and full URLs now.
  if (typeof WebSocket !== 'undefined') {
    var OrigWS = WebSocket;
    function PatchedWS(url, protos) {
      var u = typeof url === 'string' ? rewriteStr(url) : url;
      return protos === undefined ? new OrigWS(u) : new OrigWS(u, protos);
    }
    PatchedWS.prototype = OrigWS.prototype;
    PatchedWS.CONNECTING = OrigWS.CONNECTING;
    PatchedWS.OPEN = OrigWS.OPEN;
    PatchedWS.CLOSING = OrigWS.CLOSING;
    PatchedWS.CLOSED = OrigWS.CLOSED;
    window.WebSocket = PatchedWS;
  }
})();
</script>`

// injectRuntimeRewriter inserts the runtime rewriter script at the top
// of the HTML body. Tries <head> first (most common), falls back to
// prepending to the document if no head tag exists. Case-insensitive
// on the tag match so XHTML-y pages with `<HEAD>` still work.
var headOpenRE = regexp.MustCompile(`(?i)<head[^>]*>`)

func injectRuntimeRewriter(body []byte, proxyBase string) []byte {
	// JSON-encode the base so quoted chars / unicode are safe inside the
	// JS string literal. encoding/json always produces valid JS strings
	// (JSON is a strict subset of JS for strings).
	baseLiteral, err := json.Marshal(proxyBase)
	if err != nil {
		return body // shouldn't happen; leave body alone
	}
	script := []byte(strings.Replace(runtimeRewriterScript, "{{BASE}}", string(baseLiteral), 1))
	loc := headOpenRE.FindIndex(body)
	if loc != nil {
		// Insert right after the <head...> opening tag so our script
		// runs before any of the app's.
		out := make([]byte, 0, len(body)+len(script))
		out = append(out, body[:loc[1]]...)
		out = append(out, script...)
		out = append(out, body[loc[1]:]...)
		return out
	}
	// No <head> — prepend. Browsers are forgiving; a stray <script>
	// before <html> is treated as if it were inside an implicit head.
	return append(script, body...)
}

// rewriteHTMLBody reads the response body fully, rewrites absolute-path
// attributes (src="/foo" → src="<proxyBase>/foo"), injects a runtime
// rewriter script for JS-level calls (fetch / XHR / EventSource /
// WebSocket), and swaps in the new body. Content-Length is updated to
// match the new byte count.
//
// Known limitations still present after this layer:
//   - Bare `import "/foo"` statements in JS modules — the browser's
//     native module loader handles these outside the fetch shim.
//   - Inline `<script>` bodies that build URLs via string concatenation
//     and then do `location.href = built` — they compute before our
//     patched fetch would see anything.
//   - Apps that do `new Request('/foo')` then fetch(req) — the Request
//     object's URL is frozen at construction, and our fetch shim leaves
//     Request args alone to avoid a cloning-and-header-reconstruction
//     rabbit hole.
func rewriteHTMLBody(resp *http.Response, proxyBase string, accessToken string) error {
	body, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		return err
	}
	// Attribute rewrite first (catches static <script src="/…"> etc.)…
	rewritten := absPathAttrRE.ReplaceAllFunc(body, func(match []byte) []byte {
		parts := absPathAttrRE.FindSubmatch(match)
		if len(parts) != 5 {
			return match
		}
		if bytes.HasPrefix(parts[3], []byte(proxyBase)) {
			return match
		}
		rewrittenURL := appendProxyAccessToken(proxyBase+string(parts[3]), accessToken)
		out := make([]byte, 0, len(match)+len(rewrittenURL))
		out = append(out, parts[1]...)
		out = append(out, parts[2]...)
		out = append(out, []byte(rewrittenURL)...)
		out = append(out, parts[4]...)
		return out
	})
	// …then give inline <style> blocks the same CSS url(...) pass a
	// stylesheet response gets — scoped to style elements so a JS
	// `url(...)` call inside an inline script can't be corrupted…
	rewritten = styleBlockRE.ReplaceAllFunc(rewritten, func(match []byte) []byte {
		parts := styleBlockRE.FindSubmatch(match)
		if len(parts) != 4 {
			return match
		}
		out := make([]byte, 0, len(match))
		out = append(out, parts[1]...)
		out = append(out, rewriteCSSBytes(parts[2], proxyBase, accessToken)...)
		out = append(out, parts[3]...)
		return out
	})
	// …then inject the runtime rewriter so the app's JS fetch / XHR /
	// EventSource / WebSocket calls to absolute paths also go through
	// the proxy.
	rewritten = injectRuntimeRewriter(rewritten, proxyBase)
	resp.Body = io.NopCloser(bytes.NewReader(rewritten))
	resp.ContentLength = int64(len(rewritten))
	resp.Header.Set("Content-Length", strconv.Itoa(len(rewritten)))
	return nil
}

// rewriteLocation rewrites an absolute Location header that points at
// localhost:<port> so redirects don't escape the proxy. Returns an
// empty string if the input doesn't need rewriting.
func rewriteLocation(loc, proxiedPort string, origReq *http.Request) string {
	// Fast path: relative paths are fine as-is.
	if !strings.HasPrefix(loc, "http://") && !strings.HasPrefix(loc, "https://") {
		return ""
	}
	u, err := url.Parse(loc)
	if err != nil {
		return ""
	}
	host := u.Hostname()
	if host != "localhost" && host != "127.0.0.1" && host != "::1" {
		return "" // external redirect — let the browser follow it
	}
	port := u.Port()
	if port == "" {
		if u.Scheme == "https" {
			port = "443"
		} else {
			port = "80"
		}
	}
	// Build a same-origin redirect that keeps us inside the proxy.
	// origReq.URL.Path is something like /api/proxy/<proxiedPort>/…
	// We want /api/proxy/<port><u.Path> (possibly different port).
	forwardedPrefix := origReq.Header.Get("X-Forwarded-Prefix")
	proxyBase := forwardedPrefix + "/api/proxy/" + port
	proxiedNumber, _, _ := strings.Cut(proxiedPort, "~")
	if port == proxiedNumber {
		// Common case: redirecting to a different path on the same port.
		proxyBase = forwardedPrefix + "/api/proxy/" + proxiedPort
	}
	if u.Path == "" {
		u.Path = "/"
	}
	newURL := proxyBase + u.Path
	if u.RawQuery != "" {
		newURL += "?" + u.RawQuery
	}
	newURL = appendProxyAccessToken(newURL, proxyAccessToken(origReq))
	if u.Fragment != "" {
		newURL += "#" + u.Fragment
	}
	return newURL
}
