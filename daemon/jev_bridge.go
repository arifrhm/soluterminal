package main

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"soluterminal-shared/typesafe"
	"soluterminal/jev"
)

const (
	jevBridgePath                = "/api/jev/decide"
	jevBridgeEnableEnv           = "SOLU_JEV_BRIDGE"
	jevBridgeModelEnv            = "SOLU_JEV_BRIDGE_MODEL"
	jevBridgeEnableMarker        = "jev_bridge.enabled"
	jevBridgeTokenFile           = "jev-bridge-token"
	jevBridgeDiscoveryFile       = "jev-bridge.json"
	jevBridgeDefaultModel        = "jev-1.13.0"
	jevBridgeBodyLimit     int64 = 256 << 10
	jevBridgeRateLimit           = 60
	jevBridgeRateWindow          = 60 * time.Second
	jevBridgeTimeout             = 5 * time.Second
)

var jevBridgeModelPattern = regexp.MustCompile(`^jev-[0-9]+\.[0-9]+\.[0-9]+$`)

type jevSystemOneClient interface {
	SystemOneModel(context.Context, string, any, map[string]typesafe.Question) (*typesafe.Response, error)
}

type jevBridge struct {
	dataDir string
	logger  *log.Logger

	mu       sync.RWMutex
	enabled  bool
	token    string
	endpoint string

	secretSource   func() secretResolver
	clientFactory  func(string) jevSystemOneClient
	limiter        *jevBridgeLimiter
	now            func() time.Time
	timeout        time.Duration
	reconcileEvery time.Duration

	lifecycleMu sync.Mutex
	cancel      context.CancelFunc
	wg          sync.WaitGroup
}

type jevBridgeLimiter struct {
	mu     sync.Mutex
	start  time.Time
	count  int
	limit  int
	window time.Duration
}

type jevBridgeDiscovery struct {
	URL   string `json:"url"`
	Token string `json:"token"`
}

type jevBridgeResponse struct {
	Model       string                     `json:"model"`
	LatencyMS   int64                      `json:"latency_ms"`
	InputTokens int64                      `json:"input_tokens"`
	Answers     jev.BrowserDecisionAnswers `json:"answers"`
	Advisory    bool                       `json:"advisory"`
}

type jevBridgeAudit struct {
	kind        string
	candidates  int
	model       string
	latencyMS   int64
	inputTokens int64
	outcome     string
}

func newJevBridge(dataDir string, secretSource func() secretResolver, logger *log.Logger) *jevBridge {
	if logger == nil {
		logger = log.Default()
	}
	b := &jevBridge{
		dataDir:        dataDir,
		logger:         logger,
		secretSource:   secretSource,
		now:            time.Now,
		timeout:        jevBridgeTimeout,
		reconcileEvery: time.Second,
	}
	b.limiter = &jevBridgeLimiter{limit: jevBridgeRateLimit, window: jevBridgeRateWindow}
	b.clientFactory = func(apiKey string) jevSystemOneClient {
		client := typesafe.New(apiKey)
		if base := strings.TrimSpace(os.Getenv(typesafe.EnvBaseURL)); base != "" {
			client.BaseURL = strings.TrimSuffix(base, "/")
		}
		client.UserAgent = "soluterminal-jev-bridge/1"
		client.Retry = typesafe.RetryPolicy{
			MaxAttempts:             2,
			RetryStatusCodes:        []int{http.StatusTooManyRequests},
			DisableTransportRetries: true,
		}
		return client
	}
	return b
}

