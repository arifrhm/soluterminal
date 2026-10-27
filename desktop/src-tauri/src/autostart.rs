#![cfg_attr(not(windows), allow(dead_code))]

use std::fs;
use std::path::{Path, PathBuf};

pub const RUN_KEY: &str = "Software\\Microsoft\\Windows\\CurrentVersion\\Run";
// Must stay in sync with the DeleteRegValue in installer/hooks.nsh.
pub const RUN_VALUE_NAME: &str = "soluterminal";
pub const AUTOSTART_ARG: &str = "--autostart";
pub const AUTOSTART_CONSUMED_ENV: &str = "SOLU_AUTOSTART_CONSUMED";

pub fn consume_autostart_arg<Args, Argument>(args: Args, already_consumed: bool) -> bool
where
    Args: IntoIterator<Item = Argument>,
    Argument: AsRef<str>,
{
    !already_consumed
        && args
            .into_iter()
            .any(|argument| argument.as_ref() == AUTOSTART_ARG)
}

fn pref_path() -> Option<PathBuf> {
    #[cfg(windows)]
    {
        let mut path = PathBuf::from(std::env::var_os("LOCALAPPDATA")?);
        path.push("soluterminal");
        path.push("autostart.txt");
        Some(path)
    }

    #[cfg(not(windows))]
    {
        let mut path = dirs::config_dir()?;
        path.push("soluterminal");
        path.push("autostart.txt");
        Some(path)
    }
}

fn parse_pref(value: &str) -> Option<bool> {
    match value.trim() {
        "on" => Some(true),
        "off" => Some(false),
        _ => None,
    }
}

fn read_pref_at(path: &Path) -> Option<bool> {
    parse_pref(&fs::read_to_string(path).ok()?)
}

fn read_pref() -> Option<bool> {
    read_pref_at(&pref_path()?)
}

fn write_pref_at(path: &Path, enabled: bool) -> std::io::Result<()> {
    if let Some(parent) = path.parent() {
        fs::create_dir_all(parent)?;
    }
    fs::write(path, if enabled { "on" } else { "off" })
}

fn write_pref(enabled: bool) -> Result<(), String> {
    let path = pref_path().ok_or_else(|| "could not resolve autostart preference path".to_string())?;
    write_pref_at(&path, enabled)
        .map_err(|error| format!("could not persist launch-on-startup preference: {error}"))
}

fn decide(pref: Option<bool>) -> Option<bool> {
    pref
}

#[cfg(any(windows, test))]
fn apply_reconcile<Registry>(
    preference: Option<bool>,
    mut set_registry: Registry,
) -> Result<(), String>
where
    Registry: FnMut(bool) -> Result<(), String>,
{
    match decide(preference) {
        Some(enabled) => set_registry(enabled),
        None => Ok(()),
    }
}

#[cfg(any(windows, test))]
fn reported_enabled(_pref: Option<bool>, registry_enabled: bool) -> bool {
    registry_enabled
}

#[cfg(any(windows, test))]
fn apply_setting<Registry, Persist>(
    enabled: bool,
    previous_registry_enabled: bool,
    mut set_registry: Registry,
    mut persist: Persist,
) -> Result<(), String>
where
    Registry: FnMut(bool) -> Result<(), String>,
    Persist: FnMut(bool) -> Result<(), String>,
{
    set_registry(enabled)?;
    if let Err(preference_error) = persist(enabled) {
        return match set_registry(previous_registry_enabled) {
            Ok(()) => Err(preference_error),
            Err(rollback_error) => Err(format!(
                "{preference_error}; additionally could not restore the previous registry state: {rollback_error}"
            )),
        };
    }
    Ok(())
}

