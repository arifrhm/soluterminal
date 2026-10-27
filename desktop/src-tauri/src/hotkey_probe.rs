// Probe whether a system-wide hotkey chord is already owned by another
// process (Windows RegisterHotKey). Soluterminal's own shortcuts are
// plain in-app keydown listeners — they never grab keys globally — so
// an exclusive owner elsewhere silently swallows the chord before our
// webview ever sees a keydown. NVIDIA's and AMD's overlays both claim
// Alt+Z this way (found the hard way on 2026-07-09: an elevated AMD
// overlay process grabbed Alt+Z after a reboot and pane zoom "stopped
// working" with zero visible cause). The web shell probes the pane-zoom
// chord on boot and warns instead of leaving the user with a dead key —
// see useZoomHotkeyWarning.ts for the UI side.
//
// Win32 FFI is direct, matching the daemon.rs `mod job` pattern — no
// external crate needed.

/// True when `combo` is registered as a global hotkey by ANOTHER
/// process. `combo` uses the web keybindings serialization (see
/// keybindings.ts `comboFromEvent`): lowercase, fixed modifier order,
/// single-char key last — e.g. "alt+z", "ctrl+shift+m".
///
/// Registration is attempted and immediately released; failure with
/// ERROR_HOTKEY_ALREADY_REGISTERED (1409) is the "owned" signal.
/// Non-Windows platforms and unparseable combos report false — this is
/// a diagnostic for a warning toast, never a blocker, so false
/// negatives are fine and false positives are not.
#[tauri::command]
pub fn global_hotkey_owned(combo: String) -> bool {
    #[cfg(windows)]
    {
        probe_windows(&combo)
    }
    #[cfg(not(windows))]
    {
        let _ = combo;
        false
    }
}

#[cfg(windows)]
fn probe_windows(combo: &str) -> bool {
    const MOD_ALT: u32 = 0x1;
    const MOD_CONTROL: u32 = 0x2;
    const MOD_SHIFT: u32 = 0x4;
    const MOD_WIN: u32 = 0x8;
    const ERROR_HOTKEY_ALREADY_REGISTERED: i32 = 1409;

    #[link(name = "user32")]
    extern "system" {
        fn RegisterHotKey(hwnd: isize, id: i32, fs_modifiers: u32, vk: u32) -> i32;
        fn UnregisterHotKey(hwnd: isize, id: i32) -> i32;
    }

    let mut mods = 0u32;
    let mut vk = 0u32;
    for part in combo.split('+') {
        match part {
            "ctrl" => mods |= MOD_CONTROL,
            "alt" => mods |= MOD_ALT,
            "shift" => mods |= MOD_SHIFT,
            "meta" => mods |= MOD_WIN,
            key => {
                // Single ASCII letter/digit only — VK codes for 0-9/A-Z
                // equal their uppercase ASCII value. Anything else
                // (arrows, punctuation, "?") isn't a chord overlays
                // fight over; report un-owned rather than guess codes.
                let mut chars = key.chars();
                match (chars.next(), chars.next()) {
                    (Some(c), None) if c.is_ascii_alphanumeric() => {
                        vk = c.to_ascii_uppercase() as u32;
                    }
                    _ => return false,
                }
            }
        }
    }
    // A bare key with no modifier can't be a meaningfully-probed global
    // hotkey (and registering one would briefly eat real typing).
    if vk == 0 || mods == 0 {
        return false;
    }

    // NULL hwnd binds the registration to this thread; register +
    // unregister within the call leaves no residue. The id only needs
    // to not clash with a real registration in THIS process — we make
    // none.
    const PROBE_ID: i32 = 0x5A00;
    unsafe {
        if RegisterHotKey(0, PROBE_ID, mods, vk) != 0 {
            UnregisterHotKey(0, PROBE_ID);
            false
        } else {
            std::io::Error::last_os_error().raw_os_error()
                == Some(ERROR_HOTKEY_ALREADY_REGISTERED)
        }
    }
}
