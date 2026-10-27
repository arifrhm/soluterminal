package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"soluterminal-shared/deepseekusage"
)

// Per-provider DeepSeek usage/spend ledger.
//
// The official DeepSeek platform exposes no per-key usage API. Its monthly
// "Export monthly data" is the only official artifact that names the API key
// (amount-*.csv carries api_key_name and the cache-hit/cache-miss/output token
// types), and the export is account-wide. So the owner imports it once on this
// machine and SoluTerminal attributes spend per key — and therefore per user,
// when keys are named after users (which the Team assignment flow already does).
//
// The ledger keeps one entry per imported file (keyed by its filename), so
// importing January and February sums them while re-importing January replaces
// it. Nothing here talks to the cloud; it is deliberately local-first.

// harnessUsageImport is one imported export file.
type harnessUsageImport struct {
	Source     string                                       `json:"source"`
	ImportedAt int64                                        `json:"importedAt"`
	RowCount   int                                          `json:"rowCount"`
	Currency   string                                       `json:"currency,omitempty"`
	CostTotal  float64                                      `json:"costTotal,omitempty"`
	Keys       map[string]deepseekusage.KeyUsage            `json:"keys"`
	Models     map[string]map[string]deepseekusage.KeyUsage `json:"models,omitempty"`
}

type harnessUsageState struct {
	Version int                           `json:"version"`
	Imports map[string]harnessUsageImport `json:"imports"`
	// Budgets are soft, per-API-key monthly figures. They are a SoluTerminal
	// guardrail only: DeepSeek has no per-key budget to set or read.
	Budgets map[string]float64 `json:"budgets,omitempty"`
}

type harnessUsageStore struct {
	mu   sync.Mutex
	path string
}

func newHarnessUsageStore(dataDir, provider string) *harnessUsageStore {
	return &harnessUsageStore{path: filepath.Join(dataDir, "harness-accounts", provider+"-usage.json")}
}

func (s *harnessUsageStore) readLocked() (harnessUsageState, error) {
	st := harnessUsageState{Version: 1, Imports: map[string]harnessUsageImport{}, Budgets: map[string]float64{}}
	if info, err := os.Stat(s.path); err == nil && info.Size() > 4<<20 {
		return st, errors.New("harness usage store is too large; it was not overwritten")
	}
	b, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return st, nil
	}
	if err != nil {
		return st, errors.New("cannot read harness usage store")
	}
	if json.Unmarshal(b, &st) != nil || st.Version != 1 {
		return st, errors.New("harness usage store is malformed; it was not overwritten")
	}
	if st.Imports == nil {
		st.Imports = map[string]harnessUsageImport{}
	}
	if st.Budgets == nil {
		st.Budgets = map[string]float64{}
	}
	return st, nil
}

func (s *harnessUsageStore) writeLocked(st harnessUsageState) error {
	b, err := json.Marshal(st)
	if err != nil {
		return errors.New("cannot encode harness usage store")
	}
	if err = os.MkdirAll(filepath.Dir(s.path), 0700); err != nil {
		return err
	}
	return writeFileAtomicSynced(s.path, b, 0600)
}

// --- Report DTOs (browser-facing) -----------------------------------------

type harnessModelUsageRow struct {
	Model string `json:"model"`
	deepseekusage.KeyUsage
}

type harnessKeyUsageRow struct {
	APIKeyName string `json:"apiKeyName"`
	deepseekusage.KeyUsage
	Models     []harnessModelUsageRow `json:"models,omitempty"`
	Budget     float64                `json:"budget,omitempty"`
	OverBudget bool                   `json:"overBudget,omitempty"`
	// MeterCost/MeterTokens are the self-metered DSH estimate for this key,
	// shown beside the authoritative imported figure (or alone when the key has
	// no import yet).
	MeterCost   float64 `json:"meterCost,omitempty"`
	MeterTokens int64   `json:"meterTokens,omitempty"`
	// Meter estimates always use the canonical CNY table, independently of an
	// imported CSV's own currency. Unpriced tokens have an unknown model or an
	// unverified holiday calendar date and are never represented as zero cost.
	MeterCurrency       string `json:"meterCurrency,omitempty"`
	MeterUnpricedTokens int64  `json:"meterUnpricedTokens,omitempty"`
	// Estimated marks a row whose only cost is the self-metered estimate.
	Estimated bool `json:"estimated,omitempty"`
}

