// Minimal native pairing wizard — now a FALLBACK only.
//
// Pairing primarily happens inside the embedded web UI (the
// "Set up phone access" banner + pair panel driven by live cloud
// status). This native dialog flow is NO LONGER auto-fired on first
// launch and the tray "Pair with cloud…" item now just focuses the
// window so the web banner can take over. These functions remain as a
// last-resort fallback (e.g. headless / web-unavailable scenarios) and
// can still be wired back to a menu item if needed.
//
// Two flavors depending on the user's selected backend:
//
//   Backend::HostNative (or non-Windows host)
//     → Run `solud --claim --relay <cloud>` as a native subprocess.
//       Claim lands at %USERPROFILE%\.solu\claim.json (Windows) or
//       ~/.solu/claim.json (Linux/macOS), which the host-native daemon
//       reads on next spawn.
//
//   Backend::Wsl or Backend::Dual (Windows only)
//     → Run the claim inside WSL via `wsl.exe`, so claim.json lands
//       at /home/<wsluser>/.solu/claim.json — that's what the WSL
//       daemon reads on next spawn. After success we also copy the
//       claim bytes to the Windows side so if the user later
//       switches backend to HostNative the pairing survives.
//
// Flow (both flavors):
//   1. Check if already paired — if so, bail without clobbering.
//   2. Spawn solud --claim, read the 6-char code from its stdout.
//   3. Show a native dialog with the code + a button that opens the
//      cloud URL in the browser. User signs in and enters the code.
//   4. Poll for the claim file to appear. On success, just confirm —
//      NO restart. The daemon's relay supervisor re-reads claim.json on
//      a poll and dials the cloud live within a few seconds on every OS.

use std::io::{BufRead, BufReader};
use std::path::PathBuf;
use std::process::{Command, Stdio};
use std::thread;
use std::time::{Duration, Instant};

use tauri::AppHandle;
use tauri_plugin_dialog::{DialogExt, MessageDialogKind};
use tauri_plugin_opener::OpenerExt;

#[cfg(windows)]
use std::os::windows::process::CommandExt;

use crate::daemon::find_solud;

// The relay and the browser-facing cloud address both come from the build's
// hosted-cloud setting (hosted_cloud.rs). The browser address is the hosted
// app's own domain, not the hosting provider's name. A build without a hosted
// cloud has nothing to pair with, so the wizard says so and stops.
use crate::hosted_cloud;

#[cfg(windows)]
const CREATE_NO_WINDOW: u32 = 0x08000000;

/// Public entrypoint for the FALLBACK native pair flow. Forks a
/// background thread so callers stay responsive.
///
/// No longer called from lib.rs (the tray item focuses the window and
/// the web banner drives pairing). Kept available as a fallback, so it
/// is intentionally allowed to be unused without a warning. Re-wire it
/// to a tray/menu item to re-enable the native dialog path.
#[allow(dead_code)]
pub fn run_pair_wizard(app: AppHandle) {
    let app = app.clone();
    thread::spawn(move || {
        let Some(relay) = hosted_cloud::relay_url() else {
            app.dialog()
                .message(
                    "This build has no hosted cloud configured, so there is \
                     nothing to pair with. It runs entirely on this computer.",
                )
                .title("Phone access unavailable")
                .kind(MessageDialogKind::Info)
                .blocking_show();
            return;
        };
        // Route to the WSL-aware flow when the user picked a backend
        // that involves WSL. On non-Windows hosts this is always
        // HostNative so the native flow is used.
        #[cfg(windows)]
        {
            use crate::backend::{resolve_backend, Backend};
            let b = resolve_backend(&app, true);
            if matches!(b, Backend::Wsl | Backend::Dual) {
                pair_flow_wsl(app, relay);
                return;
            }
        }
        pair_flow_native(app, relay);
    });
}

