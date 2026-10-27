//! A shell upgrade must respect the existing WSL daemon's update admission.
//! Never force an update, kill the listener, or send credentials to redirects.
use std::io::Read;
use std::time::Duration;

#[derive(Debug, PartialEq, Eq)]
pub(super) enum UpgradeOutcome {
    Applying,
    Deferred,
}

pub(super) fn request_guarded_upgrade(
    addr: &str,
    token: Option<&str>,
) -> Result<UpgradeOutcome, String> {
    let addr: std::net::SocketAddr = addr.parse().map_err(|_| "invalid WSL update address")?;
    let client = reqwest::blocking::Client::builder()
        .no_proxy()
        .redirect(reqwest::redirect::Policy::none())
        .retry(reqwest::retry::never())
        .connect_timeout(Duration::from_secs(2))
        .timeout(Duration::from_secs(60))
        .build()
        .map_err(|_| "create WSL update client")?;
    let mut request = client.post(format!("http://{addr}/api/update/apply"));
    if let Some(token) = token.filter(|value| !value.is_empty()) {
        let mut header = reqwest::header::HeaderValue::from_str(&format!("Bearer {token}"))
            .map_err(|_| "invalid WSL update authorization")?;
        header.set_sensitive(true);
        request = request.header(reqwest::header::AUTHORIZATION, header);
    }
    let response = request
        .send()
        .map_err(|_| format!("request guarded WSL update at {addr} failed or timed out"))?;
    let status = response.status().as_u16();
    let mut body = Vec::new();
    response
        .take(16385)
        .read_to_end(&mut body)
        .map_err(|_| "read guarded WSL update response")?;
    if body.len() > 16384 {
        return Err("guarded WSL update response too large".into());
    }
    let result: serde_json::Value = serde_json::from_slice(&body)
        .map_err(|_| format!("invalid guarded WSL update response (HTTP {status})"))?;
    match status {
        200 if result.get("applying").and_then(|v| v.as_bool()) == Some(true) => {
            Ok(UpgradeOutcome::Applying)
        }
        409 if (result.get("busy").and_then(|v| v.as_bool()) == Some(true)
            && matches!(
                result.get("cause").and_then(|v| v.as_str()),
                Some("workflow" | "focus" | "restart")
            ))
            || result.get("deferred").and_then(|v| v.as_bool()) == Some(true) =>
        {
            Ok(UpgradeOutcome::Deferred)
        }
        _ => Err(format!(
            "guarded WSL update refused at {addr} (HTTP {status})"
        )),
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::io::{BufRead, BufReader, Write};
    use std::net::TcpListener;

    fn serve(status: u16, body: &'static str) -> (String, std::thread::JoinHandle<String>) {
        let listener = TcpListener::bind("127.0.0.1:0").unwrap();
        let addr = listener.local_addr().unwrap().to_string();
        let task = std::thread::spawn(move || {
            let (mut socket, _) = listener.accept().unwrap();
            socket
                .set_read_timeout(Some(Duration::from_secs(3)))
                .unwrap();
            let mut reader = BufReader::new(socket.try_clone().unwrap());
            let mut request = String::new();
            loop {
                let mut line = String::new();
                reader.read_line(&mut line).unwrap();
                request.push_str(&line);
                if line == "\r\n" || line.is_empty() {
                    break;
                }
            }
            write!(
                socket,
                "HTTP/1.1 {status} Test\r\nContent-Length: {}\r\nConnection: close\r\n\r\n{body}",
                body.len()
            )
            .unwrap();
            request
        });
        (addr, task)
    }

    #[test]
    fn upgrade_uses_authenticated_normal_apply_without_force() {
        let (addr, server) = serve(200, r#"{"applying":true}"#);
        assert_eq!(
            request_guarded_upgrade(&addr, Some("owner-secret")).unwrap(),
            UpgradeOutcome::Applying
        );
        let request = server.join().unwrap().to_ascii_lowercase();
        assert!(request.starts_with("post /api/update/apply http/1.1\r\n"));
        assert!(request.contains("authorization: bearer owner-secret\r\n"));
        assert!(!request.contains("force"));
    }

    #[test]
    fn workflow_focus_and_restart_holds_preserve_the_backend() {
        for body in [
            r#"{"busy":true,"cause":"workflow","workflowInflight":3}"#,
            r#"{"busy":true,"cause":"focus"}"#,
            r#"{"busy":true,"cause":"restart"}"#,
            r#"{"deferred":true,"nextRetryMs":9999999999999}"#,
        ] {
            let (addr, server) = serve(409, body);
            assert_eq!(
                request_guarded_upgrade(&addr, None).unwrap(),
                UpgradeOutcome::Deferred
            );
            server.join().unwrap();
        }
    }

    #[test]
    fn unknown_holds_auth_failures_and_invalid_replies_fail_closed() {
        for (status, body) in [
            (409, r#"{"code":"update_held"}"#),
            (409, r#"{"busy":true,"cause":"unknown"}"#),
            (401, r#"{"error":"owner-secret"}"#),
            (500, r#"{"applying":true}"#),
            (200, r#"{"applying":false}"#),
            (200, "not-json"),
            (302, r#"{"applying":true}"#),
        ] {
            let (addr, server) = serve(status, body);
            let error = request_guarded_upgrade(&addr, Some("owner-secret")).unwrap_err();
            assert!(!error.contains("owner-secret"));
            server.join().unwrap();
        }
    }
}