type harnessUsageReport struct {
	ImportedAt int64                `json:"importedAt"`
	Sources    []string             `json:"sources"`
	RowCount   int                  `json:"rowCount"`
	Currency   string               `json:"currency"`
	CostTotal  float64              `json:"costTotal"`
	Keys       []harnessKeyUsageRow `json:"keys"`
}

// report folds every imported file into one per-key view.
func (st harnessUsageState) report() harnessUsageReport {
	report := harnessUsageReport{Currency: deepseekusage.Currency, Keys: []harnessKeyUsageRow{}}
	keys := map[string]deepseekusage.KeyUsage{}
	models := map[string]map[string]deepseekusage.KeyUsage{}
	for _, imp := range st.Imports {
		if imp.ImportedAt > report.ImportedAt {
			report.ImportedAt = imp.ImportedAt
		}
		if imp.Currency != "" {
			report.Currency = imp.Currency
		}
		report.RowCount += imp.RowCount
		report.CostTotal += imp.CostTotal
		report.Sources = append(report.Sources, imp.Source)
		for name, usage := range imp.Keys {
			foldKeyUsage(keys, name, usage)
		}
		for name, byModel := range imp.Models {
			if models[name] == nil {
				models[name] = map[string]deepseekusage.KeyUsage{}
			}
			for model, usage := range byModel {
				foldKeyUsage(models[name], model, usage)
			}
		}
	}
	sort.Strings(report.Sources)
	for name, usage := range keys {
		row := harnessKeyUsageRow{APIKeyName: name, KeyUsage: usage}
		row.Budget = st.Budgets[name]
		row.OverBudget = row.Budget > 0 && usage.Cost > row.Budget
		byModel := models[name]
		modelNames := make([]string, 0, len(byModel))
		for model := range byModel {
			modelNames = append(modelNames, model)
		}
		sort.Slice(modelNames, func(i, j int) bool {
			if byModel[modelNames[i]].Cost != byModel[modelNames[j]].Cost {
				return byModel[modelNames[i]].Cost > byModel[modelNames[j]].Cost
			}
			return modelNames[i] < modelNames[j]
		})
		for _, model := range modelNames {
			row.Models = append(row.Models, harnessModelUsageRow{Model: model, KeyUsage: byModel[model]})
		}
		report.Keys = append(report.Keys, row)
	}
	sort.Slice(report.Keys, func(i, j int) bool {
		if report.Keys[i].Cost != report.Keys[j].Cost {
			return report.Keys[i].Cost > report.Keys[j].Cost
		}
		return report.Keys[i].APIKeyName < report.Keys[j].APIKeyName
	})
	return report
}

func foldKeyUsage(dst map[string]deepseekusage.KeyUsage, name string, add deepseekusage.KeyUsage) {
	cur := dst[name]
	cur.Requests += add.Requests
	cur.CacheHitInputTokens += add.CacheHitInputTokens
	cur.CacheMissInputTokens += add.CacheMissInputTokens
	cur.OutputTokens += add.OutputTokens
	cur.Cost += add.Cost
	cur.UnknownAmounts += add.UnknownAmounts
	cur.UnpricedTokens += add.UnpricedTokens
	dst[name] = cur
}

// mergeMeter folds the self-metered DSH estimate into an imported report. A key
// present in both keeps its authoritative Cost and gains MeterCost; a key only
// seen by the meter is added as an estimated row (Cost 0). The estimate never
// overwrites the official figure.
func mergeMeter(report *harnessUsageReport, meter map[string]deepseekusage.KeyUsage) {
	index := make(map[string]int, len(report.Keys))
	for i, row := range report.Keys {
		index[row.APIKeyName] = i
	}
	for name, usage := range meter {
		tokens := usage.CacheHitInputTokens + usage.CacheMissInputTokens + usage.OutputTokens
		if i, ok := index[name]; ok {
			report.Keys[i].MeterCost = usage.Cost
			report.Keys[i].MeterTokens = tokens
			report.Keys[i].MeterCurrency = deepseekusage.Currency
			report.Keys[i].MeterUnpricedTokens = usage.UnpricedTokens
			continue
		}
		report.Keys = append(report.Keys, harnessKeyUsageRow{
			APIKeyName: name,
			KeyUsage: deepseekusage.KeyUsage{
				CacheHitInputTokens:  usage.CacheHitInputTokens,
				CacheMissInputTokens: usage.CacheMissInputTokens,
				OutputTokens:         usage.OutputTokens,
			},
			MeterCost: usage.Cost, MeterTokens: tokens, MeterCurrency: deepseekusage.Currency,
			MeterUnpricedTokens: usage.UnpricedTokens, Estimated: true,
		})
	}
	sort.Slice(report.Keys, func(i, j int) bool {
		left, right := report.Keys[i], report.Keys[j]
		leftTotal, rightTotal := left.Cost+left.MeterCost, right.Cost+right.MeterCost
		if leftTotal != rightTotal {
			return leftTotal > rightTotal
		}
		return left.APIKeyName < right.APIKeyName
	})
}

