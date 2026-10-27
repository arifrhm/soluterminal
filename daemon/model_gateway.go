package main

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"soluterminal-shared/providers"
)

const (
	modelGatewayPrefix        = "/api/model-gateway/v1"
	modelGatewayTokenFile     = "model-gateway-token"
	modelGatewayDiscoveryFile = "model-gateway.json"
	modelGatewaySettingsFile  = "model-gateway-settings.json"
)

type modelGateway struct {
	mu                       sync.RWMutex
	dataDir, endpoint, token string
	enabled                  bool
	client                   *http.Client
	logger                   *log.Logger
	limiter                  *jevBridgeLimiter
	// localOpenAIBase is the configured local OpenAI-compatible endpoint, when
	// a settings store is built in (ok=false: none configured).
	localOpenAIBase func() (string, bool)
	harnessStore             func(string) *harnessCredStore
	harnessScope             func(string) string
	// Tests may replace a provider endpoint without changing production facts.
	upstreamOverride map[string]string
}

func newModelGateway(dataDir string, logger *log.Logger) *modelGateway {
	if logger == nil {
		logger = log.Default()
	}
	g := &modelGateway{dataDir: dataDir, client: &http.Client{Timeout: 0, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, logger: logger,
		limiter: &jevBridgeLimiter{limit: 120, window: time.Minute}}
	// Side-port verification may supply a loopback fake DeepSeek upstream.
	// Both registration opt-outs are required so this never affects a normal
	// daemon, and the override cannot target a remote service.
	if os.Getenv("SOLU_SKIP_MCP_REGISTER") == "1" && os.Getenv("SOLU_SKIP_HOOK_REGISTER") == "1" {
		if raw := os.Getenv("SOLU_MODEL_GATEWAY_TEST_DEEPSEEK_URL"); raw != "" {
			if parsed, err := url.Parse(raw); err == nil && parsed.Scheme == "http" && parsed.User == nil && parsed.RawQuery == "" && parsed.Fragment == "" {
				if ip := net.ParseIP(parsed.Hostname()); ip != nil && ip.IsLoopback() {
					g.upstreamOverride = map[string]string{"deepseek": strings.TrimRight(raw, "/")}
				}
			}
		}
	}
	return g
}

func (g *modelGateway) store(slug string) (*harnessCredStore, error) {
	var s *harnessCredStore
	if g.harnessStore != nil {
		s = g.harnessStore(slug)
	} else {
		s = newHarnessCredStore(g.dataDir, slug)
	}
	if g.harnessScope != nil {
		if err := s.bindScope(g.harnessScope(slug)); err != nil {
			return nil, err
		}
	}
	return s, nil
}

func (g *modelGateway) settingsPath() string {
	return filepath.Join(g.dataDir, modelGatewaySettingsFile)
}

func (g *modelGateway) Start(endpoint string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.endpoint = endpoint
	var settings struct {
		ModelGateway struct {
			Enabled bool `json:"enabled"`
		} `json:"modelGateway"`
	}
	raw, err := os.ReadFile(g.settingsPath())
	if err == nil {
		if json.Unmarshal(raw, &settings) != nil {
			return errors.New("model gateway settings malformed")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return g.setEnabledLocked(settings.ModelGateway.Enabled)
}

func (g *modelGateway) SetEnabled(enabled bool) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if enabled && g.endpoint == "" {
		return errors.New("model gateway endpoint unavailable")
	}
	if err := g.setEnabledLocked(enabled); err != nil {
		return err
	}
	settings := map[string]any{"modelGateway": map[string]bool{"enabled": enabled}}
	raw, _ := json.Marshal(settings)
	return writeFileAtomicSynced(g.settingsPath(), raw, 0600)
}

func (g *modelGateway) setEnabledLocked(enabled bool) error {
	if !enabled {
		g.enabled, g.token = false, ""
		if err := removeIfExists(filepath.Join(g.dataDir, modelGatewayDiscoveryFile)); err != nil {
			return err
		}
		return removeIfExists(filepath.Join(g.dataDir, modelGatewayTokenFile))
	}
	if g.endpoint == "" {
		return errors.New("model gateway endpoint unavailable")
	}
	token, err := loadOrCreateNamedToken(g.dataDir, modelGatewayTokenFile)
	if err != nil {
		return err
	}
	if err := os.Chmod(filepath.Join(g.dataDir, modelGatewayTokenFile), 0600); err != nil {
		return err
	}
	discovery, _ := json.Marshal(map[string]any{"version": 1, "url": g.endpoint, "token": token})
	if err := writeFileAtomicSynced(filepath.Join(g.dataDir, modelGatewayDiscoveryFile), discovery, 0600); err != nil {
		return err
	}
	g.token, g.enabled = token, true
	return nil
}

func (g *modelGateway) status(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		g.mu.RLock()
		enabled := g.enabled
		g.mu.RUnlock()
		writeJSON(w, 200, map[string]any{"modelGateway": map[string]bool{"enabled": enabled}})
		return
	}
	if r.Method != http.MethodPut {
		w.WriteHeader(405)
		return
	}
	if fileAccessOverRelay(r) {
		http.Error(w, "local-only setting", 403)
		return
	}
	var req struct {
		ModelGateway struct {
			Enabled bool `json:"enabled"`
		} `json:"modelGateway"`
	}
	if !harnessDecode(w, r, &req) {
		return
	}
	if err := g.SetEnabled(req.ModelGateway.Enabled); err != nil {
		http.Error(w, "gateway setting unavailable", 500)
		return
	}
	writeJSON(w, 200, map[string]any{"modelGateway": map[string]bool{"enabled": req.ModelGateway.Enabled}})
}

type gatewayAccount struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Selected bool   `json:"selected"`
}

