// Pop-out browser windows. Spawns a separate Tauri WebviewWindow for
// a given URL, giving the user a full Chromium/WebView2 tab inside
// the soluterminal process — unlike our in-pane iframe, this isn't
// blocked by X-Frame-Options / frame-ancestors, so Google/GitHub/
// banks actually load.
//
// Threading: WebviewWindowBuilder::build() must run on the main
// thread. Tauri command handlers run on the tokio runtime by
// default, so calling build() directly from the command creates a
// half-alive window — the HWND appears but the underlying WebView2
// controller is dysfunctional: blank content, no right-click menu,
// close button doesn't fire. Same main-thread-marshal pattern we
// use for browser_pane_create.

use std::sync::atomic::{AtomicU64, Ordering};
use std::sync::mpsc;
use tauri::{AppHandle, Manager, WebviewUrl, WebviewWindowBuilder};

static BROWSER_WINDOW_SEQ: AtomicU64 = AtomicU64::new(1);

#[tauri::command]
pub async fn open_external_window(
    app: AppHandle,
    url: String,
    title: Option<String>,
    label: Option<String>,
    navigate_url: Option<String>,
) -> Result<String, String> {
    // `url` is the TARGET URL the popout's PopoutBrowser should
    // display (and put in its address bar). `navigate_url` is the
    // daemon-served React bundle URL that the popout window's main
    // webview should navigate to. When navigate_url is absent (old
    // JS protocol or direct invocation) we fall back to loading
    // `url` directly — that reproduces the pre-popout-shell behaviour
    // and is a safe degraded mode.
    let webview_url = navigate_url.clone().unwrap_or_else(|| url.clone());
    // The popout shell holds the browser-popout capability, so it may only
    // start on a daemon origin (as its later navigations must, nav_guard.rs).
    if let Some(shell) = &navigate_url {
        let shell_url = shell
            .parse::<tauri::Url>()
            .map_err(|e| format!("bad navigate_url: {e}"))?;
        if let Some(allowed) = app.try_state::<crate::nav_guard::AllowedLocalOrigins>() {
            if !allowed.allows(&shell_url) {
                return Err("navigate_url is not this app's daemon".to_string());
            }
        }
    }
    let parsed = webview_url
        .parse::<tauri::Url>()
        .map_err(|e| format!("bad url: {e}"))?;

    // Label is usually supplied by the JS side so the popout's React
    // app can pass it back to browser_pane_create as parent_label
    // (child webview attaches to this new window, not the main one).
    // Fall back to sequential naming if the caller didn't supply one.
    //
    // Without navigate_url the window shows the target site itself, so it
    // gets a label no capability names (capabilities/browser-popout.json
    // covers `browser_*`, the popout shell): a local page loaded that way has
    // no desktop commands.
    let direct = navigate_url.is_none();
    let label = match label {
        Some(l) if !direct => l,
        _ => {
            let seq = BROWSER_WINDOW_SEQ.fetch_add(1, Ordering::Relaxed);
            if direct {
                format!("directbrowser_{seq}")
            } else {
                format!("browser_{seq}")
            }
        }
    };

    let window_title = title
        .clone()
        .unwrap_or_else(|| format!("Browser — {url}"));

    eprintln!(
        "open_external_window: spawning label={label} target={url} webview={webview_url} (main-thread marshal)"
    );
    #[cfg(windows)]
    crate::daemon::launch_log(&format!(
        "open_external_window ENTER label={label} target={url} webview={webview_url}"
    ));

    // Build the init script on the tokio worker so the main-thread
    // closure only has to carry one ready string. Injecting the popout
    // metadata as a global (read by main.tsx + PopoutBrowser) is
    // stripping-proof: a daemon auth-redirect that eats query params
    // can't touch this.
    //
    // Only inject when navigate_url is supplied — otherwise the
    // caller wanted the degraded "direct-URL" mode and injecting the
    // popout global would turn the loaded page into an unexpected
    // PopoutBrowser render.
    let init_script = if navigate_url.is_some() {
        format!(
            "window.__SOLU_TAURI__ = true; window.__SOLU_POPOUT__ = {{ url: {}, title: {}, parentLabel: {} }};",
            js_string_literal(&url),
            js_string_literal(title.as_deref().unwrap_or("")),
            js_string_literal(&label),
        )
    } else {
        "window.__SOLU_TAURI__ = true;".to_string()
    };

    let (tx, rx) = mpsc::channel();
    let app_clone = app.clone();
    let label_clone = label.clone();
    app.run_on_main_thread(move || {
        let result: Result<(), String> = (|| {
            // Touch app_clone so borrow-checker sees it used in the
            // closure; Manager needs to be in scope for any builder
            // that wants to reach the app handle.
            let _ = app_clone.webviews().len();

            WebviewWindowBuilder::new(&app_clone, &label_clone, WebviewUrl::External(parsed))
                .title(window_title)
                .inner_size(1100.0, 800.0)
                .min_inner_size(400.0, 300.0)
                // Offset from screen corner — spawning at (0,0) on
                // some Windows configurations puts the OS title bar
                // off-screen, so the user sees the client area but no
                // X button to close. Also cascades nicely if the user
                // opens multiple popouts.
                .position(120.0, 80.0)
                .resizable(true)
                .decorations(true)
                // Popout loads our React app (daemon-served) which
                // spawns a native child webview via the same
                // browser_pane_create path as in-pane browsers. Inject
                // two globals BEFORE any page script runs:
                //   __SOLU_TAURI__ — generic Tauri-desktop detector
                //     (same flag the main window sets); without it
                //     the popout would fall back to iframe rendering
                //     and re-introduce the X-Frame-Options bug that
                //     popouts were built to avoid.
                //   __SOLU_POPOUT__ — popout-specific payload the
                //     React entry uses to branch into PopoutBrowser.
                //     Going via init_script instead of URL query
                //     params means a daemon auth-redirect or any
                //     WebView2 quirk can't strip the markers.
                .initialization_script(&init_script)
                // Same WebView2 DnD fix as the main window — otherwise
                // the native handler would swallow in-page drags.
                .disable_drag_drop_handler()
                .devtools(cfg!(debug_assertions))
                .on_page_load(|_wv, payload| {
                    eprintln!(
                        "browser window page_load event={:?} url={}",
                        payload.event(),
                        payload.url()
                    );
                })
                .on_navigation({
                    // The popout shell stays on the daemon origin like the
                    // main window (nav_guard.rs); a direct window may go
                    // anywhere, holding no capability.
                    let guard = crate::nav_guard::guard(app_clone.clone());
                    move |url| {
                        eprintln!("browser window on_navigation url={url}");
                        direct || guard(url)
                    }
                })
                .build()
                .map(|_| ())
                .map_err(|e| format!("failed to open browser window: {e}"))
        })();
        let _ = tx.send(result);
    })
    .map_err(|e| format!("run_on_main_thread failed: {e}"))?;

    tauri::async_runtime::spawn_blocking(move || {
        rx.recv().map_err(|e| format!("recv failed: {e}"))?
    })
    .await
    .map_err(|e| format!("spawn_blocking join failed: {e}"))??;

    eprintln!("open_external_window: OK label={label}");
    #[cfg(windows)]
    crate::daemon::launch_log(&format!("open_external_window OK label={label}"));

    Ok(label)
}

/// Serialise a Rust string into a JavaScript string literal (including
/// the surrounding quotes), via serde_json. Safe to concatenate into
/// an `.initialization_script(...)` body even if the input contains
/// quotes, backslashes, Unicode line separators, or control chars —
/// serde_json handles all JSON/JS-compatible escaping.
fn js_string_literal(s: &str) -> String {
    serde_json::to_string(s).unwrap_or_else(|_| "\"\"".into())
}
