use std::fs;
use std::path::PathBuf;

use crate::hosted_cloud;

#[derive(Copy, Clone, Debug, PartialEq, Eq)]
pub enum LaunchMode {
    Local,
    Cloud,
}

pub fn resolve() -> LaunchMode {
    resolve_with(read_pref(), hosted_cloud::configured())
}

/// A saved Cloud preference only counts in a build that has a hosted cloud;
/// otherwise the shell runs on this computer.
fn resolve_with(pref: Option<LaunchMode>, cloud_configured: bool) -> LaunchMode {
    match pref {
        Some(LaunchMode::Cloud) if cloud_configured => LaunchMode::Cloud,
        _ => LaunchMode::Local,
    }
}

/// The account UI Cloud mode loads. Only Some in a build with a hosted cloud,
/// which resolve() requires before it returns Cloud.
pub fn cloud_web_url() -> Option<&'static str> {
    hosted_cloud::web_url()
}

/// Refuse to save Cloud in a build without a hosted cloud.
pub fn check_available(mode: LaunchMode) -> Result<(), String> {
    if mode == LaunchMode::Cloud && !hosted_cloud::configured() {
        return Err("This build has no hosted cloud configured (SOLU_CLOUD_URL), so it only runs on this computer.".into());
    }
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn cloud_preference_needs_a_configured_cloud() {
        assert_eq!(resolve_with(Some(LaunchMode::Cloud), true), LaunchMode::Cloud);
        assert_eq!(resolve_with(Some(LaunchMode::Cloud), false), LaunchMode::Local);
        assert_eq!(resolve_with(Some(LaunchMode::Local), true), LaunchMode::Local);
        assert_eq!(resolve_with(None, true), LaunchMode::Local);
        assert_eq!(resolve_with(None, false), LaunchMode::Local);
    }

    #[test]
    fn local_is_always_available() {
        assert!(check_available(LaunchMode::Local).is_ok());
        assert_eq!(check_available(LaunchMode::Cloud).is_ok(), hosted_cloud::configured());
    }
}

pub fn set_pref(mode: LaunchMode) -> std::io::Result<()> {
    write_pref(mode)
}

pub fn label(mode: LaunchMode) -> &'static str {
    match mode {
        LaunchMode::Local => "Local daemon",
        LaunchMode::Cloud => "Cloud account",
    }
}

fn pref_path() -> Option<PathBuf> {
    #[cfg(windows)]
    {
        let mut p = PathBuf::from(std::env::var_os("LOCALAPPDATA")?);
        p.push("soluterminal");
        p.push("desktop-mode.txt");
        Some(p)
    }

    #[cfg(not(windows))]
    {
        let mut p = dirs::config_dir()?;
        p.push("soluterminal");
        p.push("desktop-mode.txt");
        Some(p)
    }
}

fn read_pref() -> Option<LaunchMode> {
    let p = pref_path()?;
    let s = fs::read_to_string(&p).ok()?;
    match s.trim() {
        "local" | "daemon" => Some(LaunchMode::Local),
        "cloud" | "account" => Some(LaunchMode::Cloud),
        _ => None,
    }
}

fn write_pref(mode: LaunchMode) -> std::io::Result<()> {
    let p = pref_path()
        .ok_or_else(|| std::io::Error::new(std::io::ErrorKind::NotFound, "no config directory"))?;
    if let Some(dir) = p.parent() {
        fs::create_dir_all(dir)?;
    }
    let value = match mode {
        LaunchMode::Local => "local",
        LaunchMode::Cloud => "cloud",
    };
    fs::write(&p, value)
}
