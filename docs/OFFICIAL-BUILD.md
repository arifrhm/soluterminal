# The official build

The official SoluTerminal app is built from this repository by GitHub Actions,
on GitHub-hosted runners, by `.github/workflows/official-build.yml`. Nothing
else goes into it: no private source, no secrets, no files from a maintainer's
machine. CI builds and fingerprints the files but never signs anything.
Signing the update manifest is a separate step, described below, that checks
those fingerprints first.

## When it runs

- **A pushed tag `vX.Y.Z`.** The tag must equal the `version` in
  `desktop/src-tauri/tauri.conf.json`, or the build stops. Only tag builds are
  candidates for a release.
- **By hand** (Actions › official-build › Run workflow) for a dry run of any
  branch. `allow_incomplete` lets a dry run finish when an official input is
  missing (see Preflight). Its artifact is then named
  `…-INCOMPLETE-not-official` and is never attested. `attest` records an
  attestation for a complete manual run; tag builds always record one.

## What it builds

| Job | Runner | Output |
|---|---|---|
| preflight | ubuntu-24.04 | Checks the commit can build the official app |
| daemons (primary) | ubuntu-24.04 | Web bundle, then `solud` for linux-amd64, linux-arm64, darwin-amd64, darwin-arm64, windows-amd64 |
| daemons (rebuild) | ubuntu-24.04 | The same, on a separate runner |
| desktop | windows-latest | The desktop app and its NSIS installer `soluterminal_X.Y.Z_x64-setup.exe` |
| release-files | ubuntu-24.04 | Compares the two daemon builds, writes `SHA256SUMS`, uploads, attests |

The daemons are built by `scripts/official-build.sh daemons`, using the same
flags as every release (`solu_daemon_ldflags` in `scripts/release-ldflags.sh`):
`CGO_ENABLED=0`, `-trimpath`, `-buildvcs=false`, the version, the pane runtime
digest, the source commit with `buildDirty=0`, `buildChannel=release`, the
update manifest public key with signed manifests required, the public auth
defaults, an empty Go build ID and `-B` derived from the version and commit.
The Go toolchain is the `toolchain` line in `daemon/go.mod` and Node is pinned
in the workflow. The release-files job fails unless both daemon builds have the
same SHA-256 on every platform.

The desktop job does not rebuild the daemons. It downloads the primary build,
checks it against its `SHA256SUMS`, and stages the Windows and both Linux
daemons as the installer's sidecars (`scripts/official-build.sh
stage-sidecars`). It then builds with `desktop/scripts/tauri-release-build.cmd`,
the same wrapper as the product build, which remaps the runner's folders out of
the binary. Afterwards it checks the installer twice:

- `scripts/validate-shell-installer.sh --extract-check` checks the version, the
  web bundle source, the auth defaults, the update key and that no
  build-machine paths remain;
- `scripts/official-build.sh check-installer` unpacks the installer and
  requires the daemons inside to be byte for byte the ones built and
  fingerprinted, and the app to name the hosted cloud relay.

## Settings

| Setting | Where | Value |
|---|---|---|
| Hosted cloud | source: `desktop/src-tauri/hosted-cloud.env` (shell) and `daemon/hosted_cloud_defaults.go` (daemon) | The product's cloud: its API and relay origin, and `https://app.soluterminal.com` for its pages. To build against another cloud, set `SOLU_CLOUD_URL` and `SOLU_CLOUD_APP_URL` in the workflow `env`; the shell then reads only those (`build.rs`) and the daemons get them as `-X main.defaultCloudHTTPBase` / `-X main.publicAppURL` |
| `SOLU_SUPABASE_URL`, `SOLU_SUPABASE_ANON_KEY` | repository **variables** (Settings › Secrets and variables › Actions › Variables) | The hosted accounts service's URL and anon key |
| `NODE_VERSION`, `TAURI_CLI_VERSION` | workflow `env` | Node for the web bundle; the Tauri CLI that `desktop/package-lock.json` pins |

The Supabase URL and anon key are public by design: every released daemon
contains both, and an anon key is meant to be shipped to browsers. They are
variables rather than source so the project can change them without a commit.
They are not secrets, and the workflow uses no secrets at all. The update
manifest's private signing key is never given to CI.

