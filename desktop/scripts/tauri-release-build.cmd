@echo off
rem Release build of the desktop app and its NSIS installer, run on the Windows
rem build host from the desktop folder (see docs/SHELL-INSTALLER-BUILD.md).
rem The remaps keep the builder's user folder and checkout path out of the
rem binary (panic locations otherwise embed them); validate-shell-installer.sh
rem --extract-check refuses an installer that still carries them.
setlocal
pushd "%~dp0.."
set "SRC=%CD%"
popd
rem rustc applies the last matching remap, so the most specific one goes last.
set "RUSTFLAGS=--remap-path-prefix=%USERPROFILE%=~ --remap-path-prefix=%SRC%=src --remap-path-prefix=%USERPROFILE%\.cargo\registry\src=crates"
pushd "%SRC%"
cargo tauri build %*
set "RC=%ERRORLEVEL%"
popd
exit /b %RC%