/// Claim via the host-native solud. Used on Linux/macOS, and on
/// Windows when the user picked Backend::HostNative.
fn pair_flow_native(app: AppHandle, relay: &str) {
    // Re-pair is ALWAYS available. We used to refuse here when
    // claim.json already existed ("delete the file first"), which dead-
    // ended the exact users who needed to retry (e.g. a machine that
    // paired but never connected, or moved accounts). A fresh claim
    // simply overwrites the old file and the relay supervisor picks up
    // the new token live, so just proceed.

    // Find and spawn solud --claim.
    let Some(bin) = find_solud() else {
        app.dialog()
            .message(
                "Can't find the solud binary.\n\n\
                 It should be bundled with the desktop app. If this is a dev build, \
                 run ./build.sh at the repo root so ./solud exists.",
            )
            .title("Pair failed")
            .kind(MessageDialogKind::Error)
            .blocking_show();
        return;
    };

    let mut cmd = Command::new(&bin);
    // daemon/claim.go prints the "Code: XYZ-ABC" line to STDERR (it's
    // intended as a human-readable prompt, not program output), so
    // that's what we have to capture. Piping stdout instead — the
    // original bug — made the dialog never appear because read_code
    // blocked on an empty stream forever.
    cmd.args(["--claim", "--relay", relay])
        .stdout(Stdio::null())
        .stderr(Stdio::piped());
    #[cfg(windows)]
    {
        cmd.creation_flags(CREATE_NO_WINDOW);
    }
    let mut child = match cmd.spawn() {
        Ok(c) => c,
        Err(e) => {
            app.dialog()
                .message(format!("Failed to start solud --claim: {}", e))
                .title("Pair failed")
                .kind(MessageDialogKind::Error)
                .blocking_show();
            return;
        }
    };
    // Assign to the Job Object so if the user quits Tauri mid-pair,
    // the claim subprocess dies with it instead of lingering until
    // its 10-minute TTL expires.
    #[cfg(windows)]
    crate::daemon::assign_child_to_job(&child);

    let stderr = child.stderr.take().expect("piped stderr");
    let code = match read_code(BufReader::new(stderr)) {
        Some(c) => c,
        None => {
            let _ = child.kill();
            app.dialog()
                .message("Didn't see a pairing code in solud's output.\nCheck the Tauri terminal for errors.")
                .title("Pair failed")
                .kind(MessageDialogKind::Error)
                .blocking_show();
            return;
        }
    };

    show_code_dialog(&app, &code);

    // Poll for claim file appearing. 10-min ceiling matches the
    // cloud-side TTL for pairing codes.
    let deadline = Instant::now() + Duration::from_secs(10 * 60);
    let claim = native_claim_path();
    let mut succeeded = false;
    while Instant::now() < deadline {
        if let Some(ref p) = claim {
            if p.exists() {
                succeeded = true;
                break;
            }
        }
        thread::sleep(Duration::from_millis(1000));
    }

    let _ = child.kill();
    let _ = child.wait();

    finish(&app, succeeded);
}

