package deepseekusage

import (
	"strings"
	"testing"
	"time"

	"soluterminal-shared/providers"
)

var (
	peakTime    = time.Date(2026, 9, 14, 2, 0, 0, 0, time.UTC)  // Monday 10:00 Shanghai
	offPeakTime = time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC) // Monday 20:00 Shanghai
)

func shanghaiTime(year int, month time.Month, day, hour, minute int) time.Time {
	return time.Date(year, month, day, hour, minute, 0, 0, shanghaiLocation)
}

func TestScheduleHalfOpenBoundaries(t *testing.T) {
	cases := []struct {
		name string
		at   time.Time
		want ScheduleState
	}{
		{"08:59", shanghaiTime(2026, time.September, 14, 8, 59), ScheduleOffPeak},
		{"09:00 opens", shanghaiTime(2026, time.September, 14, 9, 0), SchedulePeak},
		{"11:59 peak", shanghaiTime(2026, time.September, 14, 11, 59), SchedulePeak},
		{"12:00 closes", shanghaiTime(2026, time.September, 14, 12, 0), ScheduleOffPeak},
		{"13:59 off", shanghaiTime(2026, time.September, 14, 13, 59), ScheduleOffPeak},
		{"14:00 opens", shanghaiTime(2026, time.September, 14, 14, 0), SchedulePeak},
		{"17:59 peak", shanghaiTime(2026, time.September, 14, 17, 59), SchedulePeak},
		{"18:00 closes", shanghaiTime(2026, time.September, 14, 18, 0), ScheduleOffPeak},
	}
	for _, tc := range cases {
		if got := ScheduleAt(tc.at); got.State != tc.want || !got.HolidayVerified {
			t.Errorf("%s: ScheduleAt(%s) = %+v, want %s verified", tc.name, tc.at, got, tc.want)
		}
	}
}

func TestScheduleHolidaysWeekendsAndCoverage(t *testing.T) {
	cases := []struct {
		name     string
		at       time.Time
		want     ScheduleState
		holiday  bool
		verified bool
	}{
		{"weekday holiday", shanghaiTime(2025, time.January, 1, 10, 0), ScheduleOffPeak, true, true},
		{"Saturday", shanghaiTime(2026, time.September, 19, 10, 0), ScheduleOffPeak, false, true},
		{"make-up work Sunday stays off", shanghaiTime(2025, time.January, 26, 10, 0), ScheduleOffPeak, false, true},
		{"consecutive National Day closure", shanghaiTime(2026, time.October, 6, 10, 0), ScheduleOffPeak, true, true},
		{"before coverage", shanghaiTime(2024, time.December, 31, 10, 0), ScheduleOnly, false, false},
		{"after coverage", shanghaiTime(2027, time.January, 4, 10, 0), ScheduleOnly, false, false},
	}
	for _, tc := range cases {
		got := ScheduleAt(tc.at)
		if got.State != tc.want || got.Holiday != tc.holiday || got.HolidayVerified != tc.verified {
			t.Errorf("%s: got %+v", tc.name, got)
		}
	}
}

func TestNextTransitionSkipsWeekendsAndConsecutiveHolidays(t *testing.T) {
	cases := []struct {
		name  string
		at    time.Time
		state ScheduleState
		want  time.Time
		ok    bool
	}{
		{"first close", shanghaiTime(2026, time.September, 14, 10, 0), ScheduleOffPeak, shanghaiTime(2026, time.September, 14, 12, 0), true},
		{"Friday night through weekend", shanghaiTime(2026, time.September, 18, 19, 0), SchedulePeak, shanghaiTime(2026, time.September, 21, 9, 0), true},
		{"consecutive National Day closure", shanghaiTime(2026, time.September, 30, 19, 0), SchedulePeak, shanghaiTime(2026, time.October, 8, 9, 0), true},
		{"coverage expires", shanghaiTime(2026, time.December, 31, 19, 0), "", time.Time{}, false},
		{"outside coverage", shanghaiTime(2027, time.January, 4, 10, 0), "", time.Time{}, false},
	}
	for _, tc := range cases {
		got, ok := NextTransition(tc.at)
		if ok != tc.ok || (ok && (got.State != tc.state || !got.At.Equal(tc.want))) {
			t.Errorf("%s: got %+v ok=%v, want %s %s ok=%v", tc.name, got, ok, tc.state, tc.want, tc.ok)
		}
	}
}