#[cfg(any(windows, test))]
fn run_value(exe_path: &str) -> String {
    format!(r#""{exe_path}" {AUTOSTART_ARG}"#)
}

#[cfg(any(windows, test))]
fn reg_key() -> String {
    format!(r"HKCU\{RUN_KEY}")
}

#[cfg(any(windows, test))]
fn reg_add_args(exe_path: &str) -> Vec<String> {
    vec![
        "add".into(),
        reg_key(),
        "/v".into(),
        RUN_VALUE_NAME.into(),
        "/t".into(),
        "REG_SZ".into(),
        "/d".into(),
        run_value(exe_path),
        "/f".into(),
    ]
}

#[cfg(any(windows, test))]
fn reg_query_args() -> Vec<String> {
    vec![
        "query".into(),
        reg_key(),
        "/v".into(),
        RUN_VALUE_NAME.into(),
    ]
}

#[cfg(any(windows, test))]
fn reg_delete_args() -> Vec<String> {
    vec![
        "delete".into(),
        reg_key(),
        "/v".into(),
        RUN_VALUE_NAME.into(),
        "/f".into(),
    ]
}

#[cfg(windows)]
fn reg_status(args: &[String]) -> Result<std::process::ExitStatus, String> {
    use std::os::windows::process::CommandExt;
    use std::process::{Command, Stdio};

    const CREATE_NO_WINDOW: u32 = 0x0800_0000;
    Command::new("reg")
        .args(args)
        .stdin(Stdio::null())
        .stdout(Stdio::null())
        .stderr(Stdio::null())
        .creation_flags(CREATE_NO_WINDOW)
        .status()
        .map_err(|error| format!("could not run reg.exe: {error}"))
}

#[cfg(windows)]
fn registry_enabled() -> bool {
    reg_status(&reg_query_args()).is_ok_and(|status| status.success())
}

#[cfg(windows)]
fn registry_set(enabled: bool) -> Result<(), String> {
    let args = if enabled {
        let exe = std::env::current_exe()
            .map_err(|error| format!("could not resolve desktop executable: {error}"))?;
        reg_add_args(&exe.to_string_lossy())
    } else {
        // `reg delete` exits non-zero when the value is absent. Query first so
        // disabling an already-disabled setting remains idempotent.
        if !reg_status(&reg_query_args())?.success() {
            return Ok(());
        }
        reg_delete_args()
    };

    let status = reg_status(&args)?;
    if status.success() {
        Ok(())
    } else {
        Err(format!(
            "reg.exe failed to {} the launch-on-startup value (status {status})",
            if enabled { "set" } else { "delete" }
        ))
    }
}

/// Re-assert the configured value on every Windows launch. This repairs the
/// stable Run-key path after an update, whose uninstall pass removes the old
/// registration before the new shell starts.
pub fn reconcile() {
    #[cfg(windows)]
    let _ = apply_reconcile(read_pref(), registry_set);
}

#[derive(serde::Serialize)]
pub struct AutostartStatus {
    supported: bool,
    enabled: bool,
}

#[tauri::command]
pub fn desktop_get_autostart() -> AutostartStatus {
    #[cfg(windows)]
    {
        AutostartStatus {
            supported: true,
            enabled: reported_enabled(read_pref(), registry_enabled()),
        }
    }
    #[cfg(not(windows))]
    {
        AutostartStatus {
            supported: false,
            enabled: false,
        }
    }
}

#[tauri::command]
pub fn desktop_set_autostart(enabled: bool) -> Result<AutostartStatus, String> {
    #[cfg(windows)]
    {
        let previous_registry_enabled = registry_enabled();
        apply_setting(
            enabled,
            previous_registry_enabled,
            registry_set,
            write_pref,
        )?;
        Ok(desktop_get_autostart())
    }
    #[cfg(not(windows))]
    {
        let _ = enabled;
        Err("Launch on startup is only available on Windows".to_string())
    }
}

#[cfg(test)]
mod tests {
    use super::{
        decide, read_pref_at, reg_add_args, reg_delete_args, reg_query_args, run_value,
        write_pref_at, AUTOSTART_ARG, RUN_KEY, RUN_VALUE_NAME,
    };
    use std::time::{SystemTime, UNIX_EPOCH};

    #[test]
    fn run_value_quotes_executable_and_appends_autostart_once() {
        let value = run_value(r"C:\Program Files\soluterminal\soluterminal.exe");
        assert_eq!(
            value,
            r#""C:\Program Files\soluterminal\soluterminal.exe" --autostart"#
        );
        assert_eq!(value.matches(AUTOSTART_ARG).count(), 1);
    }

    #[test]
    fn registry_argument_shapes_are_exact() {
        let key = format!(r"HKCU\{RUN_KEY}");
        assert_eq!(
            reg_add_args(r"C:\soluterminal\soluterminal.exe"),
            vec![
                "add",
                &key,
                "/v",
                RUN_VALUE_NAME,
                "/t",
                "REG_SZ",
                "/d",
                r#""C:\soluterminal\soluterminal.exe" --autostart"#,
                "/f",
            ]
        );
        assert_eq!(reg_query_args(), vec!["query", &key, "/v", RUN_VALUE_NAME]);
        assert_eq!(
            reg_delete_args(),
            vec!["delete", &key, "/v", RUN_VALUE_NAME, "/f"]
        );
    }

    #[test]
    fn preference_round_trips_and_rejects_unknown_or_missing_values() {
        let unique = SystemTime::now()
            .duration_since(UNIX_EPOCH)
            .expect("clock")
            .as_nanos();
        let dir = std::env::temp_dir().join(format!("solu-autostart-test-{unique}"));
        let path = dir.join("autostart.txt");

        assert_eq!(read_pref_at(&path), None);
        write_pref_at(&path, true).expect("write on");
        assert_eq!(read_pref_at(&path), Some(true));
        write_pref_at(&path, false).expect("write off");
        assert_eq!(read_pref_at(&path), Some(false));
        std::fs::write(&path, "garbage").expect("write garbage");
        assert_eq!(read_pref_at(&path), None);

        let _ = std::fs::remove_dir_all(dir);
    }

    #[test]
    fn reconcile_decision_preserves_all_preference_states() {
        assert_eq!(decide(Some(true)), Some(true));
        assert_eq!(decide(Some(false)), Some(false));
        assert_eq!(decide(None), None);
    }

    #[test]
    fn reported_status_uses_live_registry_not_stale_preference() {
        assert!(super::reported_enabled(Some(false), true));
        assert!(!super::reported_enabled(Some(true), false));
    }

    #[test]
    fn preference_failure_rolls_registry_back_to_previous_state() {
        let mut registry_writes = Vec::new();
        let result = super::apply_setting(
            true,
            false,
            |enabled| {
                registry_writes.push(enabled);
                Ok(())
            },
            |_| Err("preference is read-only".to_string()),
        );

        assert!(result.is_err());
        assert_eq!(registry_writes, vec![true, false]);
    }

    #[test]
    fn reconcile_drives_add_delete_and_noop_through_the_real_action_path() {
        for (preference, expected_writes) in [
            (Some(true), vec![true]),
            (Some(false), vec![false]),
            (None, vec![]),
        ] {
            let mut writes = Vec::new();
            super::apply_reconcile(preference, |enabled| {
                writes.push(enabled);
                Ok(())
            })
            .expect("reconcile action");
            assert_eq!(writes, expected_writes);
        }
    }

    #[test]
    fn standalone_uninstall_clears_intent_but_update_preserves_it() {
        let hooks = include_str!("../installer/hooks.nsh");
        let cleanup = hooks
            .split("!macro NSIS_HOOK_PREUNINSTALL")
            .nth(1)
            .expect("pre-uninstall hook");
        assert!(cleanup.contains("${If} $UpdateMode <> 1"));
        assert!(cleanup.contains(r#"Delete "$LOCALAPPDATA\soluterminal\autostart.txt""#));
        assert!(cleanup.contains("DeleteRegValue HKCU"));
    }

    // A standalone uninstall removes what SoluTerminal added to Claude Code and
    // Codex on both sides of the machine; the uninstall-old phase of an update
    // must not.
    #[test]
    fn standalone_uninstall_cleans_agent_integration_but_update_does_not() {
        let hooks = include_str!("../installer/hooks.nsh");
        let pre = hooks
            .split("!macro NSIS_HOOK_PREUNINSTALL")
            .nth(1)
            .expect("pre-uninstall hook");
        let pre = pre.split("!macroend").next().expect("hook body");
        let standalone = pre
            .split("${If} $UpdateMode <> 1")
            .nth(1)
            .and_then(|rest| rest.split("${EndIf}").next())
            .expect("standalone-only block");
        assert!(standalone.contains("!insertmacro _SOLU_AGENT_INTEGRATION_CLEANUP"));

        let cleanup = hooks
            .split("!macro _SOLU_AGENT_INTEGRATION_CLEANUP")
            .nth(1)
            .and_then(|rest| rest.split("!macroend").next())
            .expect("cleanup macro");
        assert!(cleanup.contains(r#""$INSTDIR\solud.exe" uninstall-cleanup"#));
        assert!(cleanup.contains(r#""$SYSDIR\wsl.exe" -e sh -lc"#));
        assert!(cleanup.contains("/.local/share/soluterminal/solud"));
        // Bounded so a stuck WSL VM cannot hang the uninstaller.
        assert_eq!(cleanup.matches("/TIMEOUT=").count(), 2);
        // The macro must be defined before the hook that inserts it.
        let defined = hooks.find("!macro _SOLU_AGENT_INTEGRATION_CLEANUP").unwrap();
        let used = hooks.find("!macro NSIS_HOOK_PREUNINSTALL").unwrap();
        assert!(defined < used);
    }

    #[test]
    fn autostart_argument_is_consumed_only_for_the_initial_process() {
        assert!(super::consume_autostart_arg([AUTOSTART_ARG], false));
        assert!(!super::consume_autostart_arg([AUTOSTART_ARG], true));
        assert!(!super::consume_autostart_arg(["--unrelated"], false));
    }

    #[cfg(not(windows))]
    #[test]
    fn unsupported_platform_status_is_hidden_and_setting_is_rejected() {
        let status = super::desktop_get_autostart();
        assert!(!status.supported);
        assert!(!status.enabled);
        let result = super::desktop_set_autostart(true);
        assert_eq!(
            result.err().as_deref(),
            Some("Launch on startup is only available on Windows")
        );
    }
}
