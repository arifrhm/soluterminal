// Package typesafe is a small, dependency-free client for TypeSafe's System
// One HTTP API — the "Jev" model.
//
// Jev is not a chat completion endpoint. It evaluates a single piece of
// "state" (a string, JSON object, or array) against a map of typed questions
// and returns typed answers your code can branch on directly. There is no
// generated prose to parse.
//
//	POST /v1/systemone  -> {model, answers, usage}
//	GET  /v1/models     -> {models: [{name, description, release_date}]}
//
// The package is stdlib-only on purpose: it lives in soluterminal-shared so
// both the daemon and the cloud control plane can import it, and it must not
// add a dependency to that module.
//
// Wire contract: https://docs.typesafe.ai/api
package typesafe

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	// DefaultBaseURL is the hosted TypeSafe API.
	DefaultBaseURL = "https://api.typesafe.ai"
	// DefaultModel is the docs' recommended alias. Pin a versioned ID
	// (e.g. "jev-1.13.0") if you have tuned thresholds against it.
	DefaultModel = "jev-latest"

	// EnvAPIKey is the environment variable the official SDKs also read.
	EnvAPIKey = "TYPESAFE_API_KEY"
	// EnvBaseURL overrides the API base URL (self-hosted/testing).
	EnvBaseURL = "TYPESAFE_BASE_URL"
	// EnvModel overrides the default model.
	EnvModel = "TYPESAFE_MODEL"

	// PricePerBillionInputTokensUSD is Jev 1.13 list pricing. Output tokens
	// are free. It is advisory telemetry only; confirm current pricing at
	// https://docs.typesafe.ai/models before billing on it.
	PricePerBillionInputTokensUSD = 42.0

	// MaxStateAndQuestionsTokens is the documented budget for the state plus
	// the single longest question (~32k tokens).
	MaxStateAndQuestionsTokens = 32000
	// MaxTotalContextTokens is the documented budget for the state plus all
	// questions (~64k tokens).
	MaxTotalContextTokens = 64000
	// MaxChoiceOptions is the documented maximum option count for a Choice.
	MaxChoiceOptions = 255
	// MinScoreLevels and MaxScoreLevels bound a Score's criteria array.
	MinScoreLevels = 2
	MaxScoreLevels = 10

	// DefaultModelHTTPTimeout is deliberately generous: a single evaluation
	// is fast, but the client also serves as a last line of defence.
	DefaultModelHTTPTimeout = 30 * time.Second
)

// RetryPolicy configures transport-level retries. A zero value is valid and
// yields the defaults; only 429/529, 5xx, and transport errors are retried.
type RetryPolicy struct {
	// MaxAttempts counts the first try. <= 0 means DefaultMaxAttempts.
	MaxAttempts int
	BaseDelay   time.Duration
	MaxDelay    time.Duration
	// RetryStatusCodes, when non-nil, is the exact set of HTTP statuses that
	// may be retried. Nil preserves the legacy 429/529/5xx policy. An empty,
	// non-nil slice disables status retries.
	RetryStatusCodes []int
	// DisableTransportRetries makes a transport error final. The zero value
	// preserves the legacy behavior of retrying transport errors.
	DisableTransportRetries bool
}

// DefaultMaxAttempts matches the official SDKs' retry posture.
const DefaultMaxAttempts = 3

func (p RetryPolicy) normalized() RetryPolicy {
	if p.MaxAttempts <= 0 {
		p.MaxAttempts = DefaultMaxAttempts
	}
	if p.BaseDelay <= 0 {
		p.BaseDelay = 500 * time.Millisecond
	}
	if p.MaxDelay <= 0 {
		p.MaxDelay = 8 * time.Second
	}
	return p
}

func (p RetryPolicy) retryableStatus(status int) bool {
	if p.RetryStatusCodes == nil {
		return status == http.StatusTooManyRequests || status == 529 || status >= 500
	}
	for _, allowed := range p.RetryStatusCodes {
		if status == allowed {
			return true
		}
	}
	return false
}

// Client is a TypeSafe System One client. It is safe for concurrent use.
type Client struct {
	APIKey     string
	BaseURL    string
	HTTPClient *http.Client
	Retry      RetryPolicy
	UserAgent  string
}

// New returns a client for apiKey against the hosted API.
func New(apiKey string) *Client {
	return &Client{
		APIKey:     strings.TrimSpace(apiKey),
		BaseURL:    DefaultBaseURL,
		HTTPClient: &http.Client{Timeout: DefaultModelHTTPTimeout},
		UserAgent:  "soluterminal-typesafe/1",
	}
}

