package main

// GLM (Z.ai) provider config for per-pane routing. A pane with Provider=="glm"
// runs Claude Code against Z.ai's Anthropic-compatible endpoint instead of
// Anthropic. This store holds only NON-SECRET routing config (base URL, model
// ids, the auto-compact window). Stored in <dataDir>/glm_settings.json, mirroring
// claudecreds_settings.go (synchronous atomic 0o600 writes).
//
// SECURITY — the token is NEVER persisted here in the MVP and NEVER crosses the
// daemon→agent argv / --env transport:
//   - The Key field is optional and EMPTY in the MVP. The token is resolved LIVE
//     at every spawn (resolveKey) from ~/.config/glm/env's ZAI_API_KEY, so a
//     rotated key applies on the next spawn with no stale copy and no restart.
//   - The token is set ONLY on the agent child's c.Env (see providerEnvVars,
//     called from the agent process), never in argv, the comma-joined --env, the
//     login-shell wrap string, /proc/<pid>/cmdline, or any log.
//   - There is deliberately no String()/MarshalText that prints Key; never
//     %v/%+v this struct. The API exposes only a keyConfigured boolean.

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

const (
	glmDefaultBaseURL           = "https://api.z.ai/api/anthropic"
	glmDefaultModel             = "glm-5.2[1m]" // opus + sonnet tiers map here ([1m] = 1M context)
	glmDefaultHaikuModel        = "glm-4.7"     // small-fast-model tier (background calls)
	glmDefaultAutoCompactWindow = 1000000
)

// errGlmKeyMissing means no GLM token is configured anywhere (store Key empty AND
// ~/.config/glm/env has no usable ZAI_API_KEY). errGlmKeyComma rejects a key with
// a comma — purely defensive, since the token never transits the comma-joined
// --env transport by design.
var (
	errGlmKeyMissing = errors.New("no GLM key configured (set ZAI_API_KEY in ~/.config/glm/env)")
	errGlmKeyComma   = errors.New("GLM key must not contain a comma")
)

// glmSettings is the on-disk + in-memory shape. Key is optional (empty in MVP).
type glmSettings struct {
	Key               string `json:"key,omitempty"`
	BaseURL           string `json:"baseUrl"`
	OpusModel         string `json:"opusModel"`
	SonnetModel       string `json:"sonnetModel"`
	HaikuModel        string `json:"haikuModel"`
	AutoCompactWindow int    `json:"autoCompactWindow"`
}

// applyDefaults fills any empty/zero non-secret field with its default, so an
// absent file or a partial hand-edit still yields a usable, complete config.
func (g *glmSettings) applyDefaults() {
	if strings.TrimSpace(g.BaseURL) == "" {
		g.BaseURL = glmDefaultBaseURL
	}
	if strings.TrimSpace(g.OpusModel) == "" {
		g.OpusModel = glmDefaultModel
	}
	if strings.TrimSpace(g.SonnetModel) == "" {
		g.SonnetModel = glmDefaultModel
	}
	if strings.TrimSpace(g.HaikuModel) == "" {
		g.HaikuModel = glmDefaultHaikuModel
	}
	if g.AutoCompactWindow <= 0 {
		g.AutoCompactWindow = glmDefaultAutoCompactWindow
	}
}

// nonSecretEnvPairs returns the ordered NON-SECRET (key,value) env pairs for a
// GLM pane: base URL, the three model ids, and the auto-compact window. The token
// (ANTHROPIC_AUTH_TOKEN) is deliberately ABSENT — it is resolved + applied only on
// the agent child's c.Env (providerEnvVars), never here. Used by both the daemon's
// login-shell-wrap prefix and providerEnvVars.
func (g glmSettings) nonSecretEnvPairs() [][2]string {
	return [][2]string{
		{"ANTHROPIC_BASE_URL", g.BaseURL},
		{"ANTHROPIC_DEFAULT_OPUS_MODEL", g.OpusModel},
		{"ANTHROPIC_DEFAULT_SONNET_MODEL", g.SonnetModel},
		{"ANTHROPIC_DEFAULT_HAIKU_MODEL", g.HaikuModel},
		{"CLAUDE_CODE_AUTO_COMPACT_WINDOW", strconv.Itoa(g.AutoCompactWindow)},
	}
}

type glmSettingsStore struct {
	path string
	mu   sync.Mutex
	s    glmSettings
}

// newGlmSettingsStore loads (or initializes with defaults) the GLM settings file
// under dataDir. A missing file is not an error — defaults apply. A malformed file
// is non-fatal (logged, defaults used): GLM config is non-critical to daemon
// startup, and the token comes from ~/.config/glm/env regardless. Never returns
// nil so callers can use it unconditionally.
func newGlmSettingsStore(dataDir string) *glmSettingsStore {
	st := &glmSettingsStore{path: filepath.Join(dataDir, "glm_settings.json")}
	st.s.applyDefaults()
	if b, err := os.ReadFile(st.path); err == nil && len(b) > 0 {
		var loaded glmSettings
		if jerr := json.Unmarshal(b, &loaded); jerr != nil {
			log.Printf("glm settings: parse %s failed, using defaults: %v", st.path, jerr)
		} else {
			loaded.applyDefaults()
			st.s = loaded
		}
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		log.Printf("glm settings: read %s failed, using defaults: %v", st.path, err)
	}
	return st
}

