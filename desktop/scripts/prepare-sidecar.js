#!/usr/bin/env node
// Stages solud binaries for Tauri bundling. Two outputs:
//
//   1. src-tauri/binaries/solud-{host-triple}{.exe}
//      The host-native sidecar via Tauri externalBin. Windows MSI
//      bundles solud.exe, Linux .deb/.AppImage bundles solud, etc.
//      Used when the Tauri app runs with backend=host-native.
//
//   2. src-tauri/resources/solud-linux   (Windows host only)
//      The Linux solud binary packaged as a plain resource (not an
//      executable sidecar from Tauri's POV — we run it inside WSL via
//      wsl.exe, not natively on Windows). Included so Windows users
//      with WSL can run the full-featured Linux daemon without a
//      separate install step.
//
// Called by npm scripts before `tauri dev` / `tauri build`. Does not
// build — only stages.
//
// Source:
//   - $REPO_ROOT/solud       → Linux binary (built via ./build.sh at
//                              repo root, or WSL-side go build)
//   - $REPO_ROOT/solud.exe   → Windows binary (cross-compiled from
//                              WSL, written to the Windows-side repo
//                              root per feedback_windows_build_command)

const { execSync } = require('node:child_process');
const fs = require('node:fs');
const path = require('node:path');

const IS_WINDOWS = process.platform === 'win32';

// `tauri build` (release) runs this with no flag; `tauri dev` passes --dev.
// The version-stamp assertion below runs only in release mode, so a local
// dev binary (main.version=dev) doesn't block `tauri dev`.
// SOLU_DEV_SIDECARS=1 lets `tauri build` package the unsigned "dev" sidecars
// from build-sidecar-binaries.sh --dev into a local-only installer; release
// builds never set it.
const DEV_SIDECARS = process.env.SOLU_DEV_SIDECARS === '1';
const RELEASE = !process.argv.includes('--dev') && !DEV_SIDECARS;
if (DEV_SIDECARS && !process.argv.includes('--dev')) {
  console.warn('warning: SOLU_DEV_SIDECARS=1 — skipping the release version-stamp check; this build is not publishable.');
}

// ELF magic for the bundled Linux daemon — first 4 bytes "\x7fELF".
const ELF_MAGIC = Buffer.from([0x7f, 0x45, 0x4c, 0x46]);
const GZIP_MAGIC = Buffer.from([0x1f, 0x8b]);

// Build-time completeness guard. Tauri only checks a bundled resource
// EXISTS, not that it's a real binary — so a 0-byte placeholder sails
// through `cargo tauri build` and ships an installer that strands every
// WSL-backend user with "bundled Linux solud is empty/not found" at
// runtime (daemon.rs). This was a real fleet incident. Assert here, the
// last staging step before the bundle, that each binary we hand Tauri is
// non-empty and (for solud-linux) actually an ELF. Hard-fail the build
// rather than ship a broken installer.
function assertRealBinary(p, label, magic) {
  let st;
  try {
    st = fs.statSync(p);
  } catch {
    console.error(`error: ${label} missing at ${p}.`);
    console.error('Run desktop/scripts/build-sidecar-binaries.sh (from WSL), copy both');
    console.error('binaries to the repo root, then re-run `cargo tauri build`.');
    process.exit(1);
  }
  if (st.size === 0) {
    console.error(`error: ${label} at ${p} is EMPTY (0 bytes) — a placeholder slipped into`);
    console.error('a Windows/NSIS build. The installer would ship WITHOUT a working daemon.');
    console.error('Stage the real binary (build-sidecar-binaries.sh) and rebuild.');
    process.exit(1);
  }
  if (magic) {
    const fd = fs.openSync(p, 'r');
    const head = Buffer.alloc(magic.length);
    fs.readSync(fd, head, 0, magic.length, 0);
    fs.closeSync(fd);
    if (!head.equals(magic)) {
      console.error(`error: ${label} at ${p} has unexpected magic ${head.toString('hex')}.`);
      console.error('Re-run build-sidecar-binaries.sh.');
      process.exit(1);
    }
  }
}
const scriptDir = __dirname;
const repoRoot = path.resolve(scriptDir, '..', '..');
const binariesDir = path.resolve(scriptDir, '..', 'src-tauri', 'binaries');
const resourcesDir = path.resolve(scriptDir, '..', 'src-tauri', 'resources');