func TestCostHalfOffPeak(t *testing.T) {
	turn := Tokens{CacheHitInputTokens: 1_000_000, CacheMissInputTokens: 1_000_000, OutputTokens: 1_000_000}
	peak, ok := Cost("deepseek-flash", turn, peakTime)
	if !ok {
		t.Fatal("deepseek-flash should be priced")
	}
	wantPeak := 0.04 + 2 + 8
	if diff := peak - wantPeak; diff > 1e-9 || diff < -1e-9 {
		t.Errorf("peak cost = %v, want %v", peak, wantPeak)
	}
	offPeak, ok := Cost("deepseek-flash", turn, offPeakTime)
	if !ok {
		t.Fatal("deepseek-flash should be priced")
	}
	if want := wantPeak / 2; offPeak != want {
		t.Errorf("off-peak cost = %v, want %v (half of peak)", offPeak, want)
	}
}

func TestCostModelAliasesAndUnknown(t *testing.T) {
	turn := Tokens{CacheMissInputTokens: 1_000_000}
	alias, ok := Cost("deepseek-v4-flash", turn, peakTime)
	if !ok || alias != 2 {
		t.Errorf("legacy deepseek-v4-flash should bill at flash: got %v ok=%v", alias, ok)
	}
	pro, ok := Cost("deepseek-v4-pro", turn, peakTime)
	if !ok || pro != 9 {
		t.Errorf("deepseek-v4-pro cache-miss: got %v ok=%v", pro, ok)
	}
	if _, ok := Cost("deepseek-chat", turn, peakTime); ok {
		t.Error("retired/unknown model must not be priced silently")
	}
}

func TestRetiredProPricingRemainsHistoricalAndDistinctFromDispatch(t *testing.T) {
	turn := Tokens{CacheMissInputTokens: 1_000_000}
	pro, proOK := Cost("deepseek-v4-pro", turn, peakTime)
	flash, flashOK := Cost(providers.DeepSeekFlashModel, turn, peakTime)
	if !proOK || !flashOK || pro != 9 || flash != 2 || pro == flash {
		t.Fatalf("historical pricing pro=%v/%v flash=%v/%v", pro, proOK, flash, flashOK)
	}
	provider, ok := providers.Get("deepseek")
	if !ok {
		t.Fatal("DeepSeek provider missing")
	}
	if _, selectable := provider.Models.Tier("deepseek-v4-pro"); selectable {
		t.Fatal("historical Pro pricing key leaked back into the dispatch catalog")
	}
}

func TestCanonicalCatalogRatesAndUncertainCost(t *testing.T) {
	catalog := Catalog()
	if got := catalog["deepseek-flash"]; got != (Rates{CacheHitInput: 0.04, CacheMissInput: 2, Output: 8}) {
		t.Fatalf("flash peak rates = %+v", got)
	}
	if got := catalog["deepseek-v4-pro"]; got != (Rates{CacheHitInput: 0.30, CacheMissInput: 9, Output: 27}) {
		t.Fatalf("pro peak rates = %+v", got)
	}
	catalog["deepseek-flash"] = Rates{}
	if got, _ := PeakRates("deepseek-flash"); got.CacheMissInput != 2 {
		t.Fatal("Catalog must return a defensive copy")
	}
	off, ok := CurrentRates("deepseek-v4-flash", offPeakTime)
	if !ok || off != (Rates{CacheHitInput: 0.02, CacheMissInput: 1, Output: 4}) {
		t.Fatalf("alias off-peak rates = %+v ok=%v", off, ok)
	}
	turn := Tokens{CacheMissInputTokens: 1}
	if _, ok := Cost("deepseek-flash", turn, shanghaiTime(2027, time.January, 4, 10, 0)); ok {
		t.Fatal("cost outside verified holiday coverage must be unpriced")
	}
	if Currency != "CNY" {
		t.Fatalf("currency = %q", Currency)
	}
}

const amountCSV = `start_time_iso,end_time_iso,model,api_key_name,api_key,type,price,amount
2026-09-14T01:00:00Z,2026-09-14T02:00:00Z,deepseek-flash,alice,sk-****a,input_cache_miss_tokens,2,1000000
2026-09-14T01:00:00Z,2026-09-14T02:00:00Z,deepseek-flash,alice,sk-****a,input_cache_hit_tokens,0.04,2000000
2026-09-14T01:00:00Z,2026-09-14T02:00:00Z,deepseek-flash,alice,sk-****a,output_tokens,8,500000
2026-09-14T01:00:00Z,2026-09-14T02:00:00Z,deepseek-flash,alice,sk-****a,request_count,,42
2026-09-14T03:00:00Z,2026-09-14T04:00:00Z,deepseek-v4-pro,bob,sk-****b,input_cache_miss_tokens,4.5,1000
`