/// Claim inside WSL. Used on Windows when backend is Wsl or Dual.
/// The daemon's start-wsl.sh already copied the Linux binary to
/// $HOME/.local/share/soluterminal/solud before we got here (normal
/// first-launch daemon boot), so we can execute it directly.
#[cfg(windows)]
fn pair_flow_wsl(app: AppHandle, relay: &str) {
    // Re-pair is ALWAYS available (see pair_flow_native). We no longer
    // refuse when a WSL-side claim.json already exists — a fresh claim
    // overwrites it and the relay supervisor picks up the new token live.

    // The daemon's start-wsl.sh copies the Linux binary to
    // $HOME/.local/share/soluterminal/solud on boot. On a fresh install
    // this takes ~1-2s (12MB cp from /mnt/c). If we auto-fire the pair
    // wizard right after the window paints, we can beat the copy and
    // fail with "command not found". Poll for up to 15s — plenty of
    // head-room without being user-visible unless something's wrong.
    let bin_ready_deadline = Instant::now() + Duration::from_secs(15);
    while Instant::now() < bin_ready_deadline {
        let out = Command::new("wsl.exe")
            .args([
                "-e",
                "bash",
                "-c",
                "test -x \"$HOME/.local/share/soluterminal/solud\"",
            ])
            .creation_flags(CREATE_NO_WINDOW)
            .output();
        if out.map(|o| o.status.success()).unwrap_or(false) {
            break;
        }
        thread::sleep(Duration::from_millis(500));
    }

    // One-liner — bash expands $HOME inside WSL. Matches the path
    // the daemon start script copies the binary to.
    let wsl_cmd = format!(
        "$HOME/.local/share/soluterminal/solud --claim --relay {}",
        relay,
    );
    // See comment on the native path: the "Code: XYZ-ABC" line goes to
    // STDERR (daemon/claim.go:93-97). Must pipe stderr, not stdout.
    let mut child = match Command::new("wsl.exe")
        .args(["-e", "bash", "-c", &wsl_cmd])
        .stdout(Stdio::null())
        .stderr(Stdio::piped())
        .creation_flags(CREATE_NO_WINDOW)
        .spawn()
    {
        Ok(c) => c,
        Err(e) => {
            app.dialog()
                .message(format!(
                    "Failed to start WSL claim: {}\n\nIs WSL installed and \
                     the daemon's first-launch copy step complete?",
                    e
                ))
                .title("Pair failed")
                .kind(MessageDialogKind::Error)
                .blocking_show();
            return;
        }
    };
    // Assign the wsl.exe launcher to the Job Object. If the user
    // quits Tauri mid-pair, wsl.exe dies; the Linux solud --claim
    // inside WSL keeps running until its TTL expires but that's
    // harmless (start-wsl.sh pkills orphans on next daemon spawn).
    crate::daemon::assign_child_to_job(&child);

    let stderr = child.stderr.take().expect("piped stderr");
    let code = match read_code(BufReader::new(stderr)) {
        Some(c) => c,
        None => {
            let _ = child.kill();
            app.dialog()
                .message(
                    "Didn't see a pairing code in the WSL daemon's output.\n\n\
                     Try relaunching soluterminal so the WSL daemon is freshly \
                     started, then retry pairing from the tray menu.",
                )
                .title("Pair failed")
                .kind(MessageDialogKind::Error)
                .blocking_show();
            return;
        }
    };

    show_code_dialog(&app, &code);

    // Poll for WSL-side claim.json.
    let deadline = Instant::now() + Duration::from_secs(10 * 60);
    let mut succeeded = false;
    while Instant::now() < deadline {
        if wsl_claim_exists() {
            succeeded = true;
            break;
        }
        thread::sleep(Duration::from_millis(1000));
    }

    let _ = child.kill();
    let _ = child.wait();

    if succeeded {
        // Mirror the claim to the Windows side so the host-native
        // daemon (whether now in Dual mode or if the user switches
        // backend later) is automatically paired too. Best-effort —
        // we don't block success on this.
        if let Err(e) = copy_wsl_claim_to_windows() {
            eprintln!("pair: couldn't mirror claim to Windows side: {}", e);
        }
    }

    finish(&app, succeeded);
}

/// Show the 6-char code dialog + open the cloud site for the user
/// to complete the claim on the web side. Used by both flows.
fn show_code_dialog(app: &AppHandle, code: &str) {
    let app_for_dialog = app.clone();
    app.dialog()
        .message(format!(
            "Pairing code:  {}\n\n\
             Enter this code in the soluterminal app on your phone \
             (using the same account), or click OK to open the cloud \
             site and claim this machine in your browser.",
            code,
        ))
        .title("Set up phone access")
        .kind(MessageDialogKind::Info)
        .show(move |_ok| {
            if let Some(url) = hosted_cloud::app_url() {
                let _ = app_for_dialog.opener().open_url(url, None::<&str>);
            }
        });
}