// Release version stamp guard. The daemon's main.version defaults to "dev"
// and is only set to X.Y.Z via `go build -ldflags "-X main.version=…"`
// (desktop/scripts/build-sidecar-binaries.sh / publish.sh). A daemon that
// ships with version=dev disables auto-update forever for everyone who
// installs it (daemon/updater.go) AND mislabels itself in the UI — a real
// fleet incident. assertRealBinary only checks the binary is non-empty /
// has the right magic; it does NOT catch an unstamped dev build, so a plain
// `go build` output sails into the NSIS and ships. This guard closes that:
// in release mode, refuse to stage any sidecar whose bytes don't contain
// the tauri.conf.json version. The check runs here, on the build host, on
// the EXACT binary being bundled — it cannot be fooled by a WSL/Windows
// repo-root divergence the way the post-hoc installer validator can.
function releaseVersion() {
  try {
    const conf = JSON.parse(
      fs.readFileSync(path.join(scriptDir, '..', 'src-tauri', 'tauri.conf.json'), 'utf8'),
    );
    return conf.version;
  } catch (e) {
    console.error('error: could not read version from src-tauri/tauri.conf.json:', e.message);
    process.exit(1);
  }
}

function assertVersionStamp(p, label) {
  const expected = releaseVersion();
  if (!expected || !/^\d+\.\d+\.\d+$/.test(expected)) {
    console.error(
      `error: tauri.conf.json version is "${expected}", not X.Y.Z; refusing to stage an unversioned ${label}.`,
    );
    process.exit(1);
  }
  const buf = fs.readFileSync(p);
  const needle = Buffer.from(expected, 'utf8');
  if (!buf.includes(needle)) {
    console.error(`error: ${label} at ${p} does not contain the release version ${expected}.`);
    console.error('It is almost certainly an unstamped `dev` build (plain `go build` with no -ldflags);');
    console.error('such a daemon disables auto-update for every user who installs it. Rebuild the sidecar');
    console.error('with desktop/scripts/build-sidecar-binaries.sh (it bakes tauri.conf.json version in via');
    console.error('-ldflags), then re-run `cargo tauri build`.');
    process.exit(1);
  }
  console.log(`✓ ${label} carries release version ${expected}`);
}

// 1. Stage host-native binary for Tauri externalBin.
const hostSrcName = IS_WINDOWS ? 'solud.exe' : 'solud';
const hostSrc = path.join(repoRoot, hostSrcName);

let triple;
try {
  const out = execSync('rustc -vV', { encoding: 'utf8' });
  const m = out.match(/^host:\s*(\S+)/m);
  if (!m) throw new Error('no "host:" line in rustc -vV output');
  triple = m[1];
} catch (e) {
  console.error('error: could not determine rustc host target triple:', e.message);
  console.error('is rustup + a toolchain installed?');
  process.exit(1);
}

fs.mkdirSync(binariesDir, { recursive: true });
const hostDstName = `solud-${triple}${IS_WINDOWS ? '.exe' : ''}`;
const hostDst = path.join(binariesDir, hostDstName);

if (fs.existsSync(hostSrc)) {
  // Dev path: repo-root has a freshly-built binary; copy it in.
  fs.copyFileSync(hostSrc, hostDst);
  if (!IS_WINDOWS) fs.chmodSync(hostDst, 0o755);
  console.log(`→ host sidecar ready: ${hostDst} (from ${hostSrc})`);
} else if (fs.existsSync(hostDst)) {
  // CI path: the workflow already placed the cross-compiled binary
  // into binaries/ (it downloaded the artifact from the daemon job).
  // Nothing to stage — don't fail.
  console.log(`→ host sidecar already staged: ${hostDst} (repo-root source missing; assuming CI)`);
} else {
  console.error(`error: neither ${hostSrc} nor ${hostDst} found.`);
  console.error(IS_WINDOWS
    ? 'From WSL, run desktop/scripts/build-sidecar-binaries.sh — it bakes the\n' +
      'version from tauri.conf.json into both binaries via -ldflags so the\n' +
      'auto-updater can compare against the release manifest. A plain\n' +
      '`go build` produces version=dev binaries that silently disable\n' +
      'auto-update for any user who installs them (daemon/updater.go:189).'
    : 'Run ./build.sh at repo root first so ./solud exists.');
  process.exit(1);
}

