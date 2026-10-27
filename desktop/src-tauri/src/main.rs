// soluterminal desktop — minimal Tauri 2 wrapper.
//
// The window loads http://127.0.0.1:8787 (the local daemon's UI) at
// startup. For v0 the daemon must be running separately (via systemd,
// or `./solud` in a shell). v0.2 will spawn it as a sidecar so the
// desktop app self-boots the daemon when launched.

// Prevents additional console window on Windows in release, DO NOT REMOVE!!
#![cfg_attr(not(debug_assertions), windows_subsystem = "windows")]

fn main() {
    soluterminal_desktop_lib::run()
}