## Preflight

`scripts/official-build.sh preflight` stops the build, with the exact missing
item, unless the commit has:

- a release version (`X.Y.Z`) that matches the tag;
- both public auth variables;
- a hosted cloud: `desktop/src-tauri/hosted-cloud.env` and
  `daemon/hosted_cloud_defaults.go` naming the same origin (or, with
  `SOLU_CLOUD_URL` set, `var defaultCloudHTTPBase string` and
  `var publicAppURL string` in the daemon for `-X`, which silently ignores
  a missing name). Each daemon is checked for the values afterwards;
- that cloud in the desktop shell's allowlists: the CSP in `tauri.conf.json`
  and `remote.urls` in each capability that has one (`build.rs` only warns
  about this; the cloud's pages would load without the shell's plugins);
- voice dictation: the `soluvoice` dependency in `web/package.json` and its
  package, `web/src/private/voice.register.tsx`, `daemon/voice.go` and
  `shared/voiceclean`.

## Files it produces

The `soluterminal-X.Y.Z-official` artifact (kept 90 days) holds:

- `solud-linux-amd64`, `solud-linux-arm64`, `solud-darwin-amd64`,
  `solud-darwin-arm64`, `solud-windows-amd64.exe`;
- `soluterminal_X.Y.Z_x64-setup.exe`;
- `SHA256SUMS` over those six files, the subjects of the GitHub build
  provenance attestation;
- `BUILDINFO.txt` (toolchains, web bundle digest and every `-X` value except the
  anon key), `BUILDINFO-desktop.txt` (Rust, Tauri CLI, runner image) and
  `SOURCE.txt` (repository, ref, commit, workflow, run).

## Verifying a build and signing the manifest

The maintainers sign the update manifest on their own machine, outside this
repository. That step only signs files whose fingerprints it has checked:

1. **Find and download the tag's run.**

   ```sh
   gh run list -R soluterminal/soluterminal --workflow official-build.yml --branch vX.Y.Z
   gh run download <run-id> -R soluterminal/soluterminal \
     --name soluterminal-X.Y.Z-official --dir official
   ```

2. **Check the files and where they came from.** `SOURCE.txt` must name
   `refs/tags/vX.Y.Z`, `complete true`, and the commit the tag points to
   (`git ls-remote https://github.com/soluterminal/soluterminal 'refs/tags/vX.Y.Z*'`).

   ```sh
   cd official && sha256sum -c --strict SHA256SUMS
   for f in $(awk '{print $2}' SHA256SUMS); do
     gh attestation verify "$f" -R soluterminal/soluterminal \
       --signer-workflow soluterminal/soluterminal/.github/workflows/official-build.yml \
       --source-ref refs/tags/vX.Y.Z --source-digest <tag commit> \
       --deny-self-hosted-runners
   done
   ```

3. **Optionally rebuild the daemons yourself** from a clean clone of the tag,
   with the same auth variables, Node and Go, and compare:

   ```sh
   SOLU_SUPABASE_URL=… SOLU_SUPABASE_ANON_KEY=… \
     scripts/official-build.sh daemons --out /tmp/solud-check
   diff <(grep solud- official/SHA256SUMS) /tmp/solud-check/SHA256SUMS
   ```

4. **Sign.** Build `update.json` from the verified `SHA256SUMS`: the version,
   each daemon's URL and SHA-256 and the installer's `shell` block. Sign it
   with the update key, which stays offline, and publish the files exactly as
   downloaded, with `update.json` and `update.json.sig`. Every official daemon
   and shell embeds the matching public key and refuses an unsigned or
   wrongly signed manifest (see `docs/UPDATE_SIGNING.md`).

A digest mismatch, a failed attestation or a run that is not the tag's stops
the release. Never re-sign files that were rebuilt or changed after
verification.

## Limits

- The desktop app and installer are built once; only the daemons are
  compared across two builds. The installer's
  daemons are checked against those builds, and the installer itself is covered
  by the attestation.
- Windows code signing (Authenticode) of the installer is not part of this
  workflow yet.
