package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const zshOriginalDirEnv = "SOLU_ZSH_ORIGINAL_DOTDIR"
const zshInlineDirEnv = "SOLU_ZSH_INLINE_DOTDIR"

// The proxy runs the user's original startup files in their usual order, then
// supplies Codex's inline default after .zshrc has finished changing PATH.
// It restores the user's ZDOTDIR before the prompt and before .zlogin runs.
const codexInlineZshEnv = `SOLU_ZSH_EFFECTIVE_DOTDIR=$SOLU_ZSH_ORIGINAL_DOTDIR
ZDOTDIR=$SOLU_ZSH_EFFECTIVE_DOTDIR
if [[ -r "$ZDOTDIR/.zshenv" ]]; then
  source "$ZDOTDIR/.zshenv"
fi
SOLU_ZSH_EFFECTIVE_DOTDIR=${ZDOTDIR:-$HOME}
if [[ -o interactive ]]; then
  ZDOTDIR=$SOLU_ZSH_INLINE_DOTDIR
fi
`

const codexInlineZshProfile = `ZDOTDIR=$SOLU_ZSH_EFFECTIVE_DOTDIR
if [[ -r "$ZDOTDIR/.zprofile" ]]; then
  source "$ZDOTDIR/.zprofile"
fi
SOLU_ZSH_EFFECTIVE_DOTDIR=${ZDOTDIR:-$HOME}
ZDOTDIR=$SOLU_ZSH_INLINE_DOTDIR
`

const codexInlineZshRC = `ZDOTDIR=$SOLU_ZSH_EFFECTIVE_DOTDIR
if [[ -r "$ZDOTDIR/.zshrc" ]]; then
  source "$ZDOTDIR/.zshrc"
fi
ZDOTDIR=${ZDOTDIR:-$HOME}
case "$(whence -w codex)" in
  *": alias"|*": function") ;;
  *)
  function codex` + codexPaneBashFunction + `
  ;;
esac
`

func ensureCodexInlineZshConfig(dataDir, original string) (string, error) {
	if original == "" {
		var err error
		original, err = os.UserHomeDir()
		if err != nil {
			return "", err
		}
	}
	if !filepath.IsAbs(original) {
		return "", fmt.Errorf("relative ZDOTDIR cannot be proxied")
	}
	original = filepath.Clean(original)
	sum := sha256.Sum256([]byte(original))
	dir := filepath.Join(dataDir, "codex-inline-zsh", hex.EncodeToString(sum[:8]))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	for name, body := range map[string]string{
		".zshenv":   codexInlineZshEnv,
		".zprofile": codexInlineZshProfile,
		".zshrc":    codexInlineZshRC,
	} {
		path := filepath.Join(dir, name)
		if current, err := os.ReadFile(path); err == nil && string(current) == body {
			continue
		}
		if err := writeFileAtomicSynced(path, []byte(body), 0o600); err != nil {
			return "", err
		}
	}
	return dir, nil
}

func codexInlineZshEnvVars(dataDir string, env []string) ([]string, error) {
	original := ""
	home := ""
	for _, kv := range env {
		if strings.HasPrefix(kv, "ZDOTDIR=") {
			original = strings.TrimPrefix(kv, "ZDOTDIR=")
		}
		if strings.HasPrefix(kv, "HOME=") {
			home = strings.TrimPrefix(kv, "HOME=")
		}
	}
	if original == "" {
		original = home
	}
	if original == "" {
		var err error
		original, err = os.UserHomeDir()
		if err != nil {
			return nil, err
		}
	}
	dir, err := ensureCodexInlineZshConfig(dataDir, original)
	if err != nil {
		return nil, err
	}
	return []string{zshOriginalDirEnv + "=" + original, zshInlineDirEnv + "=" + dir, "ZDOTDIR=" + dir}, nil
}
