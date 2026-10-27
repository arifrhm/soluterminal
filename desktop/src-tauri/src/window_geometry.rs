// Persist + restore the MAIN window's geometry across launches (multi-monitor
// Phase 1b). The window reopens at its last position, size, and maximized
// state instead of always 1280x800 at the OS default — including on the
// monitor the user left it on. If the saved geometry is off-screen (a monitor
// was unplugged), we fall back to the default rather than strand the window.
//
// This is the geometry-persistence mechanism a later slice reuses for
// secondary-window session restore.

use serde::{Deserialize, Serialize};
use std::fs;
use std::path::PathBuf;
use tauri::{Monitor, WebviewWindow};

/// x/y = PHYSICAL outer position on the virtual desktop (unambiguous across
/// monitors); width/height = LOGICAL inner size (DPI-portable — the same
/// logical size restores to the right physical size on any monitor's scale).
#[derive(Serialize, Deserialize, Clone, Copy)]
pub struct WindowGeometry {
    pub x: i32,
    pub y: i32,
    pub width: u32,
    pub height: u32,
    pub maximized: bool,
}

/// Mirrors launch_mode's pref location: %LOCALAPPDATA%\soluterminal on Windows,
/// the platform config dir elsewhere.
fn path() -> Option<PathBuf> {
    #[cfg(windows)]
    {
        let mut p = PathBuf::from(std::env::var_os("LOCALAPPDATA")?);
        p.push("soluterminal");
        p.push("window-main.json");
        Some(p)
    }
    #[cfg(not(windows))]
    {
        let mut p = dirs::config_dir()?;
        p.push("soluterminal");
        p.push("window-main.json");
        Some(p)
    }
}

pub fn load() -> Option<WindowGeometry> {
    serde_json::from_str(&fs::read_to_string(path()?).ok()?).ok()
}

pub fn save(g: &WindowGeometry) {
    let Some(p) = path() else { return };
    if let Some(dir) = p.parent() {
        let _ = fs::create_dir_all(dir);
    }
    if let Ok(s) = serde_json::to_string(g) {
        let _ = fs::write(&p, s);
    }
}

/// Capture the window's current geometry: PHYSICAL outer position + LOGICAL
/// inner size (physical / scale). Returns None on any query error, or when the
/// window is minimized (Windows reports -32000 positions there) so we never
/// persist garbage.
pub fn capture(w: &WebviewWindow) -> Option<WindowGeometry> {
    if w.is_minimized().unwrap_or(false) {
        return None;
    }
    let pos = w.outer_position().ok()?;
    let size = w.inner_size().ok()?;
    let scale = w.scale_factor().ok()?;
    Some(WindowGeometry {
        x: pos.x,
        y: pos.y,
        width: (size.width as f64 / scale).round() as u32,
        height: (size.height as f64 / scale).round() as u32,
        maximized: w.is_maximized().unwrap_or(false),
    })
}

/// Reject geometry whose monitor is gone: require at least a MIN_VISIBLE-square
/// physical block of the saved rect to overlap some live monitor's work area.
/// Normal windows overlap fully; a slightly-off window keeps well over the
/// threshold on-screen; an unplugged-monitor rect overlaps nothing → false →
/// caller falls back to the default. An empty monitor list → false.
pub fn visible_on_monitors(g: &WindowGeometry, monitors: &[Monitor]) -> bool {
    const MIN_VISIBLE: i32 = 100; // physical px
    monitors.iter().any(|m| {
        let s = m.scale_factor();
        let wa = m.work_area();
        let wx0 = g.x;
        let wy0 = g.y;
        let wx1 = g.x + (g.width as f64 * s).round() as i32;
        let wy1 = g.y + (g.height as f64 * s).round() as i32;
        let mx0 = wa.position.x;
        let my0 = wa.position.y;
        let mx1 = mx0 + wa.size.width as i32;
        let my1 = my0 + wa.size.height as i32;
        let ix = wx1.min(mx1) - wx0.max(mx0);
        let iy = wy1.min(my1) - wy0.max(my0);
        ix >= MIN_VISIBLE && iy >= MIN_VISIBLE
    })
}
