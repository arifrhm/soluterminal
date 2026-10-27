// Drops ~/.solu/shell-info.json on every app launch so the Go daemon
// can discover the installed Tauri-shell version even when it wasn't
// spawned by Tauri (systemd-managed solud, manual `solud` runs, etc.).
// Explicit --shell-version / --shell-platform CLI flags still win — the
// file is purely a fallback for the systemd path.

#[cfg(windows)]
use std::os::windows::process::CommandExt;
#[cfg(windows)]
use std::process::{Command, Stdio};

#[cfg(windows)]
const CREATE_NO_WINDOW: u32 = 0x08000000;

/// Write `~/.solu/shell-info.json` on the host (non-WSL) using the
/// host's own home dir. Errors are swallowed with an `eprintln!` so a
/// failed write never blocks app startup — the CLI-flag path still
/// works for Tauri-direct launches.
pub fn write_host(version: &str, platform: &str) {
    let Some(mut path) = dirs::home_dir() else {
        eprintln!("desktop: shell-info: no home dir; skipping");
        return;
    };
    path.push(".solu");
    if let Err(e) = std::fs::create_dir_all(&path) {
        eprintln!("desktop: shell-info: mkdir {}: {e}", path.display());
        return;
    }
    path.push("shell-info.json");
    let body = format!(
        "{{\"version\":\"{}\",\"platform\":\"{}\"}}\n",
        json_escape(version),
        json_escape(platform),
    );
    if let Err(e) = std::fs::write(&path, body.as_bytes()) {
        eprintln!("desktop: shell-info: write {}: {e}", path.display());
    }
}

/// Write `~/.solu/shell-info.json` *inside* WSL via `wsl.exe`. The
/// daemon under WSL reads its own `/home/<user>/.solu/shell-info.json`,
/// not the Windows host's, so we need to land the file on the Linux
/// side. Uses a `cat <<EOF` heredoc the same way the daemon-spawn
/// script does to dodge cross-shell quoting issues.
#[cfg(windows)]
pub fn write_wsl(version: &str, platform: &str) {
    // This best-effort metadata write runs before backend selection. It must
    // not wake a stopped distribution before the launcher's disk admission.
    if !crate::daemon::wsl_start_allowed() {
        return;
    }
    let script = format!(
        "mkdir -p \"$HOME/.solu\" && cat > \"$HOME/.solu/shell-info.json\" <<'EOF'\n{{\"version\":\"{}\",\"platform\":\"{}\"}}\nEOF\n",
        json_escape(version),
        json_escape(platform),
    );
    let mut cmd = Command::new("wsl.exe");
    cmd.args(["-e", "bash", "-c", &script])
        .stdout(Stdio::null())
        .stderr(Stdio::null())
        .creation_flags(CREATE_NO_WINDOW);
    match cmd.status() {
        Ok(s) if s.success() => {}
        Ok(s) => eprintln!("desktop: shell-info: wsl.exe exit {s}"),
        Err(e) => eprintln!("desktop: shell-info: wsl.exe spawn: {e}"),
    }
}

#[cfg(not(windows))]
#[allow(dead_code)]
pub fn write_wsl(_version: &str, _platform: &str) {
    // No WSL on non-Windows hosts.
}

fn json_escape(s: &str) -> String {
    let mut out = String::with_capacity(s.len());
    for c in s.chars() {
        match c {
            '"' => out.push_str("\\\""),
            '\\' => out.push_str("\\\\"),
            '\n' => out.push_str("\\n"),
            '\r' => out.push_str("\\r"),
            '\t' => out.push_str("\\t"),
            c if (c as u32) < 0x20 => out.push_str(&format!("\\u{:04x}", c as u32)),
            c => out.push(c),
        }
    }
    out
}
