package main

import "strings"

// unionWindowsPath merges livePath (the registry-derived Machine+User PATH a
// freshly-opened shell would read) into env's PATH entry, taking the UNION so
// nothing the daemon already inherited is dropped — a working machine keeps every
// dir it has today, while a machine whose daemon holds a stale snapshot gains the
// dirs added since (e.g. a Claude Code install that wrote the User PATH after the
// daemon started). livePath dirs come first (fresh-shell order), then any inherited
// dir not already present; dedup is case-insensitive and ignores a trailing slash.
//
// The original PATH key spelling is preserved (Windows uses "Path") so we never
// introduce a SECOND, differently-cased PATH variable — Go's exec dedups env
// case-insensitively on Windows and a stray "PATH=" alongside "Path=" would
// collapse to one unpredictably. A blank livePath is a no-op, so callers (and the
// non-Windows build, which passes "") can invoke this unconditionally.
func unionWindowsPath(env []string, livePath string) []string {
	if strings.TrimSpace(livePath) == "" {
		return env
	}
	key := "Path"
	idx := -1
	cur := ""
	for i, kv := range env {
		eq := strings.IndexByte(kv, '=')
		if eq < 0 {
			continue
		}
		if strings.EqualFold(kv[:eq], "PATH") {
			key = kv[:eq]
			cur = kv[eq+1:]
			idx = i
			break
		}
	}

	seen := make(map[string]bool)
	var dirs []string
	add := func(list string) {
		for _, p := range strings.Split(list, ";") {
			p = strings.TrimSpace(p)
			if p == "" {
				continue
			}
			k := strings.ToLower(strings.TrimRight(p, `\`))
			if seen[k] {
				continue
			}
			seen[k] = true
			dirs = append(dirs, p)
		}
	}
	add(livePath) // fresh-shell registry order first
	add(cur)      // then keep anything the daemon already had

	merged := key + "=" + strings.Join(dirs, ";")
	if idx >= 0 {
		env[idx] = merged
		return env
	}
	return append(env, merged)
}