type gatewayProvider struct {
	Provider   string           `json:"provider"`
	Protocol   string           `json:"protocol"`
	Accounts   []gatewayAccount `json:"accounts"`
	KeyPresent bool             `json:"keyPresent"`
}

func (g *modelGateway) providers() []gatewayProvider {
	out := make([]gatewayProvider, 0)
	for _, slug := range providers.GatewaySlugs() {
		facts, _ := providers.Gateway(slug)
		row := gatewayProvider{Provider: slug, Protocol: facts.Protocol, Accounts: []gatewayAccount{}}
		if p, ok := providers.Get(slug); ok && p.KeyAPI != nil {
			if accounts, err := newAPIKeyAccountStore(g.dataDir, slug).list(); err == nil {
				for _, account := range accounts {
					row.Accounts = append(row.Accounts, gatewayAccount{ID: account["id"].(string), Name: account["name"].(string), Selected: account["selected"].(bool)})
				}
			}
		} else if p, ok := providers.Get(slug); ok && p.Harness != nil {
			if store, err := g.store(slug); err == nil {
				if accounts, err := store.list(); err == nil {
					for _, account := range accounts {
						if account.Source != "personal" {
							continue // team credentials are exposed only through the scoped broker
						}
						row.Accounts = append(row.Accounts, gatewayAccount{ID: account.Source + "/" + account.Name, Name: account.Name, Selected: account.Selected})
					}
				}
			}
		}
		if facts.RuntimeID != "" {
			store := openRuntimeSecrets(g.dataDir)
			if key, ok, err := store.Resolve(facts.RuntimeID, facts.SecretEnv); err == nil && ok && key != "" {
				selected := true
				for _, account := range row.Accounts {
					if account.Selected {
						selected = false
						break
					}
				}
				row.Accounts = append(row.Accounts, gatewayAccount{ID: "runtime", Name: "Runtime key", Selected: selected})
			}
		}
		row.KeyPresent = len(row.Accounts) > 0
		out = append(out, row)
	}
	return out
}

func (g *modelGateway) credential(slug, accountID string) (id, key, base string, err error) {
	facts, ok := providers.Gateway(slug)
	if !ok {
		return "", "", "", errors.New("unsupported provider")
	}
	base = facts.BaseURL
	if slug == "local-openai" && g.localOpenAIBase != nil {
		if local, ok := g.localOpenAIBase(); ok {
			base = local
		}
	}
	if g.upstreamOverride != nil && g.upstreamOverride[slug] != "" {
		base = g.upstreamOverride[slug]
	}
	if p, ok := providers.Get(slug); ok && p.KeyAPI != nil {
		name := ""
		if accountID != "" {
			if !strings.HasPrefix(accountID, "personal/") {
				return "", "", "", errors.New("unknown account")
			}
			name = strings.TrimPrefix(accountID, "personal/")
		}
		name, key, err = newAPIKeyAccountStore(g.dataDir, slug).selected(name)
		return "personal/" + name, key, base, err
	}
	if p, ok := providers.Get(slug); ok && p.Harness != nil && accountID != "runtime" {
		s, storeErr := g.store(slug)
		if storeErr != nil {
			return "", "", "", storeErr
		}
		var source, name string
		var acc harnessAccount
		if accountID == "" {
			source, name, acc, err = s.selected()
		} else {
			parts := strings.Split(accountID, "/")
			if len(parts) != 2 || parts[0] != "personal" {
				return "", "", "", errors.New("unknown account")
			}
			source, name = parts[0], parts[1]
			acc, err = s.account(source, name)
		}
		if source == "team" {
			return "", "", "", errors.New("team accounts require the scoped broker")
		}
		if err == nil {
			key = acc.APIKey
			if b, e := harnessEndpoint(slug, acc.Billing); e == nil && g.upstreamOverride[slug] == "" {
				base = b
			}
		}
		if err == nil {
			return source + "/" + name, key, base, nil
		}
	}
	if facts.RuntimeID != "" && (accountID == "" || accountID == "runtime") {
		key, ok, resolveErr := openRuntimeSecrets(g.dataDir).Resolve(facts.RuntimeID, facts.SecretEnv)
		if resolveErr == nil && ok && key != "" {
			return "runtime", key, base, nil
		}
	}
	return "", "", "", errors.New("credential unavailable")
}

