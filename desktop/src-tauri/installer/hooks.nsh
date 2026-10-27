; =============================================================================
; soluterminal NSIS installer hooks
; -----------------------------------------------------------------------------
; Wired via tauri.conf.json -> bundle.windows.nsis.installerHooks.
; Tauri's NSIS template (nsis-tauri-utils) supports a set of empty hook macros
; that it invokes at well-known points.
;
; NSIS_HOOK_PREINSTALL runs install-time pre-flight checks BEFORE any files are
; copied:
;   0) Daemon monotonicity  -> resolve installed version from marker, then
;                               embedded Go build info; unknown fails closed.
;                               Exit 65 = older bundled daemon, 66 = downgrade
;                               token not consumed, 67 = installed unknown.
;   1) Architecture gate    -> abort on non-amd64 (we ship 64-bit binaries only).
;   2) WebView2 presence     -> Tauri's window is a WebView2 surface; with no
;                               Evergreen runtime the window paints blank, so we
;                               offer to open the bootstrapper download.
;   3) Running-process kill  -> a live soluterminal.exe / solud.exe locks its own
;                               files (see the kill macro below for the full
;                               why); we terminate them so the overwrite lands.
;
; NSIS_HOOK_PREUNINSTALL runs the same kill so the uninstall-old phase of an
; upgrade (and a plain uninstall) can delete files that would otherwise be held
; open by surviving daemon/agent processes.
;
; IMPORTANT (build): do NOT reintroduce nsProcess::_FindProcess here. That plugin
; is NOT bundled in cargo-tauri's makensis, and calling it aborts `cargo tauri
; build` ("Plugin not found") — which is exactly why installerHooks sat at null
; for so long. We use nsExec::Exec (a core, always-present NSIS plugin) + taskkill
; instead, so this hook compiles with the stock toolchain.
;
; Keep this conventional + minimal. The Tauri template already pulls in the
; standard headers (LogicLib, x64, WinVer, etc.), so we rely on the
; ${If}/${RunningX64} style helpers being available.
; =============================================================================

!include "TextFunc.nsh"
!include "FileFunc.nsh"

; The fake executor is compile-time-only and belongs to the extracted,
; empty-section probe fixture. Never allow it in an application installer.
!ifdef SOLU_PROBE_FIXTURE_EXECUTOR
  !error "SOLU_PROBE_FIXTURE_EXECUTOR is forbidden in production installer hooks"
!endif

; -----------------------------------------------------------------------------
; Terminate every soluterminal process so the installer can overwrite/delete its
; files. This is the crux of the "Retry/Abort/Ignore file-in-use" fix.
;
; Why a force-kill (not a polite "please close the app" prompt): on the Windows-
; native backend each open pane is a detached `solud.exe agent` child that
; deliberately breaks away from the Tauri app's Job Object, so it OUTLIVES the
; app. Quitting soluterminal does NOT stop them, and the user can't see them to
; close them — so a Retry-until-closed prompt can never clear them. All agents
; share the solud.exe image name, so a single `/IM solud.exe` sweep catches the
; daemon and every breakaway agent at once.
;
; nsExec::Exec runs hidden and never shows a console window. We ignore the exit
; code: taskkill returns non-zero ("process not found") when nothing is running,
; which is the normal no-op case. The short Sleep lets Windows release the file
; handles before the copy/delete step runs.
; -----------------------------------------------------------------------------
!macro _SOLU_KILL_RUNNING_PROCESSES
  ; Shell first, then the daemon + all breakaway agents (all named solud.exe).
  nsExec::Exec 'taskkill /F /IM soluterminal.exe'
  Pop $R0
  nsExec::Exec 'taskkill /F /IM solud.exe'
  Pop $R0
  Sleep 800
!macroend

; -----------------------------------------------------------------------------
; WebView2 Evergreen runtime detection.
; The Evergreen runtime publishes its installed version under the EdgeUpdate
; "Clients" key, keyed by a fixed GUID. On 64-bit Windows the per-machine entry
; lives under the WOW6432Node redirected hive; per-user installs land in HKCU.
; A non-empty "pv" (product version) value means the runtime is present.
;
; OPEN QUESTION: this GUID + registry path must be validated on a real machine
; before shipping (see report). The value below is the commonly documented
; Evergreen client GUID.
; -----------------------------------------------------------------------------
!define SOLU_WV2_GUID "{F3017226-FE2A-4295-8BDF-00C3A9A7E4C5}"
!define SOLU_WV2_KEY_HKLM "SOFTWARE\WOW6432Node\Microsoft\EdgeUpdate\Clients\${SOLU_WV2_GUID}"
!define SOLU_WV2_KEY_HKCU "SOFTWARE\Microsoft\EdgeUpdate\Clients\${SOLU_WV2_GUID}"
; Evergreen Standalone/Bootstrapper "evergreen" download fwlink (always-current).
!define SOLU_WV2_DOWNLOAD_URL "https://go.microsoft.com/fwlink/p/?LinkId=2124703"

; Top-level global. NSIS requires Var /GLOBAL at script scope, NOT inside a
; macro that expands into a section/function (the hook is invoked from the
; install section, so an in-macro declaration would fail to compile).
Var SOLU_WV2_PV
Var SOLU_INSTALLED_DAEMON_VERSION
Var SOLU_DAEMON_VERSION_COMPARE
Var SOLU_FORCE_DAEMON_DOWNGRADE
Var SOLU_PROBE_STARTED
Var SOLU_PROBE_ELAPSED
Var SOLU_PROBE_LOG
Var SOLU_PROBE_FILE
Var SOLU_PROBE_OUTPUT
Var SOLU_PROBE_ATTEMPT
Var SOLU_PROBE_TOTAL_STARTED
Var SOLU_PROBE_TOTAL_ELAPSED

!macro NSIS_HOOK_PREINSTALL
  ; Also reject a switch defined AFTER hooks.nsh was included.
  !ifdef SOLU_PROBE_FIXTURE_EXECUTOR
    !error "SOLU_PROBE_FIXTURE_EXECUTOR is forbidden in production installer hooks"
  !endif
  ; ---------------------------------------------------------------------------
  ; 0) Monotonic daemon guard. ${VERSION} is both this shell version and the
  ; bundled solud.exe version. Resolve an existing daemon from its marker first,
  ; then its embedded Go build setting, before any process is killed or copied.
  ; Authorization is either the explicit flag or the adjacent, single-use token
  ; created only after the Go/Rust launch path verifies the installer SHA-256.
  ; ---------------------------------------------------------------------------
  StrCpy $SOLU_FORCE_DAEMON_DOWNGRADE "0"
  ClearErrors
  ${GetOptions} $CMDLINE "/SOLU_FORCE_DAEMON_DOWNGRADE" $R0
  ${IfNot} ${Errors}
    StrCpy $SOLU_FORCE_DAEMON_DOWNGRADE "1"
  ${EndIf}
  IfFileExists "$EXEPATH.allow-daemon-downgrade" 0 solu_downgrade_token_done
    ClearErrors
    Delete "$EXEPATH.allow-daemon-downgrade"
    IfErrors solu_downgrade_token_failed
    StrCpy $SOLU_FORCE_DAEMON_DOWNGRADE "1"
    Goto solu_downgrade_token_done
  solu_downgrade_token_failed:
    DetailPrint "Refusing to continue because the single-use daemon downgrade token could not be consumed."
    SetErrorLevel 66
    IfSilent solu_downgrade_token_failed_silent solu_downgrade_token_failed_interactive
  solu_downgrade_token_failed_interactive:
    MessageBox MB_ICONSTOP|MB_OK \
      "soluterminal could not consume the single-use daemon downgrade authorization token.$\r$\n$\r$\nInstallation stopped so the authorization cannot be reused."
    Abort
  solu_downgrade_token_failed_silent:
    Abort
  solu_downgrade_token_done:

  ; Only a truly absent daemon is a fresh install. Legacy installs have no
  ; marker, so fall through to their embedded main.version build setting.
  IfFileExists "$INSTDIR\solud.exe" 0 solu_downgrade_gate_done
  IfFileExists "$INSTDIR\solud.exe.version" 0 solu_downgrade_derive_binary
  ClearErrors
  FileOpen $R0 "$INSTDIR\solud.exe.version" r
  IfErrors solu_downgrade_derive_binary
  FileRead $R0 $SOLU_INSTALLED_DAEMON_VERSION
  FileClose $R0
  ${TrimNewLines} "$SOLU_INSTALLED_DAEMON_VERSION" $SOLU_INSTALLED_DAEMON_VERSION
  nsis_tauri_utils::SemverCompare "$SOLU_INSTALLED_DAEMON_VERSION" "solu-invalid-version-marker"
  Pop $SOLU_DAEMON_VERSION_COMPARE
  ${If} $SOLU_DAEMON_VERSION_COMPARE == 1
    Goto solu_downgrade_compare
  ${EndIf}

  solu_downgrade_derive_binary:
  ; This delimited block is also exercised by the probe-only fixture. Keep the
  ; parser and its timeout here: the fixture must not duplicate either one.
  ; SOLU_EMBEDDED_PROBE_BEGIN
  System::Call 'Kernel32::GetTickCount() i.s'
  Pop $SOLU_PROBE_TOTAL_STARTED
  StrCpy $SOLU_PROBE_ATTEMPT "1"
  solu_probe_attempt:
  ; Discard every previous result, including partial output from a timeout.
  StrCpy $R1 ""
  StrCpy $SOLU_INSTALLED_DAEMON_VERSION ""
  StrCpy $SOLU_PROBE_OUTPUT ""
  System::Call 'Kernel32::GetTickCount() i.s'
  Pop $SOLU_PROBE_STARTED
  System::Call 'Kernel32::SetEnvironmentVariable(t "SOLU_INSTALLED_DAEMON", t "$INSTDIR\solud.exe") i.r0'
  !ifdef SOLU_PROBE_FIXTURE_EXECUTOR
    !insertmacro SOLU_PROBE_FIXTURE_EXECUTOR
  !else
  nsExec::ExecToStack /TIMEOUT=20000 `"$SYSDIR\WindowsPowerShell\v1.0\powershell.exe" -NoProfile -NonInteractive -ExecutionPolicy Bypass -Command "$$s=[Text.Encoding]::ASCII.GetString([IO.File]::ReadAllBytes($$env:SOLU_INSTALLED_DAEMON));$$v=@([regex]::Matches($$s,'main\.version=(\d+(?:\.\d+)+)')|%{$$_.Groups[1].Value}|Select-Object -Unique);if($$v.Count -eq 1){[Console]::Out.Write($$v[0]);exit 0}else{exit 3}"`
  !endif
  Pop $R1
  Pop $SOLU_INSTALLED_DAEMON_VERSION
  ${TrimNewLines} "$SOLU_INSTALLED_DAEMON_VERSION" $SOLU_INSTALLED_DAEMON_VERSION
  System::Call 'Kernel32::GetTickCount() i.s'
  Pop $SOLU_PROBE_ELAPSED
  IntOp $SOLU_PROBE_ELAPSED $SOLU_PROBE_ELAPSED - $SOLU_PROBE_STARTED
  System::Call 'Kernel32::GetTickCount() i.s'
  Pop $SOLU_PROBE_TOTAL_ELAPSED
  IntOp $SOLU_PROBE_TOTAL_ELAPSED $SOLU_PROBE_TOTAL_ELAPSED - $SOLU_PROBE_TOTAL_STARTED
  ; nsExec returns "timeout", "error", or a child exit code. Preserve that
  ; distinction without changing the fail-closed decision below. Diagnostics
  ; are opt-in, append-only UTF-16LE, and not placed in the installed application
  ; by the hook. /BOM only adds a BOM to an empty diagnostic file.
  StrCpy $SOLU_PROBE_OUTPUT $SOLU_INSTALLED_DAEMON_VERSION 512
  DetailPrint "Daemon version probe: attempt=$SOLU_PROBE_ATTEMPT result=$R1 elapsedMs=$SOLU_PROBE_ELAPSED totalElapsedMs=$SOLU_PROBE_TOTAL_ELAPSED setEnvironment=$0 path=$INSTDIR\solud.exe output=$SOLU_PROBE_OUTPUT"
  ReadEnvStr $SOLU_PROBE_LOG "SOLU_INSTALLER_PROBE_LOG"
  StrCmp $SOLU_PROBE_LOG "" solu_probe_log_done
  ClearErrors
  FileOpen $SOLU_PROBE_FILE "$SOLU_PROBE_LOG" a
  IfErrors solu_probe_log_done
  ; NSIS mode "a" preserves the file but does not position at EOF.
  FileSeek $SOLU_PROBE_FILE 0 END
  IfErrors solu_probe_log_close
  FileWriteUTF16LE /BOM $SOLU_PROBE_FILE "probeSchema=1 attempt=$SOLU_PROBE_ATTEMPT result=$R1 elapsedMs=$SOLU_PROBE_ELAPSED totalElapsedMs=$SOLU_PROBE_TOTAL_ELAPSED setEnvironment=$0 path=$INSTDIR\solud.exe output=$SOLU_PROBE_OUTPUT$\r$\n"
  solu_probe_log_close:
  FileClose $SOLU_PROBE_FILE
  solu_probe_log_done:
  ClearErrors
  ; nsExec returned only after its child exited and output drained on our
  ; supported Windows path. Retry ONCE only for its timeout result, never for
  ; launch errors, nonzero exits, missing versions or conflicting versions.
  ; /TIMEOUT is output inactivity, not a wall-clock bound: process creation,
  ; termination/draining and received output can exceed two times 20 seconds.
  StrCmp $R1 "timeout" 0 solu_probe_done
  StrCmp $SOLU_PROBE_ATTEMPT "1" 0 solu_probe_done
  StrCpy $SOLU_PROBE_ATTEMPT "2"
  Goto solu_probe_attempt
  solu_probe_done:
  ; SOLU_EMBEDDED_PROBE_END
  ; SOLU_EMBEDDED_GATE_BEGIN
  ${If} $R1 != "0"
    Goto solu_downgrade_version_unknown
  ${EndIf}
  nsis_tauri_utils::SemverCompare "$SOLU_INSTALLED_DAEMON_VERSION" "solu-invalid-version-marker"
  Pop $SOLU_DAEMON_VERSION_COMPARE
  ${If} $SOLU_DAEMON_VERSION_COMPARE != 1
    Goto solu_downgrade_version_unknown
  ${EndIf}

  solu_downgrade_compare:
  nsis_tauri_utils::SemverCompare "$SOLU_INSTALLED_DAEMON_VERSION" "${VERSION}"
  Pop $SOLU_DAEMON_VERSION_COMPARE
  ${If} $SOLU_DAEMON_VERSION_COMPARE == 1
  ${AndIf} $SOLU_FORCE_DAEMON_DOWNGRADE != "1"
    DetailPrint "Refusing daemon downgrade: installed $SOLU_INSTALLED_DAEMON_VERSION is newer than bundled ${VERSION}. Re-run explicitly with /SOLU_FORCE_DAEMON_DOWNGRADE to override."
    SetErrorLevel 65
    IfSilent solu_downgrade_refused_silent solu_downgrade_refused_interactive
    solu_downgrade_refused_interactive:
      MessageBox MB_ICONSTOP|MB_OK \
        "soluterminal refused to replace daemon v$SOLU_INSTALLED_DAEMON_VERSION with the older bundled daemon v${VERSION}.$\r$\n$\r$\nTo authorize this downgrade explicitly, re-run the installer with /SOLU_FORCE_DAEMON_DOWNGRADE."
      Abort
    solu_downgrade_refused_silent:
      Abort
  ${EndIf}
  Goto solu_downgrade_gate_done

  solu_downgrade_version_unknown:
  ${If} $SOLU_FORCE_DAEMON_DOWNGRADE != "1"
    DetailPrint "Refusing daemon replacement: the installed version could not be established from its marker or embedded-version probe; bundled daemon is ${VERSION}. Re-run explicitly with /SOLU_FORCE_DAEMON_DOWNGRADE to override."
    SetErrorLevel 67
    IfSilent solu_downgrade_unknown_silent solu_downgrade_unknown_interactive
    solu_downgrade_unknown_interactive:
      MessageBox MB_ICONSTOP|MB_OK \
        "soluterminal found an installed daemon with no usable version marker or embedded build version. It refused to replace it with bundled daemon v${VERSION}.$\r$\n$\r$\nTo authorize this replacement explicitly, re-run the installer with /SOLU_FORCE_DAEMON_DOWNGRADE."
      Abort
    solu_downgrade_unknown_silent:
      Abort
  ${Else}
    DetailPrint "Replacing an installed daemon with unknown version because /SOLU_FORCE_DAEMON_DOWNGRADE explicitly authorized it."
  ${EndIf}
  solu_downgrade_gate_done:
  ; SOLU_EMBEDDED_GATE_END

	; ---------------------------------------------------------------------------
	; 1) Architecture gate: 64-bit Windows only.
  ; ${RunningX64} is true on x64 (and on ARM64 running x64 emulation, which is
  ; fine for our amd64 build). We additionally reject 32-bit (x86) hosts where
  ; PROCESSOR_ARCHITECTURE is plain "x86" with no WOW64 redirection.
  ; ---------------------------------------------------------------------------
  ${IfNot} ${RunningX64}
    MessageBox MB_ICONSTOP|MB_OK \
      "soluterminal currently supports 64-bit Windows only.$\r$\n$\r$\nThis machine does not appear to be running 64-bit Windows, so installation cannot continue."
    Abort
  ${EndIf}

  ; ---------------------------------------------------------------------------
  ; 2) WebView2 Evergreen runtime check.
  ; Read the "pv" value from the per-machine (HKLM, WOW6432Node) location first,
  ; then fall back to the per-user (HKCU) location. If neither yields a version,
  ; the runtime is missing and the Tauri window would render blank.
  ; ---------------------------------------------------------------------------
  ClearErrors
  ReadRegStr $SOLU_WV2_PV HKLM "${SOLU_WV2_KEY_HKLM}" "pv"
  ${If} ${Errors}
  ${OrIf} $SOLU_WV2_PV == ""
    ; Not found per-machine: try per-user.
    ClearErrors
    ReadRegStr $SOLU_WV2_PV HKCU "${SOLU_WV2_KEY_HKCU}" "pv"
  ${EndIf}

  ${If} $SOLU_WV2_PV == ""
    ; Runtime absent. Offer to open the Evergreen bootstrapper download.
    ; YES  -> open the download page in the default browser, then continue the
    ;         install (user can run the bootstrapper afterward).
    ; NO   -> continue without WebView2 (window may be blank until installed).
    MessageBox MB_ICONEXCLAMATION|MB_YESNO \
      "soluterminal needs the Microsoft Edge WebView2 runtime, which does not appear to be installed.$\r$\n$\r$\nWithout it the soluterminal window may appear blank.$\r$\n$\r$\nWould you like to open the WebView2 download page in your browser?$\r$\n$\r$\n(Choose No to continue installing anyway.)" \
      IDNO solu_wv2_done
      ; Open the always-current Evergreen bootstrapper download URL.
      ExecShell "open" "${SOLU_WV2_DOWNLOAD_URL}"
    solu_wv2_done:
  ${EndIf}

  ; ---------------------------------------------------------------------------
  ; 3) Running-process kill.
  ; A live soluterminal.exe (the Tauri shell) or solud.exe (the daemon + its
  ; detached pane agents) holds port :8787 and keeps its own files open, which
  ; is what triggers the NSIS "file in use -> Retry/Abort/Ignore" dialog. Kill
  ; them here so the file copy below can overwrite cleanly. See the macro's
  ; comment for why we force-kill rather than ask the user to close the app.
  ; ---------------------------------------------------------------------------
  !insertmacro _SOLU_KILL_RUNNING_PROCESSES
!macroend

; Write the installed-daemon marker only after the payload is fully installed.
; This makes a successful forced downgrade the new truthful baseline instead of
; immediately triggering external-downgrade recovery on the next daemon boot.
!macro NSIS_HOOK_POSTINSTALL
	ClearErrors
	FileOpen $R0 "$INSTDIR\solud.exe.version" w
	${IfNot} ${Errors}
		FileWrite $R0 "${VERSION}$\r$\n"
		FileClose $R0
	${Else}
		DetailPrint "Warning: could not write installed daemon version marker $INSTDIR\solud.exe.version"
	${EndIf}
	; The exe keeps its path across upgrades, so Explorer's icon cache can keep
	; drawing the previous build's icon on shortcuts and the taskbar. Tell the
	; shell icons changed (SHCNE_ASSOCCHANGED, flushed) so it reloads them.
	System::Call 'shell32::SHChangeNotify(i 0x08000000, i 0x1000, p 0, p 0)'
!macroend

; -----------------------------------------------------------------------------
; Full uninstall cleanup of what SoluTerminal added to Claude Code and Codex:
; the "soluterminal" MCP server in ~/.claude.json, our hooks in
; ~/.claude/settings.json, the server in ~/.codex/config.toml and the marked
; block in ~/.codex/AGENTS.md. `solud uninstall-cleanup` removes only our own
; entries, backs each changed file up into the data dir, and records the
; uninstall so a daemon still running never re-adds them.
;
; Runs only for a standalone uninstall (never the uninstall-old phase of an
; update), for both sides of the machine:
;   - Windows-native: the bundled solud.exe, still on disk at this point.
;   - WSL: the WSL daemon's own binary in the default distro (the desktop's
;     ~/.local/share/soluterminal/solud, else a manual ~/.local/bin/solud).
;     wsl.exe starts a stopped distro to do this. We accept that: the WSL side
;     holds the main Claude/Codex config, so skipping it would leave the very
;     entries the user is uninstalling to get rid of. /TIMEOUT bounds a
;     stuck VM, and nothing here can fail or block the uninstall.
; -----------------------------------------------------------------------------
!macro _SOLU_AGENT_INTEGRATION_CLEANUP
  IfFileExists "$INSTDIR\solud.exe" 0 solu_cleanup_win_done
    DetailPrint "Removing SoluTerminal's entries from Claude Code and Codex (Windows)"
    nsExec::ExecToLog /TIMEOUT=30000 '"$INSTDIR\solud.exe" uninstall-cleanup'
    Pop $R0
    DetailPrint "Windows cleanup result: $R0"
  solu_cleanup_win_done:
  IfFileExists "$SYSDIR\wsl.exe" 0 solu_cleanup_wsl_done
    DetailPrint "Removing SoluTerminal's entries from Claude Code and Codex (WSL)"
    nsExec::ExecToLog /TIMEOUT=60000 '"$SYSDIR\wsl.exe" -e sh -lc "for S in $$HOME/.local/share/soluterminal/solud $$HOME/.local/bin/solud; do if test -x $$S; then $$S uninstall-cleanup; break; fi; done; true"'
    Pop $R0
    DetailPrint "WSL cleanup result: $R0"
  solu_cleanup_wsl_done:
!macroend

; =============================================================================
; NSIS_HOOK_PREUNINSTALL — runs before the uninstaller deletes files.
; During an upgrade Tauri runs the old uninstaller before the new install; a
; standalone uninstall runs it directly. In both cases surviving solud.exe
; agents lock files the uninstaller is about to delete, so we clear them first.
; =============================================================================
!macro NSIS_HOOK_PREUNINSTALL
  !insertmacro _SOLU_KILL_RUNNING_PROCESSES
  ; Value name must stay in sync with RUN_VALUE_NAME in src/autostart.rs.
  ; Tauri passes /UPDATE to the old uninstaller during an in-place upgrade.
  ; Preserve both the live value and preference then; reconcile() still
  ; rewrites the stable executable path when the new shell starts. A standalone
  ; uninstall clears both registration and intent so a later reinstall remains
  ; default-OFF until the operator explicitly enables it again.
  ${If} $UpdateMode <> 1
    DeleteRegValue HKCU "Software\Microsoft\Windows\CurrentVersion\Run" "soluterminal"
    Delete "$LOCALAPPDATA\soluterminal\autostart.txt"
    !insertmacro _SOLU_AGENT_INTEGRATION_CLEANUP
  ${EndIf}
!macroend
