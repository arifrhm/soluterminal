package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

var pricingShanghai = time.FixedZone("CST", 8*60*60)

func pricingAt(year int, month time.Month, day, hour, minute int) time.Time {
	return time.Date(year, month, day, hour, minute, 0, 0, pricingShanghai)
}

func TestBuildHarnessPricingReportCanonicalCNYRates(t *testing.T) {
	at := pricingAt(2026, time.September, 14, 10, 0)
	report := buildHarnessPricingReport(at, "deepseek-v4-flash")
	if report.Currency != "CNY" || report.Unit != "per 1M tokens" || report.State != "peak" {
		t.Fatalf("report identity/schedule = %+v", report)
	}
	if !report.Priced || report.RequestedModel != "deepseek-v4-flash" || report.CanonicalModel != "deepseek-flash" {
		t.Fatalf("alias information = %+v", report)
	}
	if report.CurrentRates == nil || *report.CurrentRates != (harnessPricingRates{CacheHitInput: 0.04, CacheMissInput: 2, Output: 8}) {
		t.Fatalf("flash current rates = %+v", report.CurrentRates)
	}
	if len(report.Catalog) != 2 || report.Catalog[0].Model != "deepseek-flash" || report.Catalog[1].Model != "deepseek-v4-pro" {
		t.Fatalf("catalog order = %+v", report.Catalog)
	}
	if report.Catalog[1].Peak != (harnessPricingRates{CacheHitInput: 0.30, CacheMissInput: 9, Output: 27}) ||
		report.Catalog[1].OffPeak != (harnessPricingRates{CacheHitInput: 0.15, CacheMissInput: 4.5, Output: 13.5}) {
		t.Fatalf("pro catalog = %+v", report.Catalog[1])
	}
	if report.NextChange == nil || report.NextChange.State != "off-peak" || report.NextChange.At != "2026-09-14T04:00:00Z" {
		t.Fatalf("next transition = %+v", report.NextChange)
	}
	if !report.Policy.HalfOpen || report.Policy.Timezone != "Asia/Shanghai" || report.Pricing.VerifiedDate != "2026-09-29" || len(report.Pricing.Sources) != 2 || report.Calendar.CoverageFrom != 2025 || report.Calendar.CoverageTo != 2026 || len(report.Calendar.Sources) != 2 {
		t.Fatalf("source metadata missing: pricing=%+v calendar=%+v", report.Pricing, report.Calendar)
	}
}

func TestBuildHarnessPricingReportOffPeakHolidayAndClosureTransition(t *testing.T) {
	report := buildHarnessPricingReport(pricingAt(2026, time.October, 2, 10, 0), "deepseek-v4-pro")
	if report.State != "off-peak" || !report.HolidayVerified || !report.Holiday || report.SavingsPercent == nil || *report.SavingsPercent != 50 {
		t.Fatalf("holiday schedule = %+v", report)
	}
	if report.CurrentRates == nil || *report.CurrentRates != (harnessPricingRates{CacheHitInput: 0.15, CacheMissInput: 4.5, Output: 13.5}) {
		t.Fatalf("holiday off-peak rates = %+v", report.CurrentRates)
	}
	if report.NextChange == nil || report.NextChange.State != "peak" || report.NextChange.At != "2026-10-08T01:00:00Z" {
		t.Fatalf("holiday next transition = %+v", report.NextChange)
	}
}

func TestBuildHarnessPricingReportUnknownAndCalendarExpiry(t *testing.T) {
	unknown := buildHarnessPricingReport(pricingAt(2026, time.September, 14, 10, 0), "future-model")
	if unknown.Priced || unknown.CurrentRates != nil || unknown.PeakRates != nil || unknown.OffPeakRates != nil {
		t.Fatalf("unknown model manufactured rates: %+v", unknown)
	}
	expired := buildHarnessPricingReport(pricingAt(2027, time.January, 4, 10, 0), "deepseek-flash")
	if expired.State != "schedule-only" || expired.HolidayVerified || expired.CurrentRates != nil || expired.NextChange != nil || expired.SavingsPercent != nil {
		t.Fatalf("calendar expiry did not fail closed: %+v", expired)
	}
	if !expired.Priced || expired.PeakRates == nil || expired.OffPeakRates == nil {
		t.Fatalf("catalog rates should remain available for details: %+v", expired)
	}
}

func TestHarnessPricingEndpointWorksWithoutAccountOrImport(t *testing.T) {
	a := &apiServer{dataDir: t.TempDir()}
	// The pricing route is read-only local policy data. Even an inaccessible
	// credential store must not block it or cause a store write.
	if err := os.WriteFile(filepath.Join(a.dataDir, "harness-accounts"), []byte("not a directory"), 0600); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	a.registerHarnessRoutes(mux)
	a.registerExtensionRoutes(mux)
	res := httptest.NewRecorder()
	mux.ServeHTTP(res, httptest.NewRequest(http.MethodGet, "/api/harnesses/deepseek/pricing?model=deepseek-flash", nil))
	if res.Code != http.StatusOK {
		t.Fatalf("pricing without account = %d %s", res.Code, res.Body.String())
	}
	var report harnessPricingReport
	if err := json.Unmarshal(res.Body.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if !report.Priced || report.CanonicalModel != "deepseek-flash" || report.Currency != "CNY" {
		t.Fatalf("pricing response = %+v", report)
	}

	res = httptest.NewRecorder()
	mux.ServeHTTP(res, httptest.NewRequest(http.MethodGet, "/api/harnesses/glm/pricing", nil))
	if res.Code != http.StatusBadRequest {
		t.Fatalf("non-DeepSeek pricing = %d, want 400", res.Code)
	}
}
