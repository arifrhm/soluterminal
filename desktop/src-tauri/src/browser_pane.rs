// In-pane native browser. Creates a child webview INSIDE the main
// window, positioned to overlay the React BrowserPane tile. React
// measures the pane body's bounding rect each animation frame and,
// on change, calls browser_pane_set_bounds so the native webview
// tracks pane resizes, splits, and sidebar toggles.
//
// Tauri 2 gates Window::add_child behind the `unstable` feature —
// enabled in Cargo.toml. API may change in minor versions; re-test
// after every tauri bump.
//
// Threading notes:
// - All commands are `async` so Tauri runs them on the tokio runtime
//   (worker), NEVER on the main thread. A sync command may end up on
//   the main thread under some dispatch paths, and if it then blocks
//   on a channel waiting for a main-thread closure, deadlock.
// - Webview ops (add_child, set_position, set_size, navigate, close,
//   show, hide) MUST run on the main thread. We marshal via
//   `run_on_main_thread` + tokio oneshot.
// - `browser_pane_ping` exists as a sentinel: JS invokes it at mount
//   to prove which Tauri shell version is actually running. If ping
//   resolves with the expected version, the shell has these commands
//   AND command dispatch is working. If it's missing or hangs, the
//   user is on an older MSI.
//
// Z-order (Windows): the child webview is a native HWND that renders
// above React DOM. Acceptable for the pane body; React modals over a
// visible browser pane would appear behind. Known limitation.

use std::sync::mpsc;
use tauri::{AppHandle, LogicalPosition, LogicalSize, Manager, WebviewBuilder, WebviewUrl};

fn label_for(id: &str) -> String {
    format!("bpane_{id}")
}

fn log(line: &str) {
    eprintln!("{line}");
    #[cfg(windows)]
    crate::daemon::launch_log(line);
}

// Run `f` on the main thread and await completion. Async version
// uses a std::sync::mpsc channel — blocks the tokio worker (fine,
// there are many) but NOT the main thread, so main thread is free
// to run the queued closure.
async fn on_main<T, F>(app: &AppHandle, f: F) -> Result<T, String>
where
    T: Send + 'static,
    F: FnOnce(AppHandle) -> Result<T, String> + Send + 'static,
{
    let (tx, rx) = mpsc::channel();
    let handle = app.clone();
    app.run_on_main_thread(move || {
        let _ = tx.send(f(handle));
    })
    .map_err(|e| format!("run_on_main_thread failed: {e}"))?;
    tauri::async_runtime::spawn_blocking(move || {
        rx.recv().map_err(|e| format!("recv failed: {e}"))?
    })
    .await
    .map_err(|e| format!("spawn_blocking join failed: {e}"))?
}

#[tauri::command]
pub async fn browser_pane_ping(app: AppHandle) -> Result<String, String> {
    // Return the app's real version (from tauri.conf.json, which
    // matches the NSIS installer version) — not CARGO_PKG_VERSION
    // which stays pinned at 0.0.1 for the lib crate.
    Ok(app.package_info().version.to_string())
}

