package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// native_trust.go pre-seeds Claude Code's per-project folder-trust flag so that
// switching a native pane BACK to terminal — which respawns the INTERACTIVE
// `claude --resume <session>` TUI — doesn't land on the "Do you trust the files
// in this folder?" onboarding gate.
//
// Background: the headless native agent (`claude -p …`) NEVER shows the trust
// dialog; the interactive TUI does, gated on
//   projects["<absolute-cwd>"].hasTrustDialogAccepted == true
// in ~/.claude.json. A fresh project (or one only ever opened headless) can have
// that key absent/false, so the terminal resume would block on the trust prompt
// instead of continuing the conversation. ensureFolderTrusted flips ONLY that
// boolean, byte-for-byte preserving every other key (the file also holds OAuth
// credentials and a large per-project history — nothing may be dropped).
//
// This is the same operation as aria_workspace_tools.go's preTrustClaudeFolder,
// extracted as a standalone error-returning helper for the switch path. It is
// best-effort: the dialog appearing is a soft UX failure, not a switch failure,
// so the caller logs and continues on any error.

// ensureFolderTrusted marks the absolute path cwd as a trusted Claude Code
// project in ~/.claude.json so an interactive `claude --resume` skips the
// folder-trust dialog.
//
// Safety contract:
//   - Reads the FULL JSON document and preserves EVERY existing key/value
//     (credentials, history, settings) — it round-trips an untyped map and only
//     mutates projects[cwd].hasTrustDialogAccepted.
//   - If the flag is already true, it is a no-op (the file is not rewritten), so
//     repeated calls don't churn the file or its mtime.
//   - If ~/.claude.json doesn't exist yet, no-op (claude creates it on first run
//     and prompts for trust then).
//   - Writes back ATOMICALLY: a temp file in the SAME directory + os.Rename,
//     preserving the original file's mode.
func ensureFolderTrusted(cwd string) error {
	if cwd == "" {
		return fmt.Errorf("ensureFolderTrusted: empty cwd")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("ensureFolderTrusted: home dir: %w", err)
	}
	path := filepath.Join(home, ".claude.json")

	info, err := os.Stat(path)
	if err != nil {
		// Most common: the file doesn't exist (claude has never run here).
		// Claude will create it and prompt for trust on first launch — nothing
		// for us to seed. Not an error worth surfacing.
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("ensureFolderTrusted: stat: %w", err)
	}
	mode := info.Mode().Perm()

	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("ensureFolderTrusted: read: %w", err)
	}
	// Round-trip the WHOLE document as an untyped map so every unrelated key —
	// including the OAuth credentials and per-project history — is preserved.
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		return fmt.Errorf("ensureFolderTrusted: parse: %w", err)
	}

	projects, ok := doc["projects"].(map[string]any)
	if !ok {
		// projects key missing or a non-object — create a fresh map. (If it was a
		// non-object we replace it; claude always writes an object here, so this
		// only happens on a corrupt/empty file, where seeding is still the right
		// outcome.)
		projects = map[string]any{}
		doc["projects"] = projects
	}
	entry, ok := projects[cwd].(map[string]any)
	if !ok {
		// No entry for this cwd yet (or a non-object) — create one, preserving
		// nothing because there was nothing valid to preserve for this key.
		entry = map[string]any{}
		projects[cwd] = entry
	}
	if v, _ := entry["hasTrustDialogAccepted"].(bool); v {
		// Already trusted — don't rewrite the file.
		return nil
	}
	entry["hasTrustDialogAccepted"] = true

	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return fmt.Errorf("ensureFolderTrusted: marshal: %w", err)
	}

	// Atomic write: temp file in the SAME directory (so os.Rename is a same-fs
	// atomic replace), preserving the original file mode.
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".claude.json.tmp-*")
	if err != nil {
		return fmt.Errorf("ensureFolderTrusted: create temp: %w", err)
	}
	tmpName := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpName) }
	if _, err := tmp.Write(out); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("ensureFolderTrusted: write temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return fmt.Errorf("ensureFolderTrusted: close temp: %w", err)
	}
	if err := os.Chmod(tmpName, mode); err != nil {
		cleanup()
		return fmt.Errorf("ensureFolderTrusted: chmod temp: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		cleanup()
		return fmt.Errorf("ensureFolderTrusted: rename: %w", err)
	}
	return nil
}