// NewFromEnv builds a client from TYPESAFE_API_KEY / TYPESAFE_BASE_URL /
// TYPESAFE_MODEL. It returns ErrNoAPIKey when no key is configured so callers
// can treat "not configured" as a soft, non-fatal state.
func NewFromEnv() (*Client, error) {
	key := strings.TrimSpace(os.Getenv(EnvAPIKey))
	if key == "" {
		return nil, ErrNoAPIKey
	}
	c := New(key)
	if base := strings.TrimSpace(os.Getenv(EnvBaseURL)); base != "" {
		c.BaseURL = strings.TrimSuffix(base, "/")
	}
	return c, nil
}

// Configured reports whether an API key is present in the environment.
func Configured() bool {
	return strings.TrimSpace(os.Getenv(EnvAPIKey)) != ""
}

// ErrNoAPIKey is returned by NewFromEnv when TYPESAFE_API_KEY is unset.
var ErrNoAPIKey = errors.New("typesafe: TYPESAFE_API_KEY is not set")

// ---------------------------------------------------------------------------
// Questions
// ---------------------------------------------------------------------------

// Question is one typed evaluation question. Type is "choice", "score", or
// "noul". Instructions and Criteria accept arbitrary JSON values (string,
// object, or array) per the API's structure rules.
type Question struct {
	Type         string `json:"type"`
	Instructions any    `json:"instructions"`
	Criteria     any    `json:"criteria,omitempty"`
}

// Noul asks a yes/no question and returns the probability that the answer is
// yes. Optional criteria can clarify what yes and no mean.
func Noul(instructions any) Question {
	return Question{Type: "noul", Instructions: instructions}
}

// NoulWithCriteria pins down a subtle yes/no boundary.
func NoulWithCriteria(instructions, yes, no any) Question {
	return Question{
		Type:         "noul",
		Instructions: instructions,
		Criteria:     map[string]any{"true": yes, "false": no},
	}
}

// Choice picks exactly one option from criteria, a map of option -> rubric
// description. Use a nil description when the option name is self-explanatory.
func Choice(instructions any, criteria map[string]any) Question {
	return Question{Type: "choice", Instructions: instructions, Criteria: criteria}
}

// Score rates the state along ordered levels, low to high. Levels may be
// strings or structured objects.
func Score(instructions any, criteria []any) Question {
	return Question{Type: "score", Instructions: instructions, Criteria: criteria}
}

// Validate checks the local shape limits the API documents, so an obvious
// mistake fails before a network round-trip.
func (q Question) Validate() error {
	switch q.Type {
	case "noul":
		if q.Instructions == nil {
			return fmt.Errorf("noul question requires instructions")
		}
	case "choice":
		if q.Instructions == nil {
			return fmt.Errorf("choice question requires instructions")
		}
		m, ok := q.Criteria.(map[string]any)
		if !ok || len(m) == 0 {
			return fmt.Errorf("choice question requires a non-empty criteria map")
		}
		if len(m) > MaxChoiceOptions {
			return fmt.Errorf("choice question has %d options; max is %d", len(m), MaxChoiceOptions)
		}
	case "score":
		if q.Instructions == nil {
			return fmt.Errorf("score question requires instructions")
		}
		lv, ok := q.Criteria.([]any)
		if !ok || len(lv) < MinScoreLevels || len(lv) > MaxScoreLevels {
			return fmt.Errorf("score question requires %d..%d levels", MinScoreLevels, MaxScoreLevels)
		}
	default:
		return fmt.Errorf("unknown question type %q", q.Type)
	}
	return nil
}

// Request is the exact POST /v1/systemone body. The generic Questions method
// takes this directly; most callers use SystemOne instead.
type Request struct {
	State     any                 `json:"state"`
	Model     string              `json:"model"`
	Questions map[string]Question `json:"questions"`
}

// ---------------------------------------------------------------------------
// Answers
// ---------------------------------------------------------------------------

// Answer is a typed answer. Exactly one of Noul, Score, or Choice is
// meaningful, selected by Type. Choice and Score answers carry Confidence and
// the full Probabilities distribution; Noul answers carry only Noul.
type Answer struct {
	Type          string             `json:"type"`
	Choice        string             `json:"choice,omitempty"`
	Noul          *float64           `json:"noul,omitempty"`
	Score         *float64           `json:"score,omitempty"`
	Legend        map[string]any     `json:"legend,omitempty"`
	Probabilities map[string]float64 `json:"probabilities,omitempty"`
	Confidence    *float64           `json:"confidence,omitempty"`
}

// NoulValue returns the yes-probability, or ok=false for the wrong type.
func (a Answer) NoulValue() (float64, bool) {
	if a.Noul == nil {
		return 0, false
	}
	return *a.Noul, true
}

