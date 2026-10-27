package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const harnessUsageCSV = `start_time_iso,end_time_iso,model,api_key_name,api_key,type,price,amount
2026-09-14T01:00:00Z,2026-09-14T02:00:00Z,deepseek-flash,alice,sk-****a,input_cache_miss_tokens,2,1000000
2026-09-14T01:00:00Z,2026-09-14T02:00:00Z,deepseek-flash,alice,sk-****a,input_cache_hit_tokens,0.04,2000000
2026-09-14T01:00:00Z,2026-09-14T02:00:00Z,deepseek-flash,alice,sk-****a,output_tokens,8,500000
2026-09-14T01:00:00Z,2026-09-14T02:00:00Z,deepseek-flash,alice,sk-****a,request_count,,42
2026-09-14T03:00:00Z,2026-09-14T04:00:00Z,deepseek-v4-pro,bob,sk-****b,input_cache_miss_tokens,4.5,1000
`

func seedHarnessAccount(t *testing.T, mux *http.ServeMux, provider string) {
	t.Helper()
	payload, _ := json.Marshal(harnessTestAccount("main", "sk-test"))
	res := httptest.NewRecorder()
	mux.ServeHTTP(res, httptest.NewRequest("PUT", "/api/harnesses/"+provider+"/accounts", strings.NewReader(string(payload))))
	if res.Code != 200 {
		t.Fatalf("seed account: %d %s", res.Code, res.Body.String())
	}
}

func importUsage(t *testing.T, mux *http.ServeMux, provider, kind, source, body string) harnessUsageReport {
	t.Helper()
	url := "/api/harnesses/" + provider + "/usage/import?kind=" + kind + "&source=" + source
	res := httptest.NewRecorder()
	mux.ServeHTTP(res, httptest.NewRequest("POST", url, strings.NewReader(body)))
	if res.Code != 200 {
		t.Fatalf("import %s %s: %d %s", kind, source, res.Code, res.Body.String())
	}
	var report harnessUsageReport
	if err := json.Unmarshal(res.Body.Bytes(), &report); err != nil {
		t.Fatalf("decode report: %v", err)
	}
	return report
}

func keyRow(t *testing.T, report harnessUsageReport, name string) harnessKeyUsageRow {
	t.Helper()
	for _, row := range report.Keys {
		if row.APIKeyName == name {
			return row
		}
	}
	t.Fatalf("key %q missing from report %+v", name, report)
	return harnessKeyUsageRow{}
}

func TestHarnessUsageImportAndReport(t *testing.T) {
	a := &apiServer{dataDir: t.TempDir()}
	mux := http.NewServeMux()
	a.registerHarnessRoutes(mux)
	a.registerExtensionRoutes(mux)
	seedHarnessAccount(t, mux, "deepseek")

	report := importUsage(t, mux, "deepseek", "amount", "amount-2026-09.csv", harnessUsageCSV)
	if report.RowCount != 5 {
		t.Errorf("rowCount = %d, want 5", report.RowCount)
	}
	if len(report.Sources) != 1 || report.Sources[0] != "amount-2026-09.csv" {
		t.Errorf("sources = %v", report.Sources)
	}
	alice := keyRow(t, report, "alice")
	if alice.CacheMissInputTokens != 1_000_000 || alice.CacheHitInputTokens != 2_000_000 || alice.OutputTokens != 500_000 || alice.Requests != 42 {
		t.Errorf("alice rollup wrong: %+v", alice)
	}
	if alice.Cost < 6.08-1e-9 || alice.Cost > 6.08+1e-9 {
		t.Errorf("alice cost = %v, want 6.08", alice.Cost)
	}
	if len(alice.Models) == 0 || alice.Models[0].Model != "deepseek-flash" {
		t.Errorf("alice per-model split missing: %+v", alice.Models)
	}
	bob := keyRow(t, report, "bob")
	if got := bob.Cost; got < 0.0045-1e-9 || got > 0.0045+1e-9 {
		t.Errorf("bob cost = %v, want 0.0045", got)
	}
	if len(bob.Models) != 1 || bob.Models[0].Model != "deepseek-v4-pro" {
		t.Fatalf("historical Pro usage was rewritten: %+v", bob.Models)
	}

	// GET returns the same persisted report.
	res := httptest.NewRecorder()
	mux.ServeHTTP(res, httptest.NewRequest("GET", "/api/harnesses/deepseek/usage", nil))
	if res.Code != 200 {
		t.Fatalf("GET usage: %d %s", res.Code, res.Body.String())
	}
	var fetched harnessUsageReport
	if err := json.Unmarshal(res.Body.Bytes(), &fetched); err != nil {
		t.Fatal(err)
	}
	if got := keyRow(t, fetched, "alice"); got.Cost != alice.Cost {
		t.Errorf("persisted cost = %v, want %v", got.Cost, alice.Cost)
	}
}