func (b *jevBridge) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	started := time.Now()
	audit := jevBridgeAudit{kind: "-", model: "-", outcome: "internal_error"}
	defer func() {
		audit.latencyMS = time.Since(started).Milliseconds()
		if audit.latencyMS < 0 {
			audit.latencyMS = 0
		}
		b.audit(audit)
	}()

	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		audit.outcome = "method_not_allowed"
		writeJevBridgeAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "POST required")
		return
	}
	enabled, scopedToken := b.authSnapshot()
	if !enabled {
		audit.outcome = "disabled"
		writeJevBridgeAPIError(w, http.StatusForbidden, "jev_bridge_disabled", "Jev decision bridge is disabled")
		return
	}
	if !validJevBridgeBearer(r.Header.Get("Authorization"), scopedToken) {
		audit.outcome = "unauthorized"
		writeJevBridgeAPIError(w, http.StatusUnauthorized, "unauthorized", "dedicated Jev bridge bearer required")
		return
	}
	// The central mux is also served over the authenticated cloud tunnel,
	// which deliberately bypasses the local auth gate. Keep this discovery-
	// based browser bridge local even when a relay caller has obtained the
	// dedicated bearer.
	if requestViaRelay(r) {
		audit.outcome = "relay_rejected"
		writeJevBridgeAPIError(w, http.StatusForbidden, "jev_bridge_local_only", "Jev decision bridge is local-only")
		return
	}

	var input jev.BrowserDecisionInput
	if err := decodeJevBridgeRequest(w, r, &input); err != nil {
		audit.outcome = "invalid_request"
		if errors.Is(err, errJevBridgeBodyTooLarge) {
			audit.outcome = "body_too_large"
			writeJevBridgeAPIError(w, http.StatusRequestEntityTooLarge, "request_too_large", "request body exceeds 256 KiB")
		} else {
			writeJevBridgeAPIError(w, http.StatusBadRequest, "invalid_request", "invalid Jev decision request")
		}
		return
	}
	audit.kind = sanitizedJevBridgeKind(input.Kind)
	audit.candidates = len(input.Candidates)

	model := strings.TrimSpace(os.Getenv(jevBridgeModelEnv))
	if model == "" {
		model = jevBridgeDefaultModel
	}
	if !jevBridgeModelPattern.MatchString(model) {
		audit.outcome = "unavailable"
		writeJevBridgeAPIError(w, http.StatusServiceUnavailable, "jev_bridge_unavailable", "Jev bridge model configuration is unavailable")
		return
	}
	request, err := jev.BrowserDecisionRequest(input, model)
	if err != nil {
		audit.outcome = "invalid_request"
		writeJevBridgeAPIError(w, http.StatusBadRequest, "invalid_request", "invalid Jev decision request")
		return
	}

	var source secretResolver
	if b.secretSource != nil {
		source = b.secretSource()
	}
	if source == nil {
		audit.outcome = "unavailable"
		writeJevBridgeAPIError(w, http.StatusServiceUnavailable, "jev_bridge_unavailable", "Jev bridge credentials are unavailable")
		return
	}
	apiKey, ok, secretErr := source.Resolve(jevTypesafeRuntimeID, jevTypesafeSecretEnv)
	if secretErr != nil || !ok || strings.TrimSpace(apiKey) == "" {
		audit.outcome = "unavailable"
		writeJevBridgeAPIError(w, http.StatusServiceUnavailable, "jev_bridge_unavailable", "Jev bridge credentials are unavailable")
		return
	}

	allowed, retryAfter := b.limiter.admit(b.now())
	if !allowed {
		w.Header().Set("Retry-After", fmt.Sprintf("%d", retryAfter))
		audit.outcome = "rate_limited"
		writeJevBridgeAPIError(w, http.StatusTooManyRequests, "rate_limited", "Jev bridge rate limit exceeded")
		return
	}
	if b.clientFactory == nil {
		audit.outcome = "unavailable"
		writeJevBridgeAPIError(w, http.StatusServiceUnavailable, "jev_bridge_unavailable", "Jev bridge provider is unavailable")
		return
	}
	client := b.clientFactory(apiKey)
	if client == nil {
		audit.outcome = "unavailable"
		writeJevBridgeAPIError(w, http.StatusServiceUnavailable, "jev_bridge_unavailable", "Jev bridge provider is unavailable")
		return
	}

	timeout := b.timeout
	if timeout <= 0 {
		timeout = jevBridgeTimeout
	}
	callCtx, cancel := context.WithTimeout(r.Context(), timeout)
	providerStarted := time.Now()
	response, callErr := client.SystemOneModel(callCtx, request.Model, request.State, request.Questions)
	providerFinished := time.Now()
	providerLatency := providerFinished.Sub(providerStarted).Milliseconds()
	callContextErr := callCtx.Err()
	// Context timer callbacks can run late under CPU pressure. A client that
	// ignores cancellation must not turn that scheduling gap into a successful
	// response after the absolute deadline has already passed.
	if callContextErr == nil {
		if deadline, ok := callCtx.Deadline(); ok && !providerFinished.Before(deadline) {
			callContextErr = context.DeadlineExceeded
		}
	}
	cancel()
	if providerLatency < 0 {
		providerLatency = 0
	}
	if callErr != nil || callContextErr != nil {
		if errors.Is(callContextErr, context.DeadlineExceeded) || errors.Is(callErr, context.DeadlineExceeded) {
			audit.outcome = "timeout"
			writeJevBridgeAPIError(w, http.StatusGatewayTimeout, "provider_timeout", "Jev provider deadline exceeded")
		} else {
			audit.outcome = "provider_error"
			writeJevBridgeAPIError(w, http.StatusBadGateway, "provider_error", "Jev provider request failed")
		}
		return
	}
	if response == nil || response.Model != model || response.Usage.InputTokens < 0 {
		audit.outcome = "provider_error"
		writeJevBridgeAPIError(w, http.StatusBadGateway, "provider_error", "Jev provider returned an invalid response")
		return
	}
	audit.model = model
	audit.inputTokens = response.Usage.InputTokens
	answers, err := jev.InterpretBrowserDecision(input, response)
	if err != nil {
		audit.outcome = "provider_error"
		writeJevBridgeAPIError(w, http.StatusBadGateway, "provider_error", "Jev provider returned an invalid response")
		return
	}
	audit.outcome = "success"
	writeJSON(w, http.StatusOK, jevBridgeResponse{
		Model: model, LatencyMS: providerLatency, InputTokens: response.Usage.InputTokens,
		Answers: answers, Advisory: true,
	})
}

