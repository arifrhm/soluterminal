package main

import (
	"bufio"
	"errors"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"
)

// loadEnvFile reads KEY=VALUE lines from <dataDir>/env and sets them
// in the process environment — unless the variable is already set
// (shell-provided env takes precedence so per-invocation overrides still
// work). Missing file is not an error.
//
// Gives us a stable place for daemon secrets (GROQ_API_KEY today, more
// tomorrow) that's loaded the same way whether the daemon is launched
// from a shell, a systemd unit, or a wrapper script.
//
// Format:
//
//	# comments and blank lines allowed
//	KEY=value
//	KEY="quoted value"       # outer quotes stripped
//	KEY='single-quoted'      # outer quotes stripped
//	export KEY=value         # `export` prefix accepted for shell compatibility
func loadEnvFile(dataDir string) {
	path := filepath.Join(dataDir, "env")
	f, err := os.Open(path)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			log.Printf("env: could not read %s: %v", path, err)
		}
		return
	}
	defer f.Close()

	loaded := 0
	skipped := 0
	s := bufio.NewScanner(f)
	for s.Scan() {
		line := strings.TrimSpace(s.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		eq := strings.IndexByte(line, '=')
		if eq <= 0 {
			continue
		}
		key := strings.TrimSpace(line[:eq])
		val := strings.TrimSpace(line[eq+1:])
		// Strip matching outer quotes. Only single-wrapper — we don't
		// implement shell-style escape sequences; users wanting that
		// can just set the env directly.
		if n := len(val); n >= 2 {
			if (val[0] == '"' && val[n-1] == '"') || (val[0] == '\'' && val[n-1] == '\'') {
				val = val[1 : n-1]
			}
		}
		if _, already := os.LookupEnv(key); already {
			skipped++
			continue
		}
		if err := os.Setenv(key, val); err != nil {
			log.Printf("env: set %s: %v", key, err)
			continue
		}
		loaded++
	}
	if err := s.Err(); err != nil {
		log.Printf("env: scan %s: %v", path, err)
	}
	if loaded > 0 || skipped > 0 {
		log.Printf("env: loaded %d var(s) from %s (%d already set)", loaded, path, skipped)
	}
}