func TestHarnessUsageImportReplacesSameSourceAndSumsOthers(t *testing.T) {
	a := &apiServer{dataDir: t.TempDir()}
	mux := http.NewServeMux()
	a.registerHarnessRoutes(mux)
	a.registerExtensionRoutes(mux)
	seedHarnessAccount(t, mux, "deepseek")

	one := "api_key_name,type,price,amount\nalice,input_cache_miss_tokens,2,1000000\n"
	if got := keyRow(t, importUsage(t, mux, "deepseek", "amount", "sep.csv", one), "alice").Cost; got != 2 {
		t.Fatalf("first import cost = %v", got)
	}
	// Re-importing the same file must replace, not double.
	if got := keyRow(t, importUsage(t, mux, "deepseek", "amount", "sep.csv", one), "alice").Cost; got != 2 {
		t.Fatalf("re-import doubled: %v", got)
	}
	two := "api_key_name,type,price,amount\nalice,output_tokens,8,1000000\n"
	report := importUsage(t, mux, "deepseek", "amount", "oct.csv", two)
	if got := keyRow(t, report, "alice").Cost; got < 10.0-1e-9 || got > 10.0+1e-9 {
		t.Fatalf("multi-source sum = %v, want 10.0", got)
	}
}

func TestHarnessUsageBudgetMarksOverBudget(t *testing.T) {
	a := &apiServer{dataDir: t.TempDir()}
	mux := http.NewServeMux()
	a.registerHarnessRoutes(mux)
	a.registerExtensionRoutes(mux)
	seedHarnessAccount(t, mux, "deepseek")
	importUsage(t, mux, "deepseek", "amount", "sep.csv", harnessUsageCSV)

	if row := keyRow(t, importUsage(t, mux, "deepseek", "amount", "sep.csv", harnessUsageCSV), "alice"); row.OverBudget {
		t.Fatal("no budget yet, must not be over")
	}
	res := httptest.NewRecorder()
	mux.ServeHTTP(res, httptest.NewRequest("PUT", "/api/harnesses/deepseek/usage/budget", strings.NewReader(`{"key":"alice","monthly":0.5}`)))
	if res.Code != 200 {
		t.Fatalf("put budget: %d %s", res.Code, res.Body.String())
	}
	var report harnessUsageReport
	if err := json.Unmarshal(res.Body.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	alice := keyRow(t, report, "alice")
	if !alice.OverBudget || alice.Budget != 0.5 {
		t.Errorf("expected over-budget at 0.5, got budget=%v over=%v", alice.Budget, alice.OverBudget)
	}
}

func TestHarnessUsageRejectsNonDeepSeekAndBadCSV(t *testing.T) {
	a := &apiServer{dataDir: t.TempDir()}
	mux := http.NewServeMux()
	a.registerHarnessRoutes(mux)
	a.registerExtensionRoutes(mux)
	seedHarnessAccount(t, mux, "glm")
	res := httptest.NewRecorder()
	mux.ServeHTTP(res, httptest.NewRequest("GET", "/api/harnesses/glm/usage", nil))
	if res.Code != 400 {
		t.Fatalf("glm usage = %d, want 400", res.Code)
	}

	seedHarnessAccount(t, mux, "deepseek")
	res = httptest.NewRecorder()
	mux.ServeHTTP(res, httptest.NewRequest("POST", "/api/harnesses/deepseek/usage/import?kind=amount", strings.NewReader("model,amount\nx,1\n")))
	if res.Code != 400 {
		t.Fatalf("bad csv = %d, want 400", res.Code)
	}
}

func TestFetchDeepSeekBalanceSanitizes(t *testing.T) {
	var gotAuth, gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotPath = r.URL.Path
		_, _ = w.Write([]byte(`{"is_available":true,"balance_infos":[{"currency":"CNY","total_balance":"12.34","granted_balance":"0.00","topped_up_balance":"12.34","SECRET":"DO-NOT-LEAK"}]}`))
	}))
	defer server.Close()
	report := fetchDeepSeekBalance(context.Background(), server.URL, harnessTestAccount("main", "sk-balance"), server.Client())
	if report.Status != "ok" || !report.Available {
		t.Fatalf("status=%q available=%v", report.Status, report.Available)
	}
	if len(report.Balances) != 1 || report.Balances[0].Currency != "CNY" || report.Balances[0].Total != "12.34" {
		t.Fatalf("balances = %+v", report.Balances)
	}
	if gotAuth != "Bearer sk-balance" || gotPath != "/user/balance" {
		t.Fatalf("wiring: auth=%q path=%q", gotAuth, gotPath)
	}
	if strings.Contains(fmt.Sprintf("%+v", report), "DO-NOT-LEAK") {
		t.Fatal("balance report leaked the provider body")
	}
}

func TestFetchDeepSeekBalanceClassifiesAuthFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"SECRET"}`))
	}))
	defer server.Close()
	if report := fetchDeepSeekBalance(context.Background(), server.URL, harnessTestAccount("main", "sk"), server.Client()); report.Status != "auth-failed" {
		t.Fatalf("status = %q, want auth-failed", report.Status)
	}
}

func TestHarnessBalanceWithoutSelectionReportsNoSelection(t *testing.T) {
	a := &apiServer{dataDir: t.TempDir()}
	mux := http.NewServeMux()
	a.registerHarnessRoutes(mux)
	a.registerExtensionRoutes(mux)
	// No account saved (and so no selection): the handler must answer locally
	// and never reach the network.
	res := httptest.NewRecorder()
	mux.ServeHTTP(res, httptest.NewRequest("GET", "/api/harnesses/deepseek/balance", nil))
	if res.Code != 200 {
		t.Fatalf("balance = %d", res.Code)
	}
	if !strings.Contains(res.Body.String(), `"status":"no-selection"`) {
		t.Fatalf("unexpected balance body: %s", res.Body.String())
	}
}
