// Multi-monitor support (Phase 1a): spawn a SECOND app window that runs
// the full soluterminal frontend, so the user can drag it to another
// monitor and work a different workspace there.
//
// Unlike the pop-out BROWSER windows (browser.rs), a workspace window
// loads the SAME daemon URL the main window loaded — with the same
// backend/token composition (WSL token, dual-mode solu_primary/solu_alt,
// router param). We don't re-derive that here: lib.rs already composed
// the final `url_string` + `__SOLU_BACKEND__` script for the main window,
// so it stashes both in a read-only `WorkspaceSpawnTemplate` managed-state
// value that this command reads back. That keeps the fragile launch-mode
// resolution in exactly one place.
//
// Each window also gets a durable per-window identity injected before any
// page script runs:  window.__SOLU_WINDOW__ = { label, role }.  The
// frontend keys its per-window storage (active workspace) off this label
// so an auto-update reload restores THIS window's workspace, not a shared
// default. The label is the stable Tauri window label, which survives the
// frontend's cache-busting `_v` reload.
//
// Threading: WebviewWindowBuilder::build() MUST run on the main thread —
// the same hard-won constraint documented in browser.rs. Building off the
// tokio worker yields a half-alive WebView2 controller (blank content, no
// menu, close button dead).

use std::sync::atomic::{AtomicU64, Ordering};
use std::sync::mpsc;
use tauri::{AppHandle, Manager, PhysicalPosition, WebviewUrl, WebviewWindowBuilder};

// Secondary windows are labelled workspace_2, workspace_3, … The main
// window is conceptually "window 1", so the first spawned one is 2.
static WORKSPACE_WINDOW_SEQ: AtomicU64 = AtomicU64::new(2);

/// Read-only template captured by lib.rs after it composes the main
/// window's URL + backend signal. Managed-state, keyed by type, never
/// mutated — so no Mutex.
pub struct WorkspaceSpawnTemplate {
    /// The exact URL the main window loaded (token + dual-mode params).
    pub url_string: String,
    /// `window.__SOLU_BACKEND__ = { … };` init script, verbatim.
    pub backend_script: String,
    /// false in Cloud launch mode, where the second window would need a
    /// per-machine prefix + JWT we don't compose for MVP. The frontend
    /// also hides the "New Window" affordance in cloud mode; this is the
    /// defense-in-depth backstop.
    pub supported: bool,
}

/// Serialise a string into a JS string literal (with surrounding quotes)
/// for safe embedding in an initialization script. serde_json handles all
/// JS-compatible escaping; falls back to an empty string literal.
fn js_string_literal(s: &str) -> String {
    serde_json::to_string(s).unwrap_or_else(|_| "\"\"".to_string())
}

