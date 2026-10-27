package main

import (
	"os"
	"runtime"
	"strings"
	"testing"
)

// native_recovery_test.go pins the native-pane self-heal that recovers a Focus
// pane after a daemon restart / app update. The regression it guards against:
// an older daemon's resume path re-persisted a native pane with its Kind dropped
// to "" while keeping the headless `claude -p … stream-json` Cmd. On the next
// restart that pane respawned as a TERMINAL pane in a PTY, so `claude -p` got a
// TTY stdin and crashed with "Input must be provided … --print", then fell back
// to a bare bash shell — the Focus pane was lost.
//
// The fix is three coupled guards, all keyed on isHeadlessClaudeCmd:
//   - --no-pty follows the COMMAND, not just Kind (pane_spawn.go Spawn).
//   - a lost native Kind is healed on restart (healLostNativeKind, main.go).
//   - the native headless agent is exec'd, not given the interactive-claude
//     `; exec bash -i` shell fallback (tryLoginShellWrap).

func TestIsHeadlessClaudeCmd(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		cmd  []string
		want bool
	}{
		{"bare native argv", nativeClaudeArgv("", false, "", false, ""), true},
		{"native argv with resume + narrate", nativeClaudeArgv("sess-123", true, "", false, ""), true},
		{"terminal resume TUI", []string{"claude", "--resume", "abc"}, false},
		{"bare claude", []string{"claude"}, false},
		{"plain shell", []string{"/usr/bin/bash"}, false},
		{"empty", nil, false},
		{"unrelated cmd that merely mentions stream-json", []string{"echo", "stream-json"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isHeadlessClaudeCmd(tc.cmd); got != tc.want {
				t.Fatalf("isHeadlessClaudeCmd(%v) = %v, want %v", tc.cmd, got, tc.want)
			}
		})
	}
}

// The real on-disk corruption stores the BASH-WRAPPED native argv (claude isn't
// on the daemon's slim PATH, so tryLoginShellWrap wraps it). isHeadlessClaudeCmd
// must see through the wrap, else neither the --no-pty guard nor the heal fire.
func TestIsHeadlessClaudeCmdDetectsBashWrappedNative(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("login shell wrapping is Unix-only")
	}
	wrapped, ok := tryLoginShellWrap(nativeClaudeArgv("sess-xyz", true, "", false, ""), "", nil)
	if !ok {
		t.Skip("bash not available to wrap (tryLoginShellWrap returned false)")
	}
	if !isHeadlessClaudeCmd(wrapped) {
		t.Fatalf("isHeadlessClaudeCmd did not see through the bash-wrapped native argv: %#v", wrapped)
	}
}

func TestHealLostNativeKind(t *testing.T) {
	t.Parallel()
	nativeCmd := nativeClaudeArgv("sess-abc", true, "", false, "")
	cases := []struct {
		name     string
		info     PersistedPane
		wantHeal bool
		wantKind string
	}{
		{"corrupted native (empty kind + native cmd)", PersistedPane{Kind: "", Cmd: nativeCmd}, true, "native"},
		{"already native is untouched", PersistedPane{Kind: "native", Cmd: nativeCmd}, false, "native"},
		{"terminal claude resume is not native", PersistedPane{Kind: "", Cmd: []string{"claude", "--resume", "x"}}, false, ""},
		{"plain shell is not native", PersistedPane{Kind: "", Cmd: []string{"bash"}}, false, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, healed := healLostNativeKind(tc.info)
			if healed != tc.wantHeal {
				t.Fatalf("healLostNativeKind healed=%v, want %v", healed, tc.wantHeal)
			}
			if got.Kind != tc.wantKind {
				t.Fatalf("healLostNativeKind kind=%q, want %q", got.Kind, tc.wantKind)
			}
		})
	}
}

// The exact field shape on disk for the broken panes: Kind omitted (""), Cmd the
// bash-wrapped native argv. healLostNativeKind must restore Kind=="native".
func TestHealLostNativeKindOnRealCorruptionShape(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("login shell wrapping is Unix-only")
	}
	wrapped, ok := tryLoginShellWrap(nativeClaudeArgv("sess-real", true, "", false, ""), "", nil)
	if !ok {
		t.Skip("bash not available to wrap")
	}
	info := PersistedPane{ID: "pane_corrupt", Kind: "", Cmd: wrapped, ClaudeSessionID: "sess-real"}
	got, healed := healLostNativeKind(info)
	if !healed || got.Kind != "native" {
		t.Fatalf("healLostNativeKind(real corruption) = (Kind=%q, healed=%v), want (native, true)", got.Kind, healed)
	}
	// Everything else must be preserved — heal only touches Kind.
	if got.ID != info.ID || got.ClaudeSessionID != info.ClaudeSessionID {
		t.Fatalf("heal mutated more than Kind: %#v", got)
	}
}