#[tauri::command]
pub async fn browser_pane_create(
    app: AppHandle,
    id: String,
    url: String,
    x: f64,
    y: f64,
    w: f64,
    h: f64,
    parent_label: Option<String>,
) -> Result<(), String> {
    let t0 = std::time::Instant::now();
    // parent_label lets popout browser windows host their own in-pane
    // child webview: when the React app is running inside a popout, it
    // passes the popout window's label instead of the default "main".
    let parent_label = parent_label.unwrap_or_else(|| "main".to_string());
    log(&format!(
        "browser_pane_create ENTER label=bpane_{id} parent={parent_label} url={url} pos=({x},{y}) size=({w},{h})"
    ));
    let parsed = url
        .parse::<tauri::Url>()
        .map_err(|e| format!("bad url: {e}"))?;
    log(&format!("browser_pane_create parsed_url label=bpane_{id}"));

    let label = label_for(&id);
    let label_for_closure = label.clone();
    let parent_label_for_closure = parent_label.clone();
    on_main(&app, move |handle| {
        log(&format!("browser_pane_create on_main_enter label={label_for_closure}"));
        if let Some(existing) = handle.get_webview(&label_for_closure) {
            log(&format!("browser_pane_create closing_existing label={label_for_closure}"));
            let _ = existing.close();
        }
        let parent = handle
            .get_window(&parent_label_for_closure)
            .ok_or_else(|| format!("parent window '{parent_label_for_closure}' missing"))?;
        log(&format!("browser_pane_create got_parent label={label_for_closure}"));
        // Tell cooperating sites we prefer dark mode. Sites that read
        // `color-scheme` (default UA styles for scrollbars / form
        // controls / initial bg) pick it up immediately; sites that
        // branch on `prefers-color-scheme` get a light fake so
        // `matchMedia('(prefers-color-scheme: dark)')` resolves true.
        // Non-cooperating sites stay as-is — same as Chrome with the
        // OS set to dark mode. A proper darkreader-style CSS rewrite
        // is a follow-up if this isn't enough.
        let dark_init = r#"
(function () {
  try {
    var s = document.createElement('style');
    s.textContent = ':root { color-scheme: dark; }';
    (document.head || document.documentElement).appendChild(s);
  } catch (e) {}
  try {
    var mm = window.matchMedia;
    window.matchMedia = function (q) {
      var r = mm.call(window, q);
      if (typeof q === 'string' && q.indexOf('prefers-color-scheme: dark') !== -1) {
        return { matches: true, media: q, onchange: null,
          addListener: function () {}, removeListener: function () {},
          addEventListener: function () {}, removeEventListener: function () {},
          dispatchEvent: function () { return false; } };
      }
      return r;
    };
  } catch (e) {}
})();
"#;
        let builder = WebviewBuilder::new(&label_for_closure, WebviewUrl::External(parsed))
            .initialization_script(dark_init)
            // Don't let the child webview grab keyboard focus on creation. It's a
            // separate OS window, so once it has focus it swallows app chords —
            // notably Alt+B, which then can't toggle the browser closed. Keeping
            // focus on the main window means Alt+B still works right after
            // opening; the user clicking into the page focuses it for scrolling
            // as usual (then the top-right globe button / pane × still close it).
            .focused(false);
        log(&format!("browser_pane_create calling_add_child label={label_for_closure}"));
        parent
            .add_child(
                builder,
                LogicalPosition::new(x, y),
                LogicalSize::new(w, h),
            )
            .map_err(|e| format!("add_child failed: {e}"))?;
        log(&format!("browser_pane_create add_child_returned label={label_for_closure}"));
        Ok(())
    })
    .await?;

    log(&format!(
        "browser_pane_create OK label={label} elapsed_ms={}",
        t0.elapsed().as_millis()
    ));
    Ok(())
}

#[tauri::command]
pub async fn browser_pane_set_bounds(
    app: AppHandle,
    id: String,
    x: f64,
    y: f64,
    w: f64,
    h: f64,
) -> Result<(), String> {
    let label = label_for(&id);
    on_main(&app, move |handle| {
        let wv = handle.get_webview(&label).ok_or("webview not found")?;
        wv.set_position(LogicalPosition::new(x, y))
            .map_err(|e| e.to_string())?;
        wv.set_size(LogicalSize::new(w, h)).map_err(|e| e.to_string())?;
        Ok(())
    })
    .await
}

#[tauri::command]
pub async fn browser_pane_navigate(
    app: AppHandle,
    id: String,
    url: String,
) -> Result<(), String> {
    let parsed = url
        .parse::<tauri::Url>()
        .map_err(|e| format!("bad url: {e}"))?;
    let label = label_for(&id);
    on_main(&app, move |handle| {
        let wv = handle.get_webview(&label).ok_or("webview not found")?;
        wv.navigate(parsed).map_err(|e| e.to_string())?;
        Ok(())
    })
    .await
}

#[tauri::command]
pub async fn browser_pane_close(app: AppHandle, id: String) -> Result<(), String> {
    let label = label_for(&id);
    let t0 = std::time::Instant::now();
    log(&format!("browser_pane_close ENTER label={label}"));
    on_main(&app, move |handle| {
        if let Some(wv) = handle.get_webview(&label) {
            wv.close().map_err(|e| e.to_string())?;
        }
        Ok(())
    })
    .await?;
    log(&format!(
        "browser_pane_close OK label={} elapsed_ms={}",
        label_for(&id),
        t0.elapsed().as_millis()
    ));
    Ok(())
}

#[tauri::command]
pub async fn browser_pane_set_visible(
    app: AppHandle,
    id: String,
    visible: bool,
) -> Result<(), String> {
    let label = label_for(&id);
    on_main(&app, move |handle| {
        let wv = handle.get_webview(&label).ok_or("webview not found")?;
        if visible {
            wv.show().map_err(|e| e.to_string())?;
        } else {
            wv.hide().map_err(|e| e.to_string())?;
        }
        Ok(())
    })
    .await
}
