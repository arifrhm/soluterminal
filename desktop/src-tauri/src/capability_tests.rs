// Capability scoping, checked through Tauri's own ACL with the real
// capabilities/*.json. `remote.urls` must include `http://127.0.0.1:*`, so
// what keeps a local dev server shown in a browser pane (a child webview of
// the app's window) from calling the desktop commands is that capabilities
// name the app's webviews, not its windows: Tauri grants a command when
// either the window label or the webview label matches.

use tauri::ipc::{CallbackFn, InvokeBody};
use tauri::test::{get_ipc_response, mock_builder, INVOKE_KEY};
use tauri::webview::InvokeRequest;
use tauri::{LogicalPosition, LogicalSize, WebviewBuilder, WebviewUrl, WebviewWindowBuilder};

struct AsWebview<'a>(&'a tauri::Webview<tauri::test::MockRuntime>);
impl AsRef<tauri::Webview<tauri::test::MockRuntime>> for AsWebview<'_> {
    fn as_ref(&self) -> &tauri::Webview<tauri::test::MockRuntime> {
        self.0
    }
}

fn ask(webview: &tauri::Webview<tauri::test::MockRuntime>, origin: &str) -> bool {
    get_ipc_response(
        &AsWebview(webview),
        InvokeRequest {
            cmd: "plugin:app|version".into(),
            callback: CallbackFn(0),
            error: CallbackFn(1),
            url: origin.parse().unwrap(),
            body: InvokeBody::default(),
            headers: Default::default(),
            invoke_key: INVOKE_KEY.to_string(),
        },
    )
    .is_ok()
}

// One expansion of generate_context!() for the tests (run() has the other).
fn app() -> tauri::App<tauri::test::MockRuntime> {
    mock_builder().build(tauri::generate_context!()).unwrap()
}

#[test]
fn browser_pane_webviews_get_no_desktop_commands() {
    let app = app();
    let daemon = "http://127.0.0.1:8787/";
    let dev_server = "http://127.0.0.1:3000/";
    for window_label in ["main", "workspace_2", "browser_1"] {
        let window = WebviewWindowBuilder::new(
            &app,
            window_label,
            WebviewUrl::External(daemon.parse().unwrap()),
        )
        .build()
        .unwrap();
        assert!(
            ask(window.as_ref(), daemon),
            "{window_label}: the app's own page lost its commands"
        );

        let pane = window
            .as_ref()
            .window()
            .add_child(
                WebviewBuilder::new(
                    format!("bpane_{window_label}"),
                    WebviewUrl::External(dev_server.parse().unwrap()),
                ),
                LogicalPosition::new(0.0, 0.0),
                LogicalSize::new(100.0, 100.0),
            )
            .unwrap();
        assert!(
            !ask(&pane, dev_server),
            "{window_label}: a browser-pane page reached the desktop commands"
        );
    }
}

#[test]
fn a_direct_popout_window_has_no_commands() {
    let app = app();
    let dev_server = "http://127.0.0.1:3000/";
    let window = WebviewWindowBuilder::new(
        &app,
        "directbrowser_1",
        WebviewUrl::External(dev_server.parse().unwrap()),
    )
    .build()
    .unwrap();
    assert!(!ask(window.as_ref(), dev_server));
}