// reportWithMeter is the handler-facing report: imported CSV data plus the
// live self-metered estimate.
func (a *apiServer) reportWithMeter(st harnessUsageState) harnessUsageReport {
	report := st.report()
	mergeMeter(&report, cachedDeepSeekMeter(a.dataDir))
	return report
}

// --- HTTP handlers ---------------------------------------------------------

// harnessUsageProvider rejects providers that have no balance/usage surface.
func harnessUsageProvider(w http.ResponseWriter, provider string) bool {
	if provider != "deepseek" {
		http.Error(w, "usage and balance tracking is available for DeepSeek only", http.StatusBadRequest)
		return false
	}
	return true
}

func (a *apiServer) getHarnessUsage(w http.ResponseWriter, r *http.Request) {
	s, ok := a.harnessRequest(w, r)
	if !ok {
		return
	}
	if !harnessUsageProvider(w, s.provider) {
		return
	}
	st, err := newHarnessUsageStore(a.dataDir, s.provider).read()
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	writeJSON(w, 200, a.reportWithMeter(st))
}

// importHarnessUsage accepts an official platform export as a raw CSV body.
// ?kind=amount (default) parses amount-*.csv; ?kind=cost parses cost-*.csv for
// currency + a reconciliation total. ?source names the file (defaults to the
// kind) and keys the entry so re-imports replace and new months sum.
func (a *apiServer) importHarnessUsage(w http.ResponseWriter, r *http.Request) {
	s, ok := a.harnessRequest(w, r)
	if !ok {
		return
	}
	if !harnessUsageProvider(w, s.provider) {
		return
	}
	kind := r.URL.Query().Get("kind")
	if kind == "" {
		kind = "amount"
	}
	if kind != "amount" && kind != "cost" {
		http.Error(w, "kind must be amount or cost", 400)
		return
	}
	source := filepath.Base(strings.TrimSpace(r.URL.Query().Get("source")))
	if source == "" || source == "." || source == string(filepath.Separator) {
		source = kind
	}
	if len(source) > 120 {
		source = source[:120]
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 8<<20))
	if err != nil {
		http.Error(w, "could not read the export", 400)
		return
	}
	store := newHarnessUsageStore(a.dataDir, s.provider)
	if err := store.importCSV(source, kind, body); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	st, err := store.read()
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	writeJSON(w, 200, a.reportWithMeter(st))
}

func (s *harnessUsageStore) importCSV(source, kind string, body []byte) error {
	entry := harnessUsageImport{Source: source, ImportedAt: time.Now().Unix()}
	switch kind {
	case "amount":
		rows, err := deepseekusage.ParseAmountCSV(bytes.NewReader(body))
		if err != nil {
			return err
		}
		entry.RowCount = len(rows)
		entry.Keys = deepseekusage.AggregateAmounts(rows)
		entry.Models = deepseekusage.AggregateByKeyModel(rows)
		for _, usage := range entry.Keys {
			entry.CostTotal += usage.Cost
		}
	case "cost":
		rows, err := deepseekusage.ParseCostCSV(bytes.NewReader(body))
		if err != nil {
			return err
		}
		entry.RowCount = len(rows)
		entry.Keys = map[string]deepseekusage.KeyUsage{}
		for _, row := range rows {
			// The export stores a charge as a negative number.
			entry.CostTotal += -row.Cost
			if entry.Currency == "" && row.Currency != "" {
				entry.Currency = row.Currency
			}
		}
	}
	_, err := s.mutate(func(st *harnessUsageState) error {
		st.Imports[source] = entry
		return nil
	})
	return err
}

