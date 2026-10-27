// Package deepseekusage prices DeepSeek Harness turns and parses the official
// DeepSeek platform usage export, so SoluTerminal can attribute spend per API
// key (and therefore per user).
//
// Why this exists:
//   - DeepSeek exposes exactly one official billing endpoint, account-wide
//     GET /user/balance. There is no per-key usage/spend API.
//   - The platform's "Export monthly data" does carry per-key rows
//     (amount-*.csv has api_key_name plus cache-hit / cache-miss / output
//     token types); cost-*.csv is model-level only.
//   - DSH records the same billed buckets durably for every turn, which lets
//     SoluTerminal self-meter between official exports.
//
// Everything here is pure (no DB, no HTTP, no clock reads except the explicit
// time.Time argument) so the pricing and parsing math is unit-testable.
package deepseekusage

import (
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
)

const (
	Currency            = "CNY"
	RateUnit            = "per 1M tokens"
	PricingVerifiedDate = "2026-09-29"
	PricingEnglishURL   = "https://api-docs.deepseek.com/quick_start/pricing/"
	PricingChineseURL   = "https://api-docs.deepseek.com/zh-cn/quick_start/pricing/"
)

// Tokens is one turn's billed token buckets. The names match DeepSeek's API
// usage fields and DSH's durable tokenUsage projection:
//
//	prompt_cache_hit_tokens  -> CacheHitInputTokens  (DSH cacheReadTokens)
//	prompt_cache_miss_tokens -> CacheMissInputTokens (DSH uncachedInputTokens)
//	completion_tokens        -> OutputTokens         (DSH outputTokens)
//
// DeepSeek bills no separate cache-write line; DSH's cacheWriteTokens (when
// present) is billed at the cache-hit rate.
type Tokens struct {
	CacheHitInputTokens  int64
	CacheMissInputTokens int64
	OutputTokens         int64
}

// Rates are PEAK-hour prices in CNY per 1,000,000 tokens. CNY is the currency
// the official platform export is denominated in, so the self-metered estimate
// is directly comparable to the imported `price` column. Off-peak is exactly
// half of peak. Source: api-docs.deepseek.com/zh-cn/quick_start/pricing.
type Rates struct {
	CacheHitInput  float64
	CacheMissInput float64
	Output         float64
}

// priceTable maps a canonical model name to its peak rates. Unknown models
// price as not-found so callers can show "unpriced" rather than invent a cost.
var priceTable = map[string]Rates{
	"deepseek-flash":  {CacheHitInput: 0.04, CacheMissInput: 2, Output: 8},
	"deepseek-v4-pro": {CacheHitInput: 0.30, CacheMissInput: 9, Output: 27},
}

// modelAliases folds legacy/vision names onto their billed model. Source: the
// official change log — deepseek-v4-flash and -vision-exp route to V4.1-Flash.
var modelAliases = map[string]string{
	"deepseek-v4-flash":            "deepseek-flash",
	"deepseek-v4-flash-vision-exp": "deepseek-flash",
}

// CanonicalModel returns the billed model name for a possibly-legacy alias.
func CanonicalModel(model string) string {
	m := strings.ToLower(strings.TrimSpace(model))
	if canonical, ok := modelAliases[m]; ok {
		return canonical
	}
	return m
}

// Catalog returns a copy so daemon/UI callers cannot mutate canonical prices.
func Catalog() map[string]Rates {
	out := make(map[string]Rates, len(priceTable))
	for model, rates := range priceTable {
		out[model] = rates
	}
	return out
}

func PeakRates(model string) (Rates, bool) {
	rates, ok := priceTable[CanonicalModel(model)]
	return rates, ok
}

func HalfRates(rates Rates) Rates {
	return Rates{CacheHitInput: rates.CacheHitInput / 2, CacheMissInput: rates.CacheMissInput / 2, Output: rates.Output / 2}
}

// CurrentRates returns no rate when either the model is unknown or the holiday
// calendar cannot verify the billing state at the requested time.
func CurrentRates(model string, at time.Time) (Rates, bool) {
	rates, ok := PeakRates(model)
	if !ok {
		return Rates{}, false
	}
	schedule := ScheduleAt(at)
	if !schedule.HolidayVerified {
		return Rates{}, false
	}
	if schedule.State == ScheduleOffPeak {
		return HalfRates(rates), true
	}
	return rates, true
}

