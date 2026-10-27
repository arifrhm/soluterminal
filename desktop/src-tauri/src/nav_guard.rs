// Navigation guard for the app's own windows (main, workspace_*).
//
// Their capabilities (capabilities/*.json) grant the desktop commands to any
// page whose origin matches `remote.urls`, which has to include
// `http://127.0.0.1:*` because the daemon's port isn't fixed. So if one of
// these windows were navigated to some other local server — a dev server a
// pane started, or a page a dependency serves — that page would get those
// commands. The windows only ever need the daemon URLs they were opened with
// (two in dual mode), so a navigation to any other loopback origin is refused
// here and opened in the system browser instead. Other origins (the cloud,
// OAuth sign-in pages) are unaffected: they match no local capability.
//
// On Windows (WebView2, the only shipped desktop target) the handler sees
// top-level navigations only. WebKitGTK and WKWebView also report iframe
// navigations, so a desktop build there would refuse a local iframe too; the
// app uses iframes only in its cloud/PWA fallback, not inside the shell.

use tauri::{AppHandle, Manager, Runtime, Url};
use tauri_plugin_opener::OpenerExt;

/// The daemon origins these windows were opened with.
pub struct AllowedLocalOrigins(pub Vec<String>);

impl AllowedLocalOrigins {
    pub fn from_urls<'a>(urls: impl IntoIterator<Item = &'a str>) -> Self {
        Self(
            urls.into_iter()
                .filter_map(|u| Url::parse(u).ok())
                .filter(is_loopback)
                .map(|u| origin_of(&u))
                .collect(),
        )
    }

    /// Whether a window may load `target`.
    pub fn allows(&self, target: &Url) -> bool {
        !is_loopback(target) || self.0.contains(&origin_of(target))
    }
}

/// The `on_navigation` handler for an app window: refused loopback targets
/// open in the system browser instead (a link to a dev server still works).
pub fn guard<R: Runtime>(app: AppHandle<R>) -> impl Fn(&Url) -> bool + Send + 'static {
    move |target: &Url| {
        let allowed = app
            .try_state::<AllowedLocalOrigins>()
            .map(|a| a.allows(target))
            .unwrap_or(!is_loopback(target));
        if !allowed {
            eprintln!(
                "nav_guard: refused in-app navigation to {}; opening it in the browser",
                origin_of(target)
            );
            let _ = app.opener().open_url(target.as_str(), None::<&str>);
        }
        allowed
    }
}

fn is_loopback(url: &Url) -> bool {
    let Some(host) = url.host_str() else {
        return false;
    };
    let host = host
        .trim_start_matches('[')
        .trim_end_matches(']')
        .to_ascii_lowercase();
    match host.parse::<std::net::IpAddr>() {
        Ok(ip) => ip.is_loopback() || ip.is_unspecified(),
        Err(_) => host == "localhost" || host.ends_with(".localhost"),
    }
}

fn origin_of(url: &Url) -> String {
    url.origin().ascii_serialization()
}

#[cfg(test)]
mod tests {
    use super::*;

    fn u(s: &str) -> Url {
        Url::parse(s).unwrap()
    }

    #[test]
    fn only_the_daemon_origins_are_reachable_on_loopback() {
        let allowed = AllowedLocalOrigins::from_urls([
            "http://127.0.0.1:8787/?token=abc",
            "http://127.0.0.1:8790/",
        ]);
        assert!(allowed.allows(&u("http://127.0.0.1:8787/workspace#x")));
        assert!(allowed.allows(&u("http://127.0.0.1:8790/")));
        for other in [
            "http://127.0.0.1:3000/",
            "http://localhost:8787/", // same port, different origin
            "http://[::1]:8787/",
            "http://0.0.0.0:8787/",
            "http://app.localhost:5173/",
            "https://127.0.0.1:8787/",
        ] {
            assert!(!allowed.allows(&u(other)), "{other} should be refused");
        }
    }

    #[test]
    fn non_local_origins_are_left_alone() {
        let allowed = AllowedLocalOrigins::from_urls(["http://127.0.0.1:8787/"]);
        for ok in [
            "https://cloud.example.com/",
            "https://accounts.google.com/o/oauth2/auth",
            "https://abc.supabase.co/auth/v1/callback",
            "about:blank",
        ] {
            assert!(allowed.allows(&u(ok)), "{ok} should be allowed");
        }
    }

    #[test]
    fn a_cloud_start_url_allows_no_local_origin() {
        let allowed = AllowedLocalOrigins::from_urls(["https://cloud.example.com/"]);
        assert!(allowed.0.is_empty());
        assert!(!allowed.allows(&u("http://127.0.0.1:8787/")));
    }
}
