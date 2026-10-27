// The hosted cloud (account UI + relay) this shell talks to. It is fixed at
// build time by build.rs from SOLU_CLOUD_URL / SOLU_CLOUD_APP_URL or
// hosted-cloud.env; a build without one has no cloud at all: the daemon gets
// no --relay, Cloud launch mode is refused and pairing is unavailable.

/// Account UI loaded in Cloud launch mode, with a trailing slash.
const WEB_URL: &str = env!("SOLU_BUILD_CLOUD_WEB_URL");
/// Relay passed to every spawned daemon as `--relay`.
const RELAY_URL: &str = env!("SOLU_BUILD_CLOUD_RELAY_URL");
/// Browser-facing address of the same cloud, opened by the pairing dialog.
const APP_URL: &str = env!("SOLU_BUILD_CLOUD_APP_URL");

fn non_empty(value: &'static str) -> Option<&'static str> {
    (!value.is_empty()).then_some(value)
}

pub fn configured() -> bool {
    relay_url().is_some()
}

pub fn web_url() -> Option<&'static str> {
    non_empty(WEB_URL)
}

pub fn relay_url() -> Option<&'static str> {
    non_empty(RELAY_URL)
}

pub fn app_url() -> Option<&'static str> {
    non_empty(APP_URL)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn the_three_urls_are_all_set_or_all_empty() {
        let set = [web_url(), relay_url(), app_url()].map(|u| u.is_some());
        assert!(set == [true; 3] || set == [false; 3], "{set:?}");
        assert_eq!(configured(), set[0]);
    }

    #[test]
    fn a_configured_cloud_has_a_websocket_relay_on_the_same_host() {
        let (Some(web), Some(relay), Some(app)) = (web_url(), relay_url(), app_url()) else {
            return;
        };
        assert!(web.ends_with('/') && app.ends_with('/'));
        let host = web.trim_start_matches("https://").trim_start_matches("http://");
        assert!(relay == format!("wss://{host}relay") || relay == format!("ws://{host}relay"), "{relay}");
    }

    // The product build must keep exactly the URLs it had as constants:
    // with no override in the build environment, hosted-cloud.env decides.
    #[test]
    fn without_an_override_the_defaults_file_decides() {
        if option_env!("SOLU_CLOUD_URL").is_some() || option_env!("SOLU_CLOUD_APP_URL").is_some() {
            return;
        }
        let path = concat!(env!("CARGO_MANIFEST_DIR"), "/hosted-cloud.env");
        let Ok(text) = std::fs::read_to_string(path) else {
            assert!(!configured(), "no defaults file and no override must mean no cloud");
            return;
        };
        let value = |key: &str| {
            text.lines()
                .find_map(|l| l.strip_prefix(key)?.strip_prefix('='))
                .map(str::trim)
                .unwrap_or_default()
                .to_string()
        };
        let origin = value("SOLU_CLOUD_URL");
        let host = origin.strip_prefix("https://").expect("product cloud is https");
        assert_eq!(web_url(), Some(format!("{origin}/").as_str()));
        assert_eq!(relay_url(), Some(format!("wss://{host}/relay").as_str()));
        assert_eq!(app_url(), Some(format!("{}/", value("SOLU_CLOUD_APP_URL")).as_str()));
    }
}
