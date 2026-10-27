// Tray icon pixels.
//
// Windows draws notification-area icons at the system small-icon size (16 px
// at 100 % scaling, 20 at 125 %, 24 at 150 %, 32 at 200 %) and resamples
// whatever image it is handed. The default window icon is a single fixed
// size, so on most scalings the tray showed a blurred resample. The mark is
// hand-tuned at these sizes by icon-source/generate.js; hand Windows the one
// that fits the system's small-icon size.

#[cfg(any(windows, test))]
const DRAWN: [(u32, &[u8]); 6] = [
    (16, include_bytes!("../icons/tray/tray-16.rgba")),
    (20, include_bytes!("../icons/tray/tray-20.rgba")),
    (24, include_bytes!("../icons/tray/tray-24.rgba")),
    (32, include_bytes!("../icons/tray/tray-32.rgba")),
    (40, include_bytes!("../icons/tray/tray-40.rgba")),
    (48, include_bytes!("../icons/tray/tray-48.rgba")),
];

/// The smallest drawn size that covers `px`, or the largest one: shrinking
/// a little stays sharper than enlarging.
#[cfg(any(windows, test))]
fn pick(px: u32) -> (u32, &'static [u8]) {
    DRAWN
        .iter()
        .copied()
        .find(|(size, _)| *size >= px)
        .unwrap_or(DRAWN[DRAWN.len() - 1])
}

/// The tray image for this platform; `None` keeps the default window icon.
#[cfg(windows)]
pub fn image() -> Option<tauri::image::Image<'static>> {
    #[link(name = "user32")]
    extern "system" {
        fn GetSystemMetrics(index: i32) -> i32;
    }
    const SM_CXSMICON: i32 = 49;
    // The shell is per-monitor DPI aware, so this is the small-icon size at
    // the system DPI, the one the taskbar's notification area uses.
    let px = unsafe { GetSystemMetrics(SM_CXSMICON) }.max(1) as u32;
    let (size, rgba) = pick(px);
    Some(tauri::image::Image::new(rgba, size, size))
}

#[cfg(not(windows))]
pub fn image() -> Option<tauri::image::Image<'static>> {
    None
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn every_drawn_size_is_square_rgba() {
        for (size, rgba) in DRAWN {
            assert_eq!(rgba.len(), (size * size * 4) as usize, "tray-{size}.rgba");
        }
    }

    #[test]
    fn picks_the_size_windows_asks_for() {
        for (px, want) in [(16, 16), (20, 20), (24, 24), (32, 32), (40, 40), (48, 48)] {
            assert_eq!(pick(px).0, want);
        }
    }

    #[test]
    fn odd_sizes_take_the_next_size_up() {
        assert_eq!(pick(1).0, 16);
        assert_eq!(pick(18).0, 20);
        assert_eq!(pick(28).0, 32);
    }

    #[test]
    fn beyond_the_largest_uses_the_largest() {
        assert_eq!(pick(64).0, 48);
    }

    // An upgrade keeps the exe path, so without this the shell's icon cache
    // can keep showing the previous build's icon.
    #[test]
    fn installer_tells_the_shell_icons_changed() {
        let hooks = include_str!("../installer/hooks.nsh");
        let postinstall = hooks
            .split("!macro NSIS_HOOK_POSTINSTALL")
            .nth(1)
            .and_then(|tail| tail.split("!macroend").next())
            .expect("postinstall hook");
        assert!(postinstall.contains("shell32::SHChangeNotify(i 0x08000000, i 0x1000"));
    }
}
