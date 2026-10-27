package main

import (
	_ "embed"
	"log"
	"os"
	"path/filepath"
)

// This file implements soluterminal's "sane ls colors by default" feature.
// There are two separate problems we solve:
//
// 1. Linux / WSL use GNU coreutils' `ls`, which reads `LS_COLORS` env var.
//    Ubuntu/Debian's default bashrc sets that var via `dircolors` — which
//    reads `~/.dircolors` if present, falling back to built-in defaults.
//    The built-in defaults include `OTHER_WRITABLE 34;42` and
//    `STICKY_OTHER_WRITABLE 30;42` — blue/black text on green background,
//    hard to read. Fix: write a `~/.dircolors` with those two lines tuned.
//
// 2. macOS uses BSD `ls`, which reads `LSCOLORS` (different var name,
//    different 11-character format). Its defaults have the same green-bg
//    issue at positions 10 and 11. Fix: set `LSCOLORS` in the agent's env
//    when spawning panes.
//
// Both fixes are non-destructive — Linux users who already have a
// `~/.dircolors` keep their config, macOS users who have their own
// `LSCOLORS` in their shell rc keep theirs (shell rc runs after env).

//go:embed data/dircolors.default
var defaultDircolorsContent []byte

// safeLSCOLORS is the BSD ls color map with positions 10 (sticky+writable
// dirs) and 11 (other-writable dirs) changed from the buggy "ac" / "ad"
// (both with green bg) to "Ex" (bold blue text, default bg). Everything
// else matches BSD's defaults.
//
// Format is 11 pairs of characters: each pair is <fg><bg> where:
//
//	a-h = black red green brown blue magenta cyan light-grey  (lowercase)
//	A-H = same colors, bold                                   (uppercase)
//	x   = default color
//
// Positions (in order):
//
//	1: directory        2: symlink         3: socket
//	4: pipe             5: executable      6: block special
//	7: char special     8: setuid exec     9: setgid exec
//	10: sticky+other-writable dir          11: other-writable dir
const safeLSCOLORS = "ExFxCxDxBxEgEdAbAgExEx"

// ensureDircolorsFile writes a tuned ~/.dircolors on first run if the
// user doesn't already have one. Safe to call on every daemon start —
// it's a no-op when a file already exists.
func ensureDircolorsFile() {
	home, err := os.UserHomeDir()
	if err != nil {
		return
	}
	path := filepath.Join(home, ".dircolors")
	if _, err := os.Stat(path); err == nil {
		// Already exists — respect whatever the user has.
		return
	} else if !os.IsNotExist(err) {
		// Some other stat error (permissions?) — don't touch.
		return
	}
	if err := os.WriteFile(path, defaultDircolorsContent, 0o644); err != nil {
		log.Printf("ensureDircolorsFile: %v", err)
		return
	}
	log.Printf("wrote default %s (fixes blue-on-green for GNU ls; delete to revert)", path)
}
