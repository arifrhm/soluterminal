//! Bounded HTTP health probes shared by desktop startup and supervision.
//! New daemons answer the identity query without fleet inventory. Older
//! daemons ignore it and can still return a large, chunked full-health body,
//! so the client must continue to decode framing before parsing the JSON.

use std::io::Read;
use std::net::SocketAddr;
use std::sync::OnceLock;
use std::time::Duration;

const MAX_BODY: u64 = 256 * 1024;
const TIMEOUT: Duration = Duration::from_millis(800);

pub(super) fn probe(addr: &str, token: Option<&str>) -> Result<serde_json::Value, String> {
    probe_with_timeout(addr, token, TIMEOUT)
}

/// `probe` with a caller-chosen overall budget. The 800ms default suits the
/// local host-native daemon; decisions that END a daemon's life must not be
/// made on it. A loaded WSL daemon has measured 2–6s on /api/health, so an
/// 800ms verdict of "unreachable" there was how a healthy daemon got killed.
pub(super) fn probe_with_timeout(
    addr: &str,
    token: Option<&str>,
    timeout: Duration,
) -> Result<serde_json::Value, String> {
    let addr: SocketAddr = addr
        .parse()
        .map_err(|_| "invalid daemon address".to_owned())?;
    static CLIENT: OnceLock<Result<reqwest::blocking::Client, reqwest::Error>> = OnceLock::new();
    let client = CLIENT
        .get_or_init(|| {
            reqwest::blocking::Client::builder()
                .http1_only()
                .no_proxy()
                .redirect(reqwest::redirect::Policy::none())
                .retry(reqwest::retry::never())
                .connect_timeout(Duration::from_millis(500))
                .timeout(TIMEOUT)
                .build()
        })
        .as_ref()
        .map_err(|_| format!("create daemon health client {addr}"))?;
    let mut request = client
        .get(format!("http://{addr}/api/health?identity=1"))
        .timeout(timeout)
        .header(reqwest::header::CONNECTION, "close");
    if let Some(token) = token.filter(|token| !token.is_empty()) {
        let mut header = reqwest::header::HeaderValue::from_str(&format!("Bearer {token}"))
            .map_err(|_| format!("invalid daemon health authorization {addr}"))?;
        header.set_sensitive(true);
        request = request.header(reqwest::header::AUTHORIZATION, header);
    }
    // Do not include library error strings: redirects/response data may contain
    // credentials. The phase and address are sufficient for launch diagnostics.
    let response = request
        .send()
        .map_err(|_| format!("request daemon health {addr} failed or timed out"))?;
    if response.status() != reqwest::StatusCode::OK {
        return Err(format!(
            "daemon {addr} health returned HTTP {}",
            response.status().as_u16()
        ));
    }
    if response
        .content_length()
        .is_some_and(|length| length > MAX_BODY)
    {
        return Err(format!(
            "daemon {addr} health body exceeds {MAX_BODY} bytes"
        ));
    }
    let mut body = Vec::new();
    response
        .take(MAX_BODY + 1)
        .read_to_end(&mut body)
        .map_err(|_| format!("read daemon health body {addr} failed or timed out"))?;
    if body.len() as u64 > MAX_BODY {
        return Err(format!(
            "daemon {addr} health body exceeds {MAX_BODY} bytes"
        ));
    }
    serde_json::from_slice(&body)
        .map_err(|_| format!("parse daemon health JSON {addr}: invalid JSON"))
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::io::{BufRead, BufReader, Write};
    use std::net::TcpListener;
    use std::thread;
    use std::time::Instant;

    fn serve(
        response: Vec<u8>,
        response_delay: Duration,
        linger: Duration,
    ) -> (String, thread::JoinHandle<String>) {
        let listener = TcpListener::bind("127.0.0.1:0").unwrap();
        let addr = listener.local_addr().unwrap().to_string();
        let server = thread::spawn(move || {
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
            thread::sleep(response_delay);
            for part in response.chunks(127) {
                if socket.write_all(part).is_err() {
                    break;
                }
            }
            thread::sleep(linger);
            request
        });
        (addr, server)
    }

    fn fixture() -> Vec<u8> {
        serde_json::to_vec(&serde_json::json!({
            "status": "ok", "backendID": "wsl", "version": "0.0.1077", "panes": 42,
            "fleet": { "components": (0..42).map(|i| serde_json::json!({
                "paneId": format!("pane_{i}"), "role": "pane-agent", "pid": 1000 + i,
                "paneTitle": "A persistent orchestrator terminal session working on a repository", "version": "0.0.1059",
                "stale": true, "restartable": true, "busy": false,
                "reason": "executing a replaced/deleted binary inode",
                "executable": "/home/user/.local/share/soluterminal/solud.old.2 (deleted)"
            })).collect::<Vec<_>>() }
        }))
        .unwrap()
    }

    fn chunked(body: &[u8]) -> Vec<u8> {
        let mut response = b"HTTP/1.1 200 OK\r\nTransfer-Encoding: chunked\r\n\r\n".to_vec();
        for chunk in body.chunks(773) {
            response.extend_from_slice(format!("{:x}\r\n", chunk.len()).as_bytes());
            response.extend_from_slice(chunk);
            response.extend_from_slice(b"\r\n");
        }
        response.extend_from_slice(b"0\r\n\r\n");
        response
    }

    #[test]
    fn reads_large_fleet_with_all_http_framings_and_preserves_authorization() {
        let body = fixture();
        assert!(body.len() > 13 * 1024);
        let mut length =
            format!("HTTP/1.1 200 OK\r\nContent-Length: {}\r\n\r\n", body.len()).into_bytes();
        length.extend_from_slice(&body);
        let mut close = b"HTTP/1.0 200 OK\r\n\r\n".to_vec();
        close.extend_from_slice(&body);
        for response in [length, close, chunked(&body)] {
            let (addr, server) = serve(response, Duration::ZERO, Duration::ZERO);
            let actual = probe(&addr, Some("test-secret")).unwrap();
            assert_eq!(actual["backendID"], "wsl");
            assert_eq!(actual["panes"], 42);
            assert_eq!(actual["fleet"]["components"].as_array().unwrap().len(), 42);
            let request = server.join().unwrap().to_lowercase();
            assert!(request.starts_with("get /api/health?identity=1 http/1.1\r\n"));
            assert!(request.contains("authorization: bearer test-secret\r\n"));
            assert!(request.contains("connection: close\r\n"));
        }
    }

    #[test]
    fn accepts_authenticated_identity_after_one_and_a_half_seconds() {
        let body = serde_json::to_vec(&serde_json::json!({
            "status": "ok", "backendID": "wsl", "version": "0.0.1137", "pid": 42
        }))
        .unwrap();
        let mut response =
            format!("HTTP/1.1 200 OK\r\nContent-Length: {}\r\n\r\n", body.len()).into_bytes();
        response.extend_from_slice(&body);
        let (addr, server) = serve(response, Duration::from_millis(1500), Duration::ZERO);
        let start = Instant::now();
        let actual =
            probe_with_timeout(&addr, Some("delayed-secret"), Duration::from_secs(3)).unwrap();
        assert!(start.elapsed() >= Duration::from_millis(1400));
        assert_eq!(actual["backendID"], "wsl");
        let request = server.join().unwrap().to_lowercase();
        assert!(request.contains("authorization: bearer delayed-secret\r\n"));
    }

    #[test]
    fn rejects_bad_status_framing_json_and_oversize_without_leaking_tokens() {
        let cases = [
            b"HTTP/1.1 401 Unauthorized\r\nContent-Length: 0\r\n\r\n".to_vec(),
            b"HTTP/1.1 302 Found\r\nLocation: http://127.0.0.1:1/test-secret\r\nContent-Length: 0\r\n\r\n".to_vec(),
            b"HTTP/1.1 200 OK\r\nContent-Length: 7\r\n\r\ninvalid".to_vec(),
            b"HTTP/1.1 200 OK\r\nContent-Length: 100\r\n\r\n{}".to_vec(),
            b"HTTP/1.1 200 OK\r\nTransfer-Encoding: chunked\r\n\r\n2\r\n{}\r\n".to_vec(),
            b"HTTP/1.1 200 OK\r\nTransfer-Encoding: chunked\r\n\r\nZZ\r\n{}".to_vec(),
            b"not HTTP\r\n\r\ntest-secret".to_vec(),
            format!("HTTP/1.1 200 OK\r\nContent-Length: {}\r\n\r\n", MAX_BODY + 1).into_bytes(),
            chunked(&vec![b' '; MAX_BODY as usize + 1]),
        ];
        for response in cases {
            let (addr, server) = serve(response, Duration::ZERO, Duration::ZERO);
            let err = probe(&addr, Some("test-secret")).unwrap_err();
            assert!(!err.contains("test-secret"), "{err}");
            server.join().unwrap();
        }
    }

    #[test]
    fn omits_empty_authorization_and_bounds_stalled_body() {
        for token in [None, Some("")] {
            let (addr, server) = serve(
                b"HTTP/1.1 200 OK\r\nContent-Length: 2\r\n\r\n{}".to_vec(),
                Duration::ZERO,
                Duration::ZERO,
            );
            probe(&addr, token).unwrap();
            assert!(!server
                .join()
                .unwrap()
                .to_lowercase()
                .contains("authorization:"));
        }
        let linger = TIMEOUT + Duration::from_millis(500);
        let (addr, server) = serve(
            b"HTTP/1.1 200 OK\r\nContent-Length: 100\r\n\r\n{".to_vec(),
            Duration::ZERO,
            linger,
        );
        let start = Instant::now();
        assert!(probe(&addr, None)
            .unwrap_err()
            .contains("read daemon health body"));
        let elapsed = start.elapsed();
        assert!(
            elapsed >= TIMEOUT - Duration::from_millis(500),
            "{elapsed:?}"
        );
        // Below the server's close: an unbounded read would only fail at EOF.
        assert!(elapsed < linger, "{elapsed:?}");
        server.join().unwrap();
    }

    #[test]
    fn bounds_a_connected_server_that_never_answers() {
        // The server stays silent past the upper bound, so only the client's
        // own budget can end the request inside the asserted window.
        let (addr, server) = serve(Vec::new(), Duration::from_millis(4500), Duration::ZERO);
        let start = Instant::now();
        let err = probe_with_timeout(&addr, Some("stall-secret"), Duration::from_secs(3))
            .unwrap_err();
        let elapsed = start.elapsed();
        assert!(err.contains("failed or timed out"), "{err}");
        assert!(!err.contains("stall-secret"), "{err}");
        assert!(elapsed >= Duration::from_millis(2500), "{elapsed:?}");
        assert!(elapsed < Duration::from_millis(3750), "{elapsed:?}");
        server.join().unwrap();
    }
}