// A native headless agent must be exec'd (so the agent exits when claude exits →
// pane shows exited/resumable), NOT handed the interactive-claude `; exec bash -i`
// fallback (which would leave a zombie shell the native parser can't read — and
// is what made the broken pane present a usable bash prompt).
func TestTryLoginShellWrapNativeUsesPlainExec(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("login shell wrapping is Unix-only")
	}
	wrapped, ok := tryLoginShellWrap(nativeClaudeArgv("sess-1", true, "", false, ""), "", nil)
	if !ok {
		t.Skip("bash not available to wrap")
	}
	script := wrapped[2]
	if strings.HasSuffix(script, " -i") || strings.Contains(script, "; exec "+wrapped[0]+" -i") {
		t.Fatalf("native wrap script = %q, must NOT drop back to an interactive shell", script)
	}
	if !strings.Contains(script, "; exec 'claude' '-p'") {
		t.Fatalf("native wrap script = %q, want a plain `exec claude -p …`", script)
	}
}

// resumeSessionIDFromCmd recovers a native pane's session id from the baked-in
// `--resume <id>` when the persisted ClaudeSessionID field was cleared — covering
// both the bare argv and the bash-wrapped single-quoted form on disk.
func TestResumeSessionIDFromCmd(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		cmd  []string
		want string
	}{
		{"bare argv", []string{"claude", "-p", "--resume", "abc-123"}, "abc-123"},
		{"native bare argv", nativeClaudeArgv("sess-xyz", false, "", false, ""), "sess-xyz"},
		{"no resume", nativeClaudeArgv("", false, "", false, ""), ""},
		{"plain shell", []string{"bash"}, ""},
		{"empty", nil, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := resumeSessionIDFromCmd(tc.cmd); got != tc.want {
				t.Fatalf("resumeSessionIDFromCmd(%v) = %q, want %q", tc.cmd, got, tc.want)
			}
		})
	}
}

func TestResumeSessionIDFromCmdBashWrapped(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("login shell wrapping is Unix-only")
	}
	wrapped, ok := tryLoginShellWrap(nativeClaudeArgv("sess-wrapped-99", true, "", false, ""), "", nil)
	if !ok {
		t.Skip("bash not available to wrap")
	}
	if got := resumeSessionIDFromCmd(wrapped); got != "sess-wrapped-99" {
		t.Fatalf("resumeSessionIDFromCmd(wrapped) = %q, want sess-wrapped-99", got)
	}
}

// isStaleNonPtyNativeAgent must NEVER match a non-agent pid — the restart loop
// calls terminatePid on a match, so a false positive would kill an unrelated
// (possibly pid-recycled) process. These guard cases pin the safety floor.
func TestIsStaleNonPtyNativeAgentGuards(t *testing.T) {
	t.Parallel()
	if isStaleNonPtyNativeAgent(0, "/x.sock") {
		t.Fatal("pid 0 must be false")
	}
	if isStaleNonPtyNativeAgent(-1, "/x.sock") {
		t.Fatal("negative pid must be false")
	}
	if isStaleNonPtyNativeAgent(12345, "") {
		t.Fatal("empty socket must be false")
	}
	// The running test process is not a `solud agent` bound to this socket, so
	// even though it's a live pid it must NOT match (identity check).
	if isStaleNonPtyNativeAgent(os.Getpid(), "/home/x/.solu/agents/pane_x.sock") {
		t.Fatal("the test process is not a solud agent for this socket; must be false")
	}
}

// TestNativeClaudeArgvPromptSuggestionsFlag pins the opt-in conditioning of
// --prompt-suggestions (per-machine native setting): present only when
// promptSuggestions is true. It must NOT change headless identity — the three
// isHeadlessClaudeCmd markers (input-format/output-format/stream-json) survive in
// both forms, or --no-pty + healLostNativeKind would misclassify the pane.
func TestNativeClaudeArgvPromptSuggestionsFlag(t *testing.T) {
	t.Parallel()
	contains := func(argv []string, flag string) bool {
		for _, a := range argv {
			if a == flag {
				return true
			}
		}
		return false
	}
	on := nativeClaudeArgv("sess-ps", true, "", true, "")
	off := nativeClaudeArgv("sess-ps", true, "", false, "")
	if !contains(on, "--prompt-suggestions") {
		t.Fatal("argv(promptSuggestions=true) is missing --prompt-suggestions")
	}
	if contains(off, "--prompt-suggestions") {
		t.Fatal("argv(promptSuggestions=false) carries --prompt-suggestions; it must be opt-in only")
	}
	for name, argv := range map[string][]string{"on": on, "off": off} {
		if !isHeadlessClaudeCmd(argv) {
			t.Fatalf("isHeadlessClaudeCmd(%s)=false; --prompt-suggestions must not break headless detection: %v", name, argv)
		}
	}
}