func (b *jevBridge) audit(event jevBridgeAudit) {
	if b == nil || b.logger == nil {
		return
	}
	b.logger.Printf("jev_bridge_audit kind=%s candidates=%d model=%s latency_ms=%d input_tokens=%d outcome=%s",
		event.kind, event.candidates, event.model, event.latencyMS, event.inputTokens, event.outcome)
}

func (b *jevBridge) authSnapshot() (bool, string) {
	if b == nil {
		return false, ""
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.enabled, b.token
}

func validJevBridgeBearer(header, token string) bool {
	const prefix = "Bearer "
	if token == "" || !strings.HasPrefix(header, prefix) {
		return false
	}
	provided := strings.TrimPrefix(header, prefix)
	if provided == "" || strings.TrimSpace(provided) != provided {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(provided), []byte(token)) == 1
}

var errJevBridgeBodyTooLarge = errors.New("Jev bridge request body too large")

func decodeJevBridgeRequest(w http.ResponseWriter, r *http.Request, dst any) error {
	if r.ContentLength > jevBridgeBodyLimit {
		return errJevBridgeBodyTooLarge
	}
	reader := http.MaxBytesReader(w, r.Body, jevBridgeBodyLimit)
	decoder := json.NewDecoder(reader)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			return errJevBridgeBodyTooLarge
		}
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			return errJevBridgeBodyTooLarge
		}
		return errors.New("request must contain exactly one JSON value")
	}
	return nil
}

func sanitizedJevBridgeKind(kind string) string {
	switch kind {
	case jev.DecisionTargetRank, jev.DecisionActionOutcome, jev.DecisionSuspiciousInstruction:
		return kind
	default:
		return "-"
	}
}

func writeJevBridgeAPIError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]any{"error": map[string]string{"code": code, "message": message}})
}