// ScoreValue returns the probability-weighted score, or ok=false.
func (a Answer) ScoreValue() (float64, bool) {
	if a.Score == nil {
		return 0, false
	}
	return *a.Score, true
}

// ConfidenceValue returns the confidence, or ok=false for Noul answers.
func (a Answer) ConfidenceValue() (float64, bool) {
	if a.Confidence == nil {
		return 0, false
	}
	return *a.Confidence, true
}

// TopChoice returns the highest-probability option from Probabilities. It is
// useful when you want to reason about the runner-up as well.
func (a Answer) TopChoice() (option string, probability float64) {
	for k, v := range a.Probabilities {
		if v > probability || option == "" {
			option, probability = k, v
		}
	}
	return option, probability
}

// Usage reports the tokens TypeSafe billed. Input tokens are charged; output
// tokens are free.
type Usage struct {
	InputTokens  int64 `json:"input_tokens"`
	OutputTokens int64 `json:"output_tokens"`
}

// EstimatedCostUSD is advisory telemetry based on list price, not a bill.
func (u Usage) EstimatedCostUSD() float64 {
	return float64(u.InputTokens) / 1e9 * PricePerBillionInputTokensUSD
}

// Response is the POST /v1/systemone body.
type Response struct {
	Model             string            `json:"model"`
	Answers           map[string]Answer `json:"answers"`
	Usage             Usage             `json:"usage"`
	inputUsagePresent *bool             // nil only for explicitly constructed in-process responses
}

