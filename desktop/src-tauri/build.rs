// build.rs — emit SOLU_SHELL_VERSION at compile time so the Rust side
// can pass `--shell-version <ver>` when spawning solud.
//
// Why not env!("CARGO_PKG_VERSION"): Cargo.toml's version is stale
// (0.0.1) — the source of truth for the Tauri shell version is
// tauri.conf.json, which `./publish.sh` reads. Keeping a single
// version file (tauri.conf.json) avoids the drift class of bug.
//
// We parse with a tiny line-scan rather than pulling in serde_json
// as a build-dependency. The shape is stable ({"version": "0.0.x", ...}).
fn main() {
    let conf = std::fs::read_to_string("tauri.conf.json")
        .expect("build.rs: read tauri.conf.json");
    let mut version: Option<String> = None;
    for line in conf.lines() {
        let trimmed = line.trim();
        // Match top-level "version": "X.Y.Z" — first one wins, since
        // tauri.conf.json's top-level version is canonical.
        if let Some(rest) = trimmed.strip_prefix("\"version\"") {
            // rest looks like: ` : "0.0.327",`
            if let Some(idx) = rest.find('"') {
                let after_quote = &rest[idx + 1..];
                if let Some(end) = after_quote.find('"') {
                    version = Some(after_quote[..end].to_string());
                    break;
                }
            }
        }
    }
    let version = version.expect("build.rs: version not found in tauri.conf.json");
    println!("cargo:rustc-env=SOLU_SHELL_VERSION={}", version);
    println!("cargo:rerun-if-changed=tauri.conf.json");

    // Stamp the source commit so a hand-installed shell can be told apart from
    // a release built from main (the 0.0.1199 shell carrying an unmerged WSL
    // fix looked identical to a release). SOLU_SOURCE_SHA / SOLU_SOURCE_DIRTY
    // win when set; otherwise ask git, and say "unknown" without it.
    let (sha, dirty) = source_stamp();
    println!("cargo:rustc-env=SOLU_SOURCE_SHA={}", sha);
    println!("cargo:rustc-env=SOLU_SOURCE_DIRTY={}", dirty);
    println!("cargo:rerun-if-env-changed=SOLU_SOURCE_SHA");
    println!("cargo:rerun-if-env-changed=SOLU_SOURCE_DIRTY");

    // src/shell_update_manifest.rs bakes in SOLU_UPDATE_MANIFEST_PUBKEY when it
    // is set. A malformed key would ship a shell that refuses every update, so
    // stop the build here instead of finding out on a user's machine.
    println!("cargo:rerun-if-env-changed=SOLU_UPDATE_MANIFEST_PUBKEY");
    if let Ok(key) = std::env::var("SOLU_UPDATE_MANIFEST_PUBKEY") {
        if !key.is_empty() && !looks_like_ed25519_pubkey(&key) {
            panic!(
                "build.rs: SOLU_UPDATE_MANIFEST_PUBKEY is not a base64 raw Ed25519 public key (32 bytes); unset it to use the default key"
            );
        }
    }
    hosted_cloud();
    tauri_build::build();
}