// Release gate: whatever path produced hostDst (fresh copy OR a CI-staged
// binary), assert it actually carries this release's version stamp before
// we let Tauri bundle it.
if (RELEASE) assertVersionStamp(hostDst, `host sidecar (${hostDstName})`);

// 2. Stage the Linux binary for the WSL backend (Windows) OR a 0-byte
// placeholder (Linux/macOS). Tauri's bundle.resources entry is declared
// in the shared tauri.conf.json, so the file MUST exist on every host or
// the build errors. On non-Windows hosts this placeholder is never
// accessed at runtime (ensure_daemon_running_wsl is cfg(windows)-only).
fs.mkdirSync(resourcesDir, { recursive: true });
// Ship the Linux daemon GZIP-COMPRESSED. Windows Defender ML false-flags a
// raw Go ELF dropped on an NTFS path and silently quarantines it (ThreatID
// 2147764476), which broke the WSL backend for every Windows user. Defender
// does not FP a .gz, and start-wsl.sh decompresses it into WSL ext4 — where
// Defender never scans. See ensure_daemon_running_wsl / write_wsl_start_script.
// Ship the Linux daemon for BOTH arches, gzip-compressed (one per arch).
// Defender ML-FPs a raw Go ELF on NTFS and quarantines it; .gz dodges that,
// and start-wsl.sh picks the arch matching the WSL distro (uname -m) and
// decompresses into WSL ext4. ARM64 Windows (Snapdragon) runs aarch64 WSL —
// an amd64 daemon there dies with "Exec format error" — so both MUST ship.
const LINUX_ARCHES = [
  { src: 'solud-linux', dst: 'solud-linux-amd64.gz' },
  { src: 'solud-linux-arm64', dst: 'solud-linux-arm64.gz' },
];
// Drop stale raw ELF / old single-arch .gz a previous build may have left.
for (const stale of ['solud-linux', 'solud-linux.gz']) {
  try { fs.rmSync(path.join(resourcesDir, stale), { force: true }); } catch { /* ignore */ }
}
const linuxDsts = [];
for (const a of LINUX_ARCHES) {
  const dst = path.join(resourcesDir, a.dst);
  linuxDsts.push(dst);
  if (IS_WINDOWS) {
    const src = path.join(repoRoot, a.src);
    if (!fs.existsSync(src)) {
      console.error(`error: ${src} not found.`);
      console.error('From WSL, run desktop/scripts/build-sidecar-binaries.sh — it');
      console.error('cross-compiles solud.exe + solud-linux (amd64) + solud-linux-arm64');
      console.error('with the version from tauri.conf.json baked in.');
      process.exit(1);
    }
    // Assert the raw ELF carries the version BEFORE gzipping (the .gz won't
    // match a plain substring search). Same dev-build guard as the host sidecar.
    if (RELEASE) assertVersionStamp(src, `WSL Linux daemon (${a.src})`);
    const zlib = require('node:zlib');
    fs.writeFileSync(dst, zlib.gzipSync(fs.readFileSync(src)));
    console.log(`→ wsl resource ready: ${dst} (gzip of ${src})`);
  } else {
    fs.writeFileSync(dst, '');
    console.log(`→ wsl resource placeholder: ${dst} (non-Windows host; not used)`);
  }
}

// ── Completeness guard (Windows/NSIS build only) ──────────────────────
// The NSIS installer bundles BOTH solud.exe (externalBin) and solud-linux
// (the WSL daemon resource). On a Windows host that's the real bundle
// target, so refuse to proceed unless both are genuine binaries. On
// non-Windows hosts the deb/appimage/dmg targets legitimately ship the
// 0-byte solud-linux placeholder (the WSL path is cfg(windows)-only and
// never reads it), so we skip the check there.
if (IS_WINDOWS) {
  assertRealBinary(hostDst, 'host sidecar (solud.exe)', null);
  for (const dst of linuxDsts) {
    assertRealBinary(dst, `WSL Linux daemon (${path.basename(dst)})`, GZIP_MAGIC);
  }
  console.log('✓ sidecar completeness guard passed (solud.exe + amd64/arm64 solud-linux .gz)');
}
