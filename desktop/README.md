# SoluTerminal desktop shell

The Windows desktop app's shell. To install the released Windows app instead,
follow the [installation help](https://soluterminal.com/help/).

The Tauri shell manages the native window, tray, backend selection and daemon
supervision. The daemon serves the UI and owns terminal sessions. Closing the
window hides it to the tray; Quit exits the shell and its owned supervision.

## Source map

- [Startup and tray](src-tauri/src/lib.rs).
- [Windows and WSL backend selection](src-tauri/src/backend.rs).
- [Daemon supervision](src-tauri/src/daemon.rs).
- [Tauri configuration](src-tauri/tauri.conf.json).
- [Sidecar staging](scripts/prepare-sidecar.js).

Windows packaging requires a native host daemon and compressed Linux amd64 and
arm64 WSL resources. Source bundle targets for other operating systems do not
establish public support for those platforms.

## Building the shell

The shell compiles and passes its unit tests on its own (checked on every
change). Tauri's build script requires its bundle inputs to exist, so for a
compile or test run, empty placeholders are enough:

```sh
cd desktop/src-tauri
mkdir -p binaries resources
: > "binaries/solud-$(rustc -vV | sed -n 's/^host: //p')"
: > resources/solud-linux-amd64.gz
: > resources/solud-linux-arm64.gz
cargo test --lib
```

A runnable app needs real daemon sidecars.
[`build-sidecar-binaries.sh --dev`](scripts/build-sidecar-binaries.sh) builds
unsigned development sidecars from `daemon/`. They need no signing key or
release tooling and report version `dev`, which turns the daemon's auto-updater
off. `cargo tauri dev` stages them; `SOLU_DEV_SIDECARS=1 cargo tauri build`
packages them into a local installer that must not be distributed. These two
steps haven't yet been verified from a public checkout; please open an issue if
they fail.

## Hosted cloud

This build connects to no hosted cloud or relay. The daemon starts without
`--relay` (so its phone pairing reports that the cloud is not configured),
and the shell always runs on this computer and refuses to switch to cloud
hosting. To build
against a cloud you run, set `SOLU_CLOUD_URL` (an origin such as
`https://cloud.example.com`; the relay is `wss://<host>/relay`) and,
optionally, `SOLU_CLOUD_APP_URL` (the address opened in a browser for pairing)
when compiling. Also add the origin to `remote.urls` in
`src-tauri/capabilities/*.json` and to the CSP in `src-tauri/tauri.conf.json`;
the build warns when they are missing.

See [update verification](../docs/UPDATE_SIGNING.md). Signed update manifests,
Windows publisher signing and reproducible builds are separate properties;
their presence must be checked against the actual artifact being distributed.
