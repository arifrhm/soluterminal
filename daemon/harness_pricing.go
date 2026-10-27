package main

import (
	"net/http"
	"sort"
	"strings"
	"time"

	"soluterminal-shared/deepseekusage"
)

type harnessPricingRates struct {
	CacheHitInput  float64 `json:"cachedInput"`
	CacheMissInput float64 `json:"uncachedInput"`
	Output         float64 `json:"output"`
}

type harnessPricingCatalogRow struct {
	Model   string              `json:"model"`
	Peak    harnessPricingRates `json:"peak"`
	OffPeak harnessPricingRates `json:"offPeak"`
}

type harnessPricingTransition struct {
	State string `json:"state"`
	At    string `json:"at"`
}

type harnessPricingSource struct {
	Label      string `json:"label"`
	URL        string `json:"url"`
	IssuedDate string `json:"issuedDate,omitempty"`
}

type harnessPricingReport struct {
	GeneratedAt       string                     `json:"generatedAt"`
	Currency          string                     `json:"currency"`
	Unit              string                     `json:"unit"`
	RequestedModel    string                     `json:"requestedModel,omitempty"`
	CanonicalModel    string                     `json:"canonicalModel,omitempty"`
	Priced            bool                       `json:"priced"`
	State             string                     `json:"state"`
	HolidayVerified   bool                       `json:"holidayVerified"`
	Holiday           bool                       `json:"holiday"`
	ScheduleWouldPeak bool                       `json:"scheduleWouldPeak"`
	SavingsPercent    *int                       `json:"savingsPercent,omitempty"`
	CurrentRates      *harnessPricingRates       `json:"currentRates,omitempty"`
	PeakRates         *harnessPricingRates       `json:"peakRates,omitempty"`
	OffPeakRates      *harnessPricingRates       `json:"offPeakRates,omitempty"`
	NextChange        *harnessPricingTransition  `json:"nextChange,omitempty"`
	Catalog           []harnessPricingCatalogRow `json:"catalog"`
	Policy            struct {
		Timezone string               `json:"timezone"`
		Windows  []string             `json:"windows"`
		HalfOpen bool                 `json:"halfOpen"`
		Source   harnessPricingSource `json:"source"`
	} `json:"policy"`
	Pricing struct {
		VerifiedDate string                 `json:"verifiedDate"`
		Sources      []harnessPricingSource `json:"sources"`
	} `json:"pricing"`
	Calendar struct {
		Version      string                 `json:"version"`
		UpdatedDate  string                 `json:"updatedDate"`
		CoverageFrom int                    `json:"coverageFrom"`
		CoverageTo   int                    `json:"coverageTo"`
		Sources      []harnessPricingSource `json:"sources"`
	} `json:"calendar"`
}

func pricingRates(r deepseekusage.Rates) harnessPricingRates {
	return harnessPricingRates{CacheHitInput: r.CacheHitInput, CacheMissInput: r.CacheMissInput, Output: r.Output}
}

func pricingSource(s deepseekusage.Source) harnessPricingSource {
	return harnessPricingSource{Label: s.Label, URL: s.URL, IssuedDate: s.IssuedDate}
}

// buildHarnessPricingReport is pure: handlers provide the clock and it only
// reads immutable shared pricing/calendar data. It makes no credential,
// provider, model, balance, CSV or network call.
func buildHarnessPricingReport(at time.Time, requestedModel string) harnessPricingReport {
	requestedModel = strings.TrimSpace(requestedModel)
	report := harnessPricingReport{
		GeneratedAt:    at.UTC().Format(time.RFC3339Nano),
		Currency:       deepseekusage.Currency,
		Unit:           deepseekusage.RateUnit,
		RequestedModel: requestedModel,
		Catalog:        []harnessPricingCatalogRow{},
	}

	schedule := deepseekusage.ScheduleAt(at)
	report.State = string(schedule.State)
	report.HolidayVerified = schedule.HolidayVerified
	report.Holiday = schedule.Holiday
	report.ScheduleWouldPeak = schedule.ScheduleWouldPeak
	if schedule.State == deepseekusage.ScheduleOffPeak {
		savings := 50
		report.SavingsPercent = &savings
	}
	if next, ok := deepseekusage.NextTransition(at); ok {
		report.NextChange = &harnessPricingTransition{State: string(next.State), At: next.At.UTC().Format(time.RFC3339Nano)}
	}

	report.Policy.Timezone = deepseekusage.PolicyTimezone
	report.Policy.Windows = []string{"Monday–Friday 09:00–12:00", "Monday–Friday 14:00–18:00"}
	report.Policy.HalfOpen = true
	report.Policy.Source = harnessPricingSource{Label: "DeepSeek Models & Pricing (schedule and public-holiday policy)", URL: deepseekusage.ScheduleSourceURL}
	report.Pricing.VerifiedDate = deepseekusage.PricingVerifiedDate
	report.Pricing.Sources = []harnessPricingSource{
		{Label: "DeepSeek Models & Pricing (English)", URL: deepseekusage.PricingEnglishURL},
		{Label: "DeepSeek Models & Pricing (Chinese/CNY)", URL: deepseekusage.PricingChineseURL},
	}
	report.Calendar.Version = deepseekusage.CalendarVersion
	report.Calendar.UpdatedDate = deepseekusage.CalendarUpdatedDate
	report.Calendar.CoverageFrom = deepseekusage.CalendarCoverageFrom
	report.Calendar.CoverageTo = deepseekusage.CalendarCoverageTo
	for _, source := range deepseekusage.CalendarSources() {
		report.Calendar.Sources = append(report.Calendar.Sources, pricingSource(source))
	}

	catalog := deepseekusage.Catalog()
	models := make([]string, 0, len(catalog))
	for model := range catalog {
		models = append(models, model)
	}
	sort.Strings(models)
	for _, model := range models {
		peak := catalog[model]
		report.Catalog = append(report.Catalog, harnessPricingCatalogRow{Model: model, Peak: pricingRates(peak), OffPeak: pricingRates(deepseekusage.HalfRates(peak))})
	}

	if requestedModel != "" {
		report.CanonicalModel = deepseekusage.CanonicalModel(requestedModel)
		if peak, ok := deepseekusage.PeakRates(requestedModel); ok {
			report.Priced = true
			peakDTO, offDTO := pricingRates(peak), pricingRates(deepseekusage.HalfRates(peak))
			report.PeakRates, report.OffPeakRates = &peakDTO, &offDTO
			if current, ok := deepseekusage.CurrentRates(requestedModel, at); ok {
				currentDTO := pricingRates(current)
				report.CurrentRates = &currentDTO
			}
		}
	}
	return report
}

func (a *apiServer) getHarnessPricing(w http.ResponseWriter, r *http.Request) {
	provider := r.PathValue("provider")
	if !isHarnessProvider(provider) {
		http.NotFound(w, r)
		return
	}
	if !harnessUsageProvider(w, provider) {
		return
	}
	writeJSON(w, http.StatusOK, buildHarnessPricingReport(time.Now(), r.URL.Query().Get("model")))
}