/// Spawn a new full-app window (workspace_<n>) reusing the main window's
/// daemon URL + backend composition. Returns the new window's label.
#[tauri::command]
pub async fn open_workspace_window(
    app: AppHandle,
    initial_view: Option<String>,
) -> Result<String, String> {
    if initial_view.is_some() {
        return Err("initial_view is no longer supported".to_string());
    }
    // Pull the captured template. Extract owned Strings up-front so the
    // main-thread closure captures only Send data (not the State guard).
    let (url_string, backend_script, supported) = {
        let template = app.state::<WorkspaceSpawnTemplate>();
        (
            template.url_string.clone(),
            template.backend_script.clone(),
            template.supported,
        )
    };

    if !supported {
        return Err("multi-window is not available in cloud mode".to_string());
    }

    let parsed = url_string
        .parse::<tauri::Url>()
        .map_err(|e| format!("bad url: {e}"))?;

    let seq = WORKSPACE_WINDOW_SEQ.fetch_add(1, Ordering::Relaxed);
    let label = format!("workspace_{seq}");

    // Per-window identity, injected before any page script (same
    // mechanism as __SOLU_TAURI__). serde_json output already includes
    // the surrounding quotes, so it's emitted verbatim.
    let window_script = format!(
        "window.__SOLU_WINDOW__ = {{ label: {}, role: \"secondary\" }};",
        js_string_literal(&label)
    );

    // Single-monitor (or undeterminable) fallback: cascade from the corner,
    // staggered so stacked windows don't perfectly overlap. On a multi-monitor
    // setup the closure below overrides this with physical placement centered
    // on the NEXT monitor. 120px keeps the OS titlebar on-screen.
    let offset = 120.0 + ((seq.saturating_sub(2)) as f64) * 40.0;

    eprintln!(
        "open_workspace_window: spawning label={label} url={url_string} (main-thread marshal)"
    );
    #[cfg(windows)]
    crate::daemon::launch_log(&format!(
        "open_workspace_window ENTER label={label} url={url_string}"
    ));

    let (tx, rx) = mpsc::channel();
    let app_clone = app.clone();
    let label_clone = label.clone();
    app.run_on_main_thread(move || {
        let result: Result<(), String> = (|| {
            // Monitor-aware placement. These monitor queries read main-thread-
            // only event-loop state; we're already on the main thread here
            // (run_on_main_thread), so they read it directly without
            // dispatching back to the loop — no deadlock.
            //
            // Identify the main window's monitor by POSITION (a monitor's
            // top-left is unique on the virtual desktop; name() can be None or
            // duplicated across identical displays), then pick the NEXT monitor
            // ordered by (x, y) with wrap — so the new window lands on a screen
            // the user can see without dragging it there first.
            let cur_pos = app_clone
                .get_webview_window("main")
                .and_then(|w| w.current_monitor().ok().flatten())
                .map(|m| *m.position());
            let monitors = app_clone.available_monitors().unwrap_or_default();

            let target = if monitors.len() >= 2 {
                cur_pos.and_then(|cp| {
                    let mut order: Vec<usize> = (0..monitors.len()).collect();
                    order.sort_by_key(|&i| {
                        let p = monitors[i].position();
                        (p.x, p.y)
                    });
                    order
                        .iter()
                        .position(|&i| {
                            let p = monitors[i].position();
                            p.x == cp.x && p.y == cp.y
                        })
                        .map(|k| order[(k + 1) % order.len()])
                })
            } else {
                None
            };

            // Physical top-left, centered in the target monitor's work area
            // (which excludes the taskbar), plus the per-window stagger.
            // inner_size is LOGICAL (1280x800); on a monitor with scale S the
            // window settles at 1280*S x 800*S physical. decorations(false)
            // means outer == inner, so centering the outer rect with the inner
            // dimensions is exact.
            let target_pos = target.map(|i| {
                let m = &monitors[i];
                let s = m.scale_factor();
                let wa = m.work_area();
                let phys_w = (1280.0 * s).round() as i32;
                let phys_h = (800.0 * s).round() as i32;
                let stagger = (seq.saturating_sub(2) as f64 * 40.0 * s).round() as i32;
                let x = wa.position.x + ((wa.size.width as i32 - phys_w) / 2).max(0) + stagger;
                let y = wa.position.y + ((wa.size.height as i32 - phys_h) / 2).max(0) + stagger;
                (x, y)
            });

            let title = "soluterminal";
            let mut builder =
                WebviewWindowBuilder::new(&app_clone, &label_clone, WebviewUrl::External(parsed))
                    .title(title)
                    .inner_size(1280.0, 800.0)
                    .min_inner_size(600.0, 400.0)
                    .resizable(true)
                    // Match the main window: the custom titlebar lives in the
                    // React app (Topbar.tsx), so no OS decorations.
                    .decorations(false)
                    // Inject the same two globals the main window sets, then
                    // the per-window identity. Separate scripts, each runs
                    // before page code — mirrors lib.rs's main builder.
                    .initialization_script("window.__SOLU_TAURI__ = true;")
                    .initialization_script(&backend_script)
                    .initialization_script(&window_script)
                    .disable_drag_drop_handler()
                    // Same daemon-origin restriction as the main window.
                    .on_navigation(crate::nav_guard::guard(app_clone.clone()))
                    .devtools(cfg!(debug_assertions));

            // Cross-monitor placement needs PHYSICAL coords, but the builder's
            // .position() is LOGICAL (resolved against the SOURCE monitor's
            // scale → wrong under mixed DPI). So build HIDDEN, set the physical
            // position, then show — which also avoids a flash at the cascade
            // origin. Single-monitor falls back to the logical cascade.
            builder = match target_pos {
                Some(_) => builder.visible(false),
                None => builder.position(offset, offset),
            };

            let window = builder
                .build()
                .map_err(|e| format!("failed to open workspace window: {e}"))?;

            if let Some((x, y)) = target_pos {
                window
                    .set_position(PhysicalPosition::new(x, y))
                    .map_err(|e| format!("set_position failed: {e}"))?;
                window.show().map_err(|e| format!("show failed: {e}"))?;
            }
            Ok(())
        })();
        let _ = tx.send(result);
    })
    .map_err(|e| format!("run_on_main_thread failed: {e}"))?;

    tauri::async_runtime::spawn_blocking(move || {
        rx.recv().map_err(|e| format!("recv failed: {e}"))?
    })
    .await
    .map_err(|e| format!("spawn_blocking join failed: {e}"))??;

    eprintln!("open_workspace_window: OK label={label}");
    #[cfg(windows)]
    crate::daemon::launch_log(&format!("open_workspace_window OK label={label}"));

    Ok(label)
}