// The hosted cloud (account UI + relay) this shell talks to is a build-time
// setting, read here and handed to src/hosted_cloud.rs:
//
//   SOLU_CLOUD_URL      origin of the cloud service, e.g. https://cloud.example.com
//                       (the relay is <origin>/relay over wss/ws)
//   SOLU_CLOUD_APP_URL  optional browser-facing address of the same cloud,
//                       opened for pairing (defaults to SOLU_CLOUD_URL)
//
// SOLU_CLOUD_URL in the environment wins, even when empty (SOLU_CLOUD_URL=
// builds a shell with no cloud), and then only the environment is read: a
// staging or self-hosted origin never picks up the product's app address.
// Otherwise hosted-cloud.env next to this file supplies the product's
// values. (Its
// rerun-if-changed is printed only when it exists: a file created later
// needs a clean build.)
fn hosted_cloud() {
    const DEFAULTS: &str = "hosted-cloud.env";
    println!("cargo:rerun-if-env-changed=SOLU_CLOUD_URL");
    println!("cargo:rerun-if-env-changed=SOLU_CLOUD_APP_URL");
    let defaults = match std::fs::read_to_string(DEFAULTS) {
        Ok(text) => {
            println!("cargo:rerun-if-changed={DEFAULTS}");
            text
        }
        Err(_) => String::new(),
    };
    let from_env = std::env::var_os("SOLU_CLOUD_URL").is_some();
    let setting = |key: &str| -> String {
        if from_env {
            return std::env::var(key).unwrap_or_default().trim().to_string();
        }
        defaults
            .lines()
            .map(str::trim)
            .filter(|line| !line.starts_with('#'))
            .find_map(|line| line.strip_prefix(key)?.strip_prefix('='))
            .map(|value| value.trim().to_string())
            .unwrap_or_default()
    };
    let origin = setting("SOLU_CLOUD_URL");
    let app = setting("SOLU_CLOUD_APP_URL");
    let (web, relay, app) = if origin.is_empty() {
        if !app.is_empty() && std::env::var_os("SOLU_CLOUD_APP_URL").is_some() {
            println!("cargo:warning=SOLU_CLOUD_APP_URL is ignored without SOLU_CLOUD_URL; this shell has no hosted cloud");
        }
        (String::new(), String::new(), String::new())
    } else {
        let origin = cloud_origin("SOLU_CLOUD_URL", &origin);
        let app = if app.is_empty() { origin.clone() } else { cloud_origin("SOLU_CLOUD_APP_URL", &app) };
        let relay = match origin.strip_prefix("https://") {
            Some(host) => format!("wss://{host}/relay"),
            None => format!("ws://{}/relay", &origin["http://".len()..]),
        };
        warn_if_not_allowed(&origin);
        (format!("{origin}/"), relay, format!("{app}/"))
    };
    println!("cargo:rustc-env=SOLU_BUILD_CLOUD_WEB_URL={web}");
    println!("cargo:rustc-env=SOLU_BUILD_CLOUD_RELAY_URL={relay}");
    println!("cargo:rustc-env=SOLU_BUILD_CLOUD_APP_URL={app}");
}

// An http(s) origin without a path, query or trailing slash.
fn cloud_origin(key: &str, value: &str) -> String {
    let value = value.trim_end_matches('/');
    let host = value.strip_prefix("https://").or_else(|| value.strip_prefix("http://"));
    match host {
        Some(host)
            if !host.is_empty()
                && host.bytes().all(|b| b.is_ascii_alphanumeric() || matches!(b, b'.' | b'-' | b':')) =>
        {
            value.to_string()
        }
        _ => panic!("build.rs: {key}={value:?} must be an http(s) origin such as https://cloud.example.com (no path)"),
    }
}

// The shell loads the cloud's account UI in its own windows, which only get
// the Tauri plugins when the capabilities list the origin under remote.urls
// (and the CSP allows it). Say so at build time instead of shipping a cloud
// mode whose buttons silently do nothing.
fn warn_if_not_allowed(origin: &str) {
    let mut files = vec!["tauri.conf.json".to_string()];
    if let Ok(entries) = std::fs::read_dir("capabilities") {
        let mut caps: Vec<String> = entries
            .filter_map(|e| e.ok()?.path().to_str().map(str::to_string))
            .filter(|p| p.ends_with(".json"))
            .collect();
        caps.sort();
        files.extend(caps);
    }
    for file in files {
        let text = std::fs::read_to_string(&file).unwrap_or_default();
        let lists_remotes = file == "tauri.conf.json" || text.contains("\"remote\"");
        if lists_remotes && !text.contains(origin) {
            println!("cargo:warning={file} does not mention {origin}; add it to remote.urls / the CSP so the cloud UI can use the shell");
        }
    }
}

// 32 bytes is 43 base64 characters plus optional '=' padding, in either the
// standard or URL-safe alphabet (the shell's decoder accepts both).
fn looks_like_ed25519_pubkey(key: &str) -> bool {
    let body = key.strip_suffix('=').unwrap_or(key);
    body.len() == 43
        && body
            .bytes()
            .all(|b| b.is_ascii_alphanumeric() || matches!(b, b'+' | b'/' | b'-' | b'_'))
}

fn source_stamp() -> (String, String) {
    let git = |args: &[&str]| {
        std::process::Command::new("git")
            .args(args)
            .output()
            .ok()
            .filter(|out| out.status.success())
            .map(|out| String::from_utf8_lossy(&out.stdout).trim().to_string())
    };
    let sha = std::env::var("SOLU_SOURCE_SHA")
        .ok()
        .filter(|s| !s.trim().is_empty())
        .or_else(|| git(&["rev-parse", "HEAD"]))
        .unwrap_or_else(|| "unknown".to_string());
    let dirty = std::env::var("SOLU_SOURCE_DIRTY")
        .ok()
        .filter(|s| !s.trim().is_empty())
        .or_else(|| {
            git(&["status", "--porcelain", "--untracked-files=no"])
                .map(|s| if s.is_empty() { "0".to_string() } else { "1".to_string() })
        })
        .unwrap_or_else(|| "unknown".to_string());
    (sha, dirty)
}
