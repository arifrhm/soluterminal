use super::{admit_installer, installed_daemon_version, InstalledDaemon};

fn fixture_dir(label: &str) -> std::path::PathBuf {
    let unique = format!(
        "solu-shell-{label}-{}-{}",
        std::process::id(),
        std::time::SystemTime::now()
            .duration_since(std::time::UNIX_EPOCH)
            .expect("clock")
            .as_nanos()
    );
    let dir = std::env::temp_dir().join(unique);
    std::fs::create_dir_all(&dir).expect("create fixture");
    dir
}

#[test]
fn pre_marker_install_is_not_mistaken_for_a_fresh_install() {
    let dir = fixture_dir("pre-marker-install");
    let app = dir.join("soluterminal.exe");

    let fresh = installed_daemon_version(&app).expect("inspect true fresh install");
    assert_eq!(fresh, InstalledDaemon::Absent);
    admit_installer(&fresh, "", "0.0.916", false)
        .expect("a true fresh install has no daemon to downgrade");

    std::fs::write(
        dir.join("solud.exe"),
        b"existing pre-marker daemon\x00-ldflags=\"-X main.version=0.0.932\"\x00",
    )
    .expect("write existing daemon");
    let installed = installed_daemon_version(&app).expect("inspect pre-marker install");
    assert_eq!(
        installed,
        InstalledDaemon::Known {
            version: "0.0.932".to_string(),
            source: "binary"
        }
    );
    let refusal = admit_installer(&installed, "", "0.0.916", false)
        .expect_err("the marker-less installed 0.0.932 daemon must not be downgraded");
    assert_eq!(refusal.installer_version, "0.0.916");
    assert_eq!(refusal.installed_version, "0.0.932");
    assert!(refusal.message.contains("forceDowngrade:true"));
    admit_installer(&installed, "", "0.0.916", true)
        .expect("the explicit force flag may replace the newer daemon");
    admit_installer(&installed, "", "0.0.932", false).expect("same version may reinstall");
    admit_installer(&installed, "", "0.0.940", false)
        .expect("marker-less legacy install may still upgrade");

    std::fs::write(dir.join("solud.exe"), b"existing daemon without build info")
        .expect("overwrite with unversioned daemon");
    let unknown = installed_daemon_version(&app).expect("inspect unversioned daemon");
    assert!(matches!(unknown, InstalledDaemon::Unknown(_)));
    let refusal = admit_installer(&unknown, "", "0.0.916", false)
        .expect_err("unknown installed daemon must fail closed");
    assert!(refusal.message.contains("version is unknown"));
    admit_installer(&unknown, "", "0.0.916", true)
        .expect("explicit force may replace an unknown daemon");

    std::fs::remove_dir_all(dir).expect("clean fixture");
}

#[test]
fn malformed_marker_beside_existing_daemon_fails_closed() {
    let dir = fixture_dir("malformed-marker");
    let app = dir.join("soluterminal.exe");
    std::fs::write(dir.join("solud.exe"), b"existing daemon").expect("write daemon");
    std::fs::write(dir.join("solud.exe.version"), b"not-a-version\r\n")
        .expect("write malformed marker");

    let installed = installed_daemon_version(&app).expect("inspect malformed marker");
    assert!(matches!(installed, InstalledDaemon::Unknown(_)));
    let refusal = admit_installer(&installed, "", "0.0.916", false)
        .expect_err("an existing daemon with a malformed marker must fail closed");
    assert_eq!(refusal.installer_version, "0.0.916");
    assert!(refusal.message.contains("version is unknown"));

    std::fs::write(
        dir.join("solud.exe"),
        b"build\x00-ldflags=\"-X main.version=0.0.932\"\x00",
    )
    .expect("write embedded build version");
    let derived = installed_daemon_version(&app).expect("derive despite malformed marker");
    assert_eq!(
        derived,
        InstalledDaemon::Known {
            version: "0.0.932".to_string(),
            source: "binary"
        }
    );
    let refusal = admit_installer(&derived, "", "0.0.916", false)
        .expect_err("derived newer daemon must be refused");
    assert_eq!(refusal.installed_version, "0.0.932");

    std::fs::remove_dir_all(dir).expect("clean fixture");
}