// IsPeak reports whether t falls in DeepSeek's peak window: 01:00–04:00 and
// 06:00–10:00 UTC, Monday through Friday. Everything else (including all
// weekend hours) is off-peak and billed at half price.
func IsPeak(t time.Time) bool {
	return ScheduleAt(t).State == SchedulePeak
}

// Cost prices one turn at `at`, in the price table's currency (CNY). ok is
// false when the model is not in the price table (callers should surface that
// instead of a made-up number).
func Cost(model string, tk Tokens, at time.Time) (cost float64, ok bool) {
	rates, found := CurrentRates(model, at)
	if !found {
		return 0, false
	}
	const perMillion = 1_000_000
	cost = float64(tk.CacheHitInputTokens) * rates.CacheHitInput / perMillion
	cost += float64(tk.CacheMissInputTokens) * rates.CacheMissInput / perMillion
	cost += float64(tk.OutputTokens) * rates.Output / perMillion
	return cost, true
}

// AmountRow is one row of the official amount-*.csv export. Prices and amounts
// are left exactly as the file carries them. The export's own `price` column is
// used only through rowCost (per-1M-token convention); the raw values stay for
// audit and reconciliation against cost-*.csv.
type AmountRow struct {
	Start        string
	End          string
	Model        string
	APIKeyName   string
	APIKeyMasked string
	Type         string
	Price        float64 // 0 when the column is empty (e.g. request_count)
	Amount       int64
}

// KeyUsage is the per-(API key) or per-(API key, model) rollup of an amount
// export.
type KeyUsage struct {
	Requests             int64 `json:"requests"`
	CacheHitInputTokens  int64 `json:"cacheHitInputTokens"`
	CacheMissInputTokens int64 `json:"cacheMissInputTokens"`
	OutputTokens         int64 `json:"outputTokens"`
	// Cost is the export's own priced total. The amount-*.csv `price` column is
	// a per-1,000,000-token rate (the same unit as the official pricing page),
	// so a token row contributes amount/1e6*price. Rows with no price
	// (request_count) contribute nothing. Treat this as the authoritative
	// billed figure; Cost() is the self-metered estimate to reconcile to.
	Cost float64 `json:"cost"`
	// UnknownAmounts counts rows whose type was not one of the four documented
	// values, so a schema drift is visible rather than silently dropped.
	UnknownAmounts int64 `json:"unknownAmounts"`
	// UnpricedTokens is used only by the live DSH meter. Official CSV rows keep
	// their provider prices untouched and never populate this field.
	UnpricedTokens int64 `json:"-"`
}

// rowCost returns the priced contribution of one amount row. ok is false for
// non-token rows (request_count) and for rows the export left unpriced.
func rowCost(row AmountRow) (float64, bool) {
	switch strings.ToLower(row.Type) {
	case "input_cache_hit_tokens", "input_cache_miss_tokens", "output_tokens":
		return float64(row.Amount) / 1_000_000 * row.Price, true
	default:
		return 0, false
	}
}

// ErrMissingHeader means the CSV did not look like the documented export.
var ErrMissingHeader = errors.New("deepseek usage export: missing required columns")

// ParseAmountCSV parses the official amount-*.csv. It matches columns by
// header name (order- and case-insensitive) so a column shuffle does not break
// the import. api_key_name is required; the rest are best-effort.
func ParseAmountCSV(r io.Reader) ([]AmountRow, error) {
	records, err := csv.NewReader(r).ReadAll()
	if err != nil {
		return nil, fmt.Errorf("deepseek usage export: %w", err)
	}
	if len(records) == 0 {
		return nil, ErrMissingHeader
	}
	index := map[string]int{}
	for i, name := range records[0] {
		index[strings.ToLower(strings.TrimSpace(name))] = i
	}
	keyCol, ok := index["api_key_name"]
	if !ok {
		return nil, fmt.Errorf("%w: api_key_name", ErrMissingHeader)
	}
	col := func(record []string, name string) string {
		i, present := index[name]
		if !present || i >= len(record) {
			return ""
		}
		return strings.TrimSpace(record[i])
	}
	rows := make([]AmountRow, 0, len(records)-1)
	for _, record := range records[1:] {
		if len(record) <= keyCol {
			continue
		}
		amount, _ := strconv.ParseInt(col(record, "amount"), 10, 64)
		price, _ := strconv.ParseFloat(col(record, "price"), 64)
		rows = append(rows, AmountRow{
			Start:        col(record, "start_time_iso"),
			End:          col(record, "end_time_iso"),
			Model:        col(record, "model"),
			APIKeyName:   col(record, "api_key_name"),
			APIKeyMasked: col(record, "api_key"),
			Type:         col(record, "type"),
			Price:        price,
			Amount:       amount,
		})
	}
	return rows, nil
}