// UnmarshalJSON keeps presence separate from value: missing/null metering
// cannot be interpreted as an explicitly reported zero-cost request.
func (r *Response) UnmarshalJSON(data []byte) error {
	type wireResponse Response
	var wire wireResponse
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	var presence struct {
		Usage *struct {
			InputTokens *int64 `json:"input_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(data, &presence); err != nil {
		return err
	}
	known := presence.Usage != nil && presence.Usage.InputTokens != nil
	*r = Response(wire)
	r.inputUsagePresent = &known
	return nil
}

// InputUsageKnown reports whether decoded input usage was explicitly present.
// Directly constructed responses retain their intentional Usage values.
func (r *Response) InputUsageKnown() bool {
	return r != nil && r.Usage.InputTokens >= 0 && (r.inputUsagePresent == nil || *r.inputUsagePresent)
}

// ModelCard describes one model or alias from GET /v1/models.
type ModelCard struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	ReleaseDate string `json:"release_date"`
}

// ---------------------------------------------------------------------------
// Errors
// ---------------------------------------------------------------------------

// APIError is a non-2xx response from TypeSafe.
type APIError struct {
	StatusCode int
	Status     string
	Body       string
	RetryAfter time.Duration
}

func (e *APIError) Error() string {
	body := strings.TrimSpace(e.Body)
	if len(body) > 400 {
		body = body[:400] + "…"
	}
	if body == "" {
		return fmt.Sprintf("typesafe: HTTP %d %s", e.StatusCode, e.Status)
	}
	return fmt.Sprintf("typesafe: HTTP %d %s: %s", e.StatusCode, e.Status, body)
}

// Retryable reports whether the status is transient.
func (e *APIError) Retryable() bool {
	switch e.StatusCode {
	case http.StatusTooManyRequests, 529:
		return true
	}
	return e.StatusCode >= 500
}

// IsUnauthorized reports a missing or invalid key.
func IsUnauthorized(err error) bool {
	var ae *APIError
	return errors.As(err, &ae) && ae.StatusCode == http.StatusUnauthorized
}

// IsRateLimited reports a 429 or 529 overload response.
func IsRateLimited(err error) bool {
	var ae *APIError
	return errors.As(err, &ae) && (ae.StatusCode == http.StatusTooManyRequests || ae.StatusCode == 529)
}

// ---------------------------------------------------------------------------
// Calls
// ---------------------------------------------------------------------------

// SystemOne evaluates questions against state using the default model
// (TYPESAFE_MODEL, else DefaultModel).
func (c *Client) SystemOne(ctx context.Context, state any, questions map[string]Question) (*Response, error) {
	model := strings.TrimSpace(os.Getenv(EnvModel))
	if model == "" {
		model = DefaultModel
	}
	return c.SystemOneModel(ctx, model, state, questions)
}

// SystemOneModel evaluates questions against state with an explicit model.
func (c *Client) SystemOneModel(ctx context.Context, model string, state any, questions map[string]Question) (*Response, error) {
	if err := c.validate(questions); err != nil {
		return nil, err
	}
	if strings.TrimSpace(model) == "" {
		model = DefaultModel
	}
	req := Request{State: state, Model: model, Questions: questions}
	var out Response
	if err := c.doJSON(ctx, http.MethodPost, "/v1/systemone", req, &out); err != nil {
		return nil, err
	}
	if out.Answers == nil {
		out.Answers = map[string]Answer{}
	}
	return &out, nil
}

// ListModels returns the model IDs and aliases the account may request.
func (c *Client) ListModels(ctx context.Context) ([]ModelCard, error) {
	var out struct {
		Models []ModelCard `json:"models"`
	}
	if err := c.doJSON(ctx, http.MethodGet, "/v1/models", nil, &out); err != nil {
		return nil, err
	}
	return out.Models, nil
}

func (c *Client) validate(questions map[string]Question) error {
	if len(questions) == 0 {
		return errors.New("typesafe: at least one question is required")
	}
	ids := make([]string, 0, len(questions))
	for id, q := range questions {
		if strings.TrimSpace(id) == "" {
			return errors.New("typesafe: question ids must be non-empty")
		}
		if err := q.Validate(); err != nil {
			return fmt.Errorf("typesafe: question %q: %w", id, err)
		}
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return nil
}

func (c *Client) baseURL() string {
	if strings.TrimSpace(c.BaseURL) == "" {
		return DefaultBaseURL
	}
	return strings.TrimSuffix(c.BaseURL, "/")
}

func (c *Client) httpClient() *http.Client {
	if c.HTTPClient != nil {
		return c.HTTPClient
	}
	return &http.Client{Timeout: DefaultModelHTTPTimeout}
}

func (c *Client) doJSON(ctx context.Context, method, path string, payload, out any) error {
	var body []byte
	var err error
	if payload != nil {
		if body, err = json.Marshal(payload); err != nil {
			return fmt.Errorf("typesafe: encode request: %w", err)
		}
	}

	policy := c.Retry.normalized()
	var lastErr error
	for attempt := 0; attempt < policy.MaxAttempts; attempt++ {
		if attempt > 0 {
			delay := backoff(policy, attempt, lastErr)
			if err := sleepCtx(ctx, delay); err != nil {
				return err
			}
		}
		respBody, status, header, err := c.roundTrip(ctx, method, path, body)
		if err != nil {
			// Transport fault: retry unless the context is done or this caller
			// selected a status-only retry policy.
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if policy.DisableTransportRetries {
				return err
			}
			lastErr = err
			continue
		}
		if status >= 200 && status < 300 {
			if out == nil || len(respBody) == 0 {
				return nil
			}
			if err := json.Unmarshal(respBody, out); err != nil {
				return fmt.Errorf("typesafe: decode response: %w", err)
			}
			return nil
		}
		apiErr := &APIError{
			StatusCode: status,
			Status:     http.StatusText(status),
			Body:       string(respBody),
			RetryAfter: parseRetryAfter(header.Get("Retry-After")),
		}
		if !policy.retryableStatus(status) {
			return apiErr
		}
		lastErr = apiErr
	}
	if lastErr == nil {
		lastErr = errors.New("typesafe: request failed")
	}
	return lastErr
}

func (c *Client) roundTrip(ctx context.Context, method, path string, body []byte) ([]byte, int, http.Header, error) {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL()+path, reader)
	if err != nil {
		return nil, 0, nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")
	if c.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.APIKey)
	}
	if c.UserAgent != "" {
		req.Header.Set("User-Agent", c.UserAgent)
	}
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return nil, 0, nil, err
	}
	defer resp.Body.Close()
	// Cap the read; System One responses are small, and a pathological body
	// must not be able to exhaust memory.
	const maxBody = 8 << 20
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if err != nil {
		return nil, 0, nil, err
	}
	if len(data) > maxBody {
		return nil, 0, nil, fmt.Errorf("typesafe: response body exceeds %d bytes", maxBody)
	}
	return data, resp.StatusCode, resp.Header, nil
}

func backoff(p RetryPolicy, attempt int, last error) time.Duration {
	if ae, ok := last.(*APIError); ok && ae.RetryAfter > 0 {
		return ae.RetryAfter
	}
	d := p.BaseDelay
	for i := 1; i < attempt; i++ {
		d *= 2
		if d >= p.MaxDelay {
			return p.MaxDelay
		}
	}
	if d > p.MaxDelay {
		return p.MaxDelay
	}
	return d
}

func parseRetryAfter(v string) time.Duration {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}
	if secs, err := strconv.Atoi(v); err == nil {
		if secs < 0 {
			return 0
		}
		return time.Duration(secs) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil {
		if d := time.Until(t); d > 0 {
			return d
		}
	}
	return 0
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