// Snapshot returns a copy of the current non-secret config (Key included for
// resolveKey's use, but callers must never log it).
func (s *glmSettingsStore) Snapshot() glmSettings {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.s
}

// resolveKey returns the live GLM token: the store's Key if non-empty, else
// ZAI_API_KEY read live from ~/.config/glm/env. Reading the file each call means a
// rotated key applies on the next spawn with no stale copy. Rejects a comma-bearing
// key (defensive). Returns errGlmKeyMissing when no key is found anywhere.
func (s *glmSettingsStore) resolveKey() (string, error) {
	s.mu.Lock()
	k := strings.TrimSpace(s.s.Key)
	s.mu.Unlock()
	if k == "" {
		var err error
		if k, err = readZaiKeyFromEnvFile(); err != nil {
			return "", err
		}
	}
	if strings.Contains(k, ",") {
		return "", errGlmKeyComma
	}
	return k, nil
}

// KeyConfigured reports whether a usable GLM token resolves right now. Used by the
// createPane 400-gate so a glm request without a key fails fast and clearly.
func (s *glmSettingsStore) KeyConfigured() bool {
	_, err := s.resolveKey()
	return err == nil
}

// readZaiKeyFromEnvFile parses ZAI_API_KEY from ~/.config/glm/env (the same file
// the `glm` shell wrapper reads). Skips #-comments and blank lines; strips optional
// surrounding quotes; ignores the install placeholder. Returns errGlmKeyMissing if
// no usable value is present.
func readZaiKeyFromEnvFile() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	f, err := os.Open(filepath.Join(home, ".config", "glm", "env"))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", errGlmKeyMissing
		}
		return "", fmt.Errorf("read glm env file: %w", err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		eq := strings.IndexByte(line, '=')
		if eq < 0 || strings.TrimSpace(line[:eq]) != "ZAI_API_KEY" {
			continue
		}
		val := strings.TrimSpace(line[eq+1:])
		val = strings.Trim(val, `"'`)
		if val != "" && val != "PASTE_YOUR_ZAI_KEY_HERE" {
			return val, nil
		}
	}
	if err := sc.Err(); err != nil {
		return "", err
	}
	return "", errGlmKeyMissing
}

// providerEnvVars builds the FULL GLM env var set (the 5 non-secret vars PLUS
// ANTHROPIC_AUTH_TOKEN) for the AGENT to set on the child's c.Env. It loads the
// non-secret config from <dataDir>/glm_settings.json (defaults if absent) and
// resolves the token live. Returns an error (no partial set) when no key is
// configured, so the agent fails loudly instead of spawning claude with a blank
// token. The token appears ONLY in the returned slice — which goes straight onto
// c.Env, never into argv/logs.
func providerEnvVars(dataDir string) ([]string, error) {
	st := newGlmSettingsStore(dataDir)
	key, err := st.resolveKey()
	if err != nil {
		return nil, err
	}
	g := st.Snapshot()
	out := make([]string, 0, 6)
	for _, p := range g.nonSecretEnvPairs() {
		out = append(out, p[0]+"="+p[1])
	}
	out = append(out, "ANTHROPIC_AUTH_TOKEN="+key)
	return out, nil
}

// glmConflictEnvKeys are the env var NAMES a GLM pane must not inherit from the
// daemon's environment: they would shadow the GLM token (Claude Code prefers
// ANTHROPIC_API_KEY over ANTHROPIC_AUTH_TOKEN) or redirect the routing. The agent
// filters these out of the inherited env before appending the GLM set, so the GLM
// vars are the SOLE occurrence (Linux getenv returns the first duplicate).
var glmConflictEnvKeys = []string{
	"ANTHROPIC_API_KEY",
	"ANTHROPIC_AUTH_TOKEN",
	"ANTHROPIC_BASE_URL",
	"ANTHROPIC_DEFAULT_OPUS_MODEL",
	"ANTHROPIC_DEFAULT_SONNET_MODEL",
	"ANTHROPIC_DEFAULT_HAIKU_MODEL",
	"CLAUDE_CODE_AUTO_COMPACT_WINDOW",
}

// filterOutEnvKeys returns env without any KEY=VALUE entry whose KEY is in drop.
func filterOutEnvKeys(env []string, drop []string) []string {
	dropSet := make(map[string]bool, len(drop))
	for _, k := range drop {
		dropSet[k] = true
	}
	out := make([]string, 0, len(env))
	for _, kv := range env {
		key := kv
		if i := strings.IndexByte(kv, '='); i >= 0 {
			key = kv[:i]
		}
		if dropSet[key] {
			continue
		}
		out = append(out, kv)
	}
	return out
}