func (l *jevBridgeLimiter) admit(now time.Time) (bool, int) {
	if l == nil {
		return false, 1
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.limit <= 0 {
		l.limit = jevBridgeRateLimit
	}
	if l.window <= 0 {
		l.window = jevBridgeRateWindow
	}
	if l.start.IsZero() || now.Before(l.start) || !now.Before(l.start.Add(l.window)) {
		l.start = now
		l.count = 0
	}
	if l.count < l.limit {
		l.count++
		return true, 0
	}
	remaining := l.start.Add(l.window).Sub(now)
	seconds := int(math.Ceil(remaining.Seconds()))
	if seconds < 1 {
		seconds = 1
	}
	return false, seconds
}

// Start reconciles discovery before the HTTP server starts accepting traffic,
// then checks the default-off env/marker gate once per second.
func (b *jevBridge) Start(endpoint string) error {
	if b == nil {
		return errors.New("Jev bridge unavailable")
	}
	b.lifecycleMu.Lock()
	defer b.lifecycleMu.Unlock()
	if b.cancel != nil {
		return errors.New("Jev bridge already started")
	}
	b.mu.Lock()
	b.endpoint = endpoint
	b.mu.Unlock()
	firstErr := b.reconcile()
	ctx, cancel := context.WithCancel(context.Background())
	b.cancel = cancel
	b.wg.Add(1)
	go func() {
		defer b.wg.Done()
		interval := b.reconcileEvery
		if interval <= 0 {
			interval = time.Second
		}
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := b.reconcile(); err != nil && b.logger != nil {
					b.logger.Printf("jev_bridge_lifecycle outcome=unavailable")
				}
			}
		}
	}()
	return firstErr
}

func (b *jevBridge) Close() error {
	if b == nil {
		return nil
	}
	b.lifecycleMu.Lock()
	cancel := b.cancel
	b.cancel = nil
	b.lifecycleMu.Unlock()
	if cancel != nil {
		cancel()
		b.wg.Wait()
	}
	return b.disableAndRemoveDiscovery(nil)
}

func (b *jevBridge) reconcile() error {
	if !b.shouldEnable() {
		return b.disableAndRemoveDiscovery(nil)
	}
	b.mu.RLock()
	endpoint := b.endpoint
	b.mu.RUnlock()
	if strings.TrimSpace(endpoint) == "" {
		return b.disableAndRemoveDiscovery(errors.New("Jev bridge endpoint unavailable"))
	}
	token, err := loadOrCreateNamedToken(b.dataDir, jevBridgeTokenFile)
	if err != nil {
		return b.disableAndRemoveDiscovery(err)
	}
	if err := os.Chmod(filepath.Join(b.dataDir, jevBridgeTokenFile), 0o600); err != nil {
		return b.disableAndRemoveDiscovery(err)
	}
	discovery, err := json.Marshal(jevBridgeDiscovery{URL: endpoint, Token: token})
	if err != nil {
		return b.disableAndRemoveDiscovery(err)
	}
	discoveryPath := filepath.Join(b.dataDir, jevBridgeDiscoveryFile)
	if current, readErr := os.ReadFile(discoveryPath); readErr != nil || !jevBridgeBytesEqual(current, discovery) {
		if err := writeFileAtomicSynced(discoveryPath, discovery, 0o600); err != nil {
			return b.disableAndRemoveDiscovery(err)
		}
	} else if err := os.Chmod(discoveryPath, 0o600); err != nil {
		return b.disableAndRemoveDiscovery(err)
	}
	b.mu.Lock()
	b.token = token
	b.enabled = true
	b.mu.Unlock()
	return nil
}

func (b *jevBridge) shouldEnable() bool {
	if strings.TrimSpace(os.Getenv(jevBridgeEnableEnv)) == "1" {
		return true
	}
	_, err := os.Stat(filepath.Join(b.dataDir, jevBridgeEnableMarker))
	return err == nil
}

func (b *jevBridge) setDisabled() {
	b.mu.Lock()
	b.enabled = false
	b.token = ""
	b.mu.Unlock()
}

func (b *jevBridge) disableAndRemoveDiscovery(cause error) error {
	b.setDisabled()
	removeErr := removeIfExists(filepath.Join(b.dataDir, jevBridgeDiscoveryFile))
	if cause != nil {
		return cause
	}
	return removeErr
}

func removeIfExists(path string) error {
	err := os.Remove(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func jevBridgeBytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	return subtle.ConstantTimeCompare(a, b) == 1
}