/// Post-claim: show success/timeout. NO restart anymore — the daemon's relay
/// supervisor re-reads claim.json on a poll and dials the relay live within a
/// few seconds, on every OS. The old Windows-only `app.restart()` was both a
/// platform-split lie (macOS/Linux never restarted, so the boot-time
/// "cloud disabled" decision was never revisited → the 7-week-dark machine)
/// and now unnecessary. Honest copy: "connecting", not "restarting".
fn finish(app: &AppHandle, succeeded: bool) {
    if succeeded {
        app.dialog()
            .message(
                "Paired — connecting…\n\n\
                 This machine is now paired with the cloud. It will connect \
                 within a few seconds — no restart needed. Voice transcription \
                 and cloud features come online automatically.",
            )
            .title("Paired — connecting…")
            .kind(MessageDialogKind::Info)
            .blocking_show();
    } else {
        app.dialog()
            .message(
                "The pairing code expired before it was accepted. \
                 You can set up phone access again any time from the \
                 banner in the app (or the tray \"Pair with cloud…\" item).",
            )
            .title("Pair timed out")
            .kind(MessageDialogKind::Warning)
            .blocking_show();
    }
}

fn read_code<R: BufRead>(reader: R) -> Option<String> {
    for line in reader.lines().map_while(Result::ok) {
        // Daemon prints `│  Code:  XYZ-W42    ...   │` inside a box.
        if let Some(idx) = line.find("Code:") {
            let rest = &line[idx + "Code:".len()..];
            let code: String = rest
                .chars()
                .skip_while(|c| c.is_whitespace())
                .take_while(|c| c.is_ascii_alphanumeric() || *c == '-')
                .collect();
            if !code.is_empty() {
                return Some(code);
            }
        }
    }
    None
}

/// Location the NATIVE daemon (host-native on Windows, or the sole
/// daemon on Linux/macOS) reads its claim from.
pub fn native_claim_path() -> Option<PathBuf> {
    // dirs::home_dir maps to HOME on Unix and USERPROFILE on Windows,
    // matching where the daemon itself stores ~/.solu/.
    Some(dirs::home_dir()?.join(".solu").join("claim.json"))
}

/// True iff the WSL-side daemon has a claim.json inside WSL.
/// `wsl.exe -e test -f <path>` exits 0/1 — we can't use $HOME expansion
/// without a shell, so go through bash.
#[cfg(windows)]
pub fn wsl_claim_exists() -> bool {
    Command::new("wsl.exe")
        .args(["-e", "bash", "-c", "test -f \"$HOME/.solu/claim.json\""])
        .creation_flags(CREATE_NO_WINDOW)
        .output()
        .map(|out| out.status.success())
        .unwrap_or(false)
}

/// Read WSL's claim.json and write a copy at %USERPROFILE%\.solu\claim.json
/// so the host-native daemon is also paired. Best-effort — never blocks
/// the success path.
#[cfg(windows)]
fn copy_wsl_claim_to_windows() -> Result<(), String> {
    let out = Command::new("wsl.exe")
        .args(["-e", "bash", "-c", "cat \"$HOME/.solu/claim.json\""])
        .creation_flags(CREATE_NO_WINDOW)
        .output()
        .map_err(|e| format!("run wsl cat: {e}"))?;
    if !out.status.success() {
        return Err(format!(
            "wsl cat claim.json exited {}: {}",
            out.status,
            String::from_utf8_lossy(&out.stderr).trim()
        ));
    }
    let Some(dst) = native_claim_path() else {
        return Err("no home dir".into());
    };
    if let Some(dir) = dst.parent() {
        std::fs::create_dir_all(dir).map_err(|e| format!("mkdir {}: {}", dir.display(), e))?;
    }
    std::fs::write(&dst, &out.stdout).map_err(|e| format!("write {}: {}", dst.display(), e))?;
    // Tighten perms on Windows — os::unix perms don't apply but leaving
    // this file readable is OK (it's under the user's profile).
    Ok(())
}

/// True iff EITHER side has a claim — used by lib.rs to decide whether
/// to auto-open the pair wizard on first launch. We consider the user
/// paired if the backend they picked has a claim; we don't require
/// BOTH sides to be paired.
pub fn any_claim_exists() -> bool {
    if let Some(p) = native_claim_path() {
        if p.exists() {
            return true;
        }
    }
    #[cfg(windows)]
    {
        if wsl_claim_exists() {
            return true;
        }
    }
    false
}