func (a *apiServer) putHarnessUsageBudget(w http.ResponseWriter, r *http.Request) {
	s, ok := a.harnessRequest(w, r)
	if !ok {
		return
	}
	if !harnessUsageProvider(w, s.provider) {
		return
	}
	var req struct {
		Key     string  `json:"key"`
		Monthly float64 `json:"monthly"`
	}
	if !harnessDecode(w, r, &req) {
		return
	}
	key := strings.TrimSpace(req.Key)
	if key == "" || req.Monthly < 0 || req.Monthly > 1_000_000 {
		http.Error(w, "invalid budget", 400)
		return
	}
	st, err := newHarnessUsageStore(a.dataDir, s.provider).mutate(func(st *harnessUsageState) error {
		if req.Monthly == 0 {
			delete(st.Budgets, key)
		} else {
			st.Budgets[key] = req.Monthly
		}
		return nil
	})
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	writeJSON(w, 200, a.reportWithMeter(st))
}

// --- Balance ---------------------------------------------------------------

// harnessBalanceDTO is the sanitized GET /user/balance view. Provider body
// fields are copied by name; the raw response is never returned.
type harnessBalanceDTO struct {
	Currency string `json:"currency"`
	Total    string `json:"total"`
	Granted  string `json:"granted"`
	ToppedUp string `json:"toppedUp"`
}

type harnessBalanceReport struct {
	Status    string              `json:"status"` // ok | no-account | no-selection | auth-failed | unreachable | timeout | error
	Available bool                `json:"available"`
	Balances  []harnessBalanceDTO `json:"balances"`
}

type deepSeekBalanceResponse struct {
	IsAvailable  bool `json:"is_available"`
	BalanceInfos []struct {
		Currency        string `json:"currency"`
		TotalBalance    string `json:"total_balance"`
		GrantedBalance  string `json:"granted_balance"`
		ToppedUpBalance string `json:"topped_up_balance"`
	} `json:"balance_infos"`
}

// fetchDeepSeekBalance reads the official account balance. Like the Check/Test
// probes it never returns the raw body; unlike them it is free and safe to poll.
func fetchDeepSeekBalance(ctx context.Context, base string, acc harnessAccount, client *http.Client) harnessBalanceReport {
	report := harnessBalanceReport{Status: "ok", Balances: []harnessBalanceDTO{}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(base, "/")+"/user/balance", nil)
	if err != nil {
		report.Status = "error"
		return report
	}
	req.Header.Set("Authorization", "Bearer "+acc.APIKey)
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || os.IsTimeout(err) {
			report.Status = "timeout"
		} else {
			report.Status = "unreachable"
		}
		return report
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == 401 || resp.StatusCode == 403:
		report.Status = "auth-failed"
		return report
	case resp.StatusCode < 200 || resp.StatusCode >= 300:
		report.Status = "error"
		return report
	}
	var parsed deepSeekBalanceResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&parsed); err != nil {
		report.Status = "error"
		return report
	}
	report.Available = parsed.IsAvailable
	for _, info := range parsed.BalanceInfos {
		report.Balances = append(report.Balances, harnessBalanceDTO{
			Currency: info.Currency, Total: info.TotalBalance,
			Granted: info.GrantedBalance, ToppedUp: info.ToppedUpBalance,
		})
	}
	return report
}

func (a *apiServer) getHarnessBalance(w http.ResponseWriter, r *http.Request) {
	s, ok := a.harnessRequest(w, r)
	if !ok {
		return
	}
	if !harnessUsageProvider(w, s.provider) {
		return
	}
	_, _, acc, err := s.selected()
	if err != nil {
		writeJSON(w, 200, harnessBalanceReport{Status: "no-selection", Balances: []harnessBalanceDTO{}})
		return
	}
	if acc.BrokerOnly {
		writeJSON(w, 200, harnessBalanceReport{Status: "unsupported", Balances: []harnessBalanceDTO{}})
		return
	}
	base, err := harnessEndpoint(s.provider, acc.Billing)
	if err != nil {
		writeJSON(w, 200, harnessBalanceReport{Status: "error", Balances: []harnessBalanceDTO{}})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	writeJSON(w, 200, fetchDeepSeekBalance(ctx, base, acc, nil))
}

// read/write helpers -------------------------------------------------------

func (s *harnessUsageStore) read() (harnessUsageState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.readLocked()
}

func (s *harnessUsageStore) mutate(fn func(*harnessUsageState) error) (harnessUsageState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, err := s.readLocked()
	if err != nil {
		return st, err
	}
	if err = fn(&st); err != nil {
		return st, err
	}
	return st, s.writeLocked(st)
}