func TestParseAmountCSVAndAggregate(t *testing.T) {
	rows, err := ParseAmountCSV(strings.NewReader(amountCSV))
	if err != nil {
		t.Fatalf("ParseAmountCSV: %v", err)
	}
	if len(rows) != 5 {
		t.Fatalf("rows = %d, want 5", len(rows))
	}
	if rows[0].APIKeyName != "alice" || rows[0].Amount != 1_000_000 || rows[0].Type != "input_cache_miss_tokens" {
		t.Errorf("first row parsed wrong: %+v", rows[0])
	}
	byKey := AggregateAmounts(rows)
	alice := byKey["alice"]
	if alice.CacheMissInputTokens != 1_000_000 || alice.CacheHitInputTokens != 2_000_000 || alice.OutputTokens != 500_000 || alice.Requests != 42 {
		t.Errorf("alice rollup wrong: %+v", alice)
	}
	if byKey["bob"].CacheMissInputTokens != 1000 {
		t.Errorf("bob rollup wrong: %+v", byKey["bob"])
	}
	// Export-priced cost (CNY): 1M miss @2 + 2M hit @0.04 + 0.5M out @8.
	if want := 6.08; alice.Cost < want-1e-9 || alice.Cost > want+1e-9 {
		t.Errorf("alice cost = %v, want %v", alice.Cost, want)
	}
	// Per-model split keeps the same key isolated by model.
	byModel := AggregateByKeyModel(rows)
	if got := byModel["alice"]["deepseek-flash"].Cost; got < 6.08-1e-9 || got > 6.08+1e-9 {
		t.Errorf("alice/flash cost = %v, want 6.08", got)
	}
	if got := byModel["bob"]["deepseek-v4-pro"].CacheMissInputTokens; got != 1000 {
		t.Errorf("bob/pro cost tokens = %d, want 1000", got)
	}
}

func TestParseAmountCSVHeaderOrderAndCase(t *testing.T) {
	shuffled := "TYPE,Amount,API_KEY_NAME,price,model,start_time_iso,end_time_iso,api_key\n" +
		"output_tokens,7,carol,1.20,deepseek-flash,a,b,sk-****c\n"
	rows, err := ParseAmountCSV(strings.NewReader(shuffled))
	if err != nil {
		t.Fatalf("ParseAmountCSV: %v", err)
	}
	if len(rows) != 1 || rows[0].APIKeyName != "carol" || rows[0].Amount != 7 {
		t.Fatalf("order/case-insensitive parse failed: %+v", rows)
	}
}

func TestParseAmountCSVMissingKeyColumn(t *testing.T) {
	if _, err := ParseAmountCSV(strings.NewReader("model,amount\ndeepseek-flash,1\n")); err == nil {
		t.Fatal("expected an error when api_key_name is absent")
	}
}

func TestParseAmountCSVUnknownTypeIsCounted(t *testing.T) {
	csv := "api_key_name,type,amount\ndave,input_cache_write_tokens,9\n"
	rows, err := ParseAmountCSV(strings.NewReader(csv))
	if err != nil {
		t.Fatalf("ParseAmountCSV: %v", err)
	}
	if got := AggregateAmounts(rows)["dave"].UnknownAmounts; got != 9 {
		t.Errorf("unknown amount = %d, want 9", got)
	}
}

func TestParseCostCSV(t *testing.T) {
	csv := "start_time_iso,end_time_iso,model,wallet_type,cost,currency\n" +
		"2026-09-14T00:00:00Z,2026-09-15T00:00:00Z,deepseek-flash,Paid,-12.34,CNY\n"
	rows, err := ParseCostCSV(strings.NewReader(csv))
	if err != nil {
		t.Fatalf("ParseCostCSV: %v", err)
	}
	if len(rows) != 1 || rows[0].Model != "deepseek-flash" || rows[0].Cost != -12.34 || rows[0].Currency != "CNY" {
		t.Fatalf("cost row parsed wrong: %+v", rows)
	}
	if _, err := ParseCostCSV(strings.NewReader("model,amount\nx,1\n")); err == nil {
		t.Fatal("expected an error when cost column is absent")
	}
}