// AggregateAmounts rolls amount rows up per API key. Key names are returned
// verbatim (they are what the owner names keys after, and what the Team flow
// maps to a member), so callers can join on them directly.
func AggregateAmounts(rows []AmountRow) map[string]KeyUsage {
	out := map[string]KeyUsage{}
	for _, row := range rows {
		if row.APIKeyName == "" {
			continue
		}
		usage := out[row.APIKeyName]
		switch strings.ToLower(row.Type) {
		case "request_count":
			usage.Requests += row.Amount
		case "input_cache_hit_tokens":
			usage.CacheHitInputTokens += row.Amount
		case "input_cache_miss_tokens":
			usage.CacheMissInputTokens += row.Amount
		case "output_tokens":
			usage.OutputTokens += row.Amount
		default:
			usage.UnknownAmounts += row.Amount
		}
		if cost, ok := rowCost(row); ok {
			usage.Cost += cost
		}
		out[row.APIKeyName] = usage
	}
	return out
}

// AggregateByKeyModel rolls amount rows up per API key AND per model within
// that key, so the UI can show which model a user's spend came from.
func AggregateByKeyModel(rows []AmountRow) map[string]map[string]KeyUsage {
	out := map[string]map[string]KeyUsage{}
	for _, row := range rows {
		if row.APIKeyName == "" {
			continue
		}
		byModel := out[row.APIKeyName]
		if byModel == nil {
			byModel = map[string]KeyUsage{}
			out[row.APIKeyName] = byModel
		}
		usage := byModel[row.Model]
		switch strings.ToLower(row.Type) {
		case "request_count":
			usage.Requests += row.Amount
		case "input_cache_hit_tokens":
			usage.CacheHitInputTokens += row.Amount
		case "input_cache_miss_tokens":
			usage.CacheMissInputTokens += row.Amount
		case "output_tokens":
			usage.OutputTokens += row.Amount
		default:
			usage.UnknownAmounts += row.Amount
		}
		if cost, ok := rowCost(row); ok {
			usage.Cost += cost
		}
		byModel[row.Model] = usage
	}
	return out
}

// CostRow is one row of the official cost-*.csv (model-level, no key column).
// It is the reconciliation source: per-key self-metering should sum back to
// these totals within rounding.
type CostRow struct {
	Start      string
	End        string
	Model      string
	WalletType string
	Cost       float64 // negative is a charge, per the export
	Currency   string
}

// ParseCostCSV parses the official cost-*.csv.
func ParseCostCSV(r io.Reader) ([]CostRow, error) {
	records, err := csv.NewReader(r).ReadAll()
	if err != nil {
		return nil, fmt.Errorf("deepseek cost export: %w", err)
	}
	if len(records) == 0 {
		return nil, ErrMissingHeader
	}
	index := map[string]int{}
	for i, name := range records[0] {
		index[strings.ToLower(strings.TrimSpace(name))] = i
	}
	if _, ok := index["cost"]; !ok {
		return nil, fmt.Errorf("%w: cost", ErrMissingHeader)
	}
	col := func(record []string, name string) string {
		i, present := index[name]
		if !present || i >= len(record) {
			return ""
		}
		return strings.TrimSpace(record[i])
	}
	rows := make([]CostRow, 0, len(records)-1)
	for _, record := range records[1:] {
		cost, _ := strconv.ParseFloat(col(record, "cost"), 64)
		rows = append(rows, CostRow{
			Start:      col(record, "start_time_iso"),
			End:        col(record, "end_time_iso"),
			Model:      col(record, "model"),
			WalletType: col(record, "wallet_type"),
			Cost:       cost,
			Currency:   col(record, "currency"),
		})
	}
	return rows, nil
}