type gatewayCountWriter struct {
	http.ResponseWriter
	status, bytes int
}

func (w *gatewayCountWriter) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
		w.ResponseWriter.WriteHeader(status)
	}
}
func (w *gatewayCountWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(200)
	}
	n, err := w.ResponseWriter.Write(b)
	w.bytes += n
	return n, err
}
func (w *gatewayCountWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

type gatewayFlushWriter struct{ w *gatewayCountWriter }

func (f gatewayFlushWriter) Write(b []byte) (int, error) {
	n, err := f.w.Write(b)
	f.w.Flush()
	return n, err
}

func (g *modelGateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	started := time.Now()
	counted := &gatewayCountWriter{ResponseWriter: w}
	provider, account, route := "-", "-", "-"
	defer func() {
		status := counted.status
		if status == 0 {
			status = 200
		}
		g.logger.Printf("model_gateway provider=%s account=%s route=%s status=%d bytes=%d duration_ms=%d", provider, account, route, status, counted.bytes, time.Since(started).Milliseconds())
	}()
	g.mu.RLock()
	enabled, token := g.enabled, g.token
	g.mu.RUnlock()
	if !enabled || token == "" {
		http.Error(counted, "gateway disabled", 403)
		return
	}
	if requestViaRelay(r) {
		http.Error(counted, "gateway is local-only", 403)
		return
	}
	provided := ""
	if auth := r.Header.Get("Authorization"); strings.HasPrefix(auth, "Bearer ") {
		provided = strings.TrimPrefix(auth, "Bearer ")
	}
	if provided == "" {
		provided = r.Header.Get("x-api-key")
	}
	if subtle.ConstantTimeCompare([]byte(provided), []byte(token)) != 1 {
		http.Error(counted, "gateway token required", 403)
		return
	}
	if allowed, retry := g.limiter.admit(time.Now()); !allowed {
		counted.Header().Set("Retry-After", fmt.Sprint(retry))
		http.Error(counted, "rate limited", 429)
		return
	}
	if r.URL.Path == modelGatewayPrefix+"/providers" && r.Method == http.MethodGet {
		route = "providers"
		writeJSON(counted, 200, g.providers())
		return
	}
	rel := strings.TrimPrefix(r.URL.Path, modelGatewayPrefix+"/")
	parts := strings.Split(rel, "/")
	if len(parts) < 4 {
		http.NotFound(counted, r)
		return
	}
	candidate := parts[0]
	facts, ok := providers.Gateway(candidate)
	if !ok || parts[1] != facts.Protocol || parts[2] != "v1" {
		http.NotFound(counted, r)
		return
	}
	candidateRoute := strings.Join(parts[3:], "/")
	if !((candidateRoute == "models" && r.Method == http.MethodGet) || (candidateRoute == "messages" && facts.Protocol == "anthropic" && r.Method == http.MethodPost) || (candidateRoute == "chat/completions" && facts.Protocol == "openai" && r.Method == http.MethodPost)) {
		http.NotFound(counted, r)
		return
	}
	provider, route = candidate, candidateRoute
	accountID := r.Header.Get("X-Solu-Account")
	account, key, base, err := g.credential(provider, accountID)
	if err != nil || base == "" {
		account = "-"
		http.Error(counted, "credential unavailable", 503)
		return
	}
	base = strings.TrimRight(base, "/")
	if facts.Protocol == "anthropic" {
		base += "/v1"
	}
	upstream, err := http.NewRequestWithContext(r.Context(), r.Method, base+"/"+route, r.Body)
	if err != nil {
		http.Error(counted, "upstream unavailable", 502)
		return
	}
	if facts.Protocol == "anthropic" {
		upstream.Header.Set("x-api-key", key)
		if version := r.Header.Get("anthropic-version"); version != "" {
			upstream.Header.Set("anthropic-version", version)
		} else {
			upstream.Header.Set("anthropic-version", "2023-06-01")
		}
		if beta := r.Header.Get("anthropic-beta"); beta != "" {
			upstream.Header.Set("anthropic-beta", beta)
		}
	} else {
		upstream.Header.Set("Authorization", "Bearer "+key)
	}
	if contentType := r.Header.Get("Content-Type"); contentType != "" {
		upstream.Header.Set("Content-Type", contentType)
	}
	client := g.client
	if client == nil {
		client = http.DefaultClient
	}
	response, err := client.Do(upstream)
	if err != nil {
		http.Error(counted, "upstream unavailable", 502)
		return
	}
	defer response.Body.Close()
	if value := response.Header.Get("Content-Type"); value != "" {
		counted.Header().Set("Content-Type", value)
	}
	if value := response.Header.Get("Retry-After"); value != "" {
		counted.Header().Set("Retry-After", value)
	}
	counted.WriteHeader(response.StatusCode)
	_, _ = io.Copy(gatewayFlushWriter{counted}, response.Body)
}
