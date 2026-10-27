package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	codexTestThread = "01a07e8f-474a-7550-877a-3e8aa8c8a2d1"
	codexTestRoot   = "01a0ecf7-f3ab-7c73-aa9a-1ac74c3670b0"
)

func codexMetaLine(id, session, originator, source string) string {
	return `{"timestamp":"2026-10-01T01:00:00.000Z","type":"session_meta","payload":{"session_id":"` + session +
		`","id":"` + id + `","cwd":"/home/u/proj","originator":"` + originator + `","cli_version":"0.162.1","source":` + source +
		`,"model_provider":"openai","creator_account_id":"acct-1","creator_user_id":"user-1","git":{"branch":"main","repository_url":"git@github.com:acme/proj.git"}}}` + "\n"
}

func codexTurnLine(model string) string {
	return `{"timestamp":"2026-10-01T01:00:01.000Z","type":"turn_context","payload":{"turn_id":"t1","cwd":"/home/u/proj","model":"` + model + `","effort":"high"}}` + "\n"
}

func codexRecordLine(resp string, input, cached, output, reasoning int) string {
	u := map[string]int{"input_tokens": input, "cached_input_tokens": cached, "cache_write_input_tokens": 0,
		"output_tokens": output, "reasoning_output_tokens": reasoning, "total_tokens": input + output}
	ub, _ := json.Marshal(u)
	return `{"timestamp":"2026-10-01T01:00:02.000Z","type":"token_usage_record","payload":{"thread_id":"` + codexTestThread +
		`","turn_id":"t1","session_id":"` + codexTestThread + `","response_id":"` + resp + `","usage":` + string(ub) + `}}` + "\n"
}

func codexTokenCountLine(total, lastIn, lastCached, lastOut int) string {
	last := map[string]int{"input_tokens": lastIn, "cached_input_tokens": lastCached, "cache_write_input_tokens": 0,
		"output_tokens": lastOut, "reasoning_output_tokens": 0, "total_tokens": lastIn + lastOut}
	lb, _ := json.Marshal(last)
	return `{"timestamp":"2026-10-01T01:00:03.000Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"total_tokens":` +
		strconv.Itoa(total) + `},"last_token_usage":` + string(lb) + `}}}` + "\n"
}

func TestExtractCodexUsageNormalizesOpenAITokenClasses(t *testing.T) {
	var st codexFileState
	buf := codexMetaLine(codexTestThread, codexTestThread, "codex-tui", `"cli"`) +
		codexTurnLine("gpt-6.1-sol") +
		codexRecordLine("resp_a", 21804, 11904, 70, 0) +
		// The token_count that follows the record must NOT be counted again.
		codexTokenCountLine(21874, 21804, 11904, 70) +
		codexRecordLine("resp_b", 24878, 11904, 201, 19)
	recs, consumed := extractCodexUsage([]byte(buf), &st)
	if consumed != len(buf) {
		t.Fatalf("consumed %d of %d", consumed, len(buf))
	}
	if len(recs) != 2 {
		t.Fatalf("records = %d, want 2 (token_count ignored once records exist)", len(recs))
	}
	r := recs[0]
	if r.Provider != "codex" || r.Model != "gpt-6.1-sol" || r.MessageID != "resp_a" || r.RequestID != "codex:t1" {
		t.Fatalf("identity = %+v", r)
	}
	if r.InputTokens != 21804-11904 || r.CacheReadTokens != 11904 || r.OutputTokens != 70 || r.CacheCreateTokens != 0 {
		t.Fatalf("token classes = in %d cached %d out %d", r.InputTokens, r.CacheReadTokens, r.OutputTokens)
	}
	if r.TotalTokens != 21804+70 {
		t.Fatalf("total = %d, want the OpenAI total %d", r.TotalTokens, 21804+70)
	}
	if recs[1].ReasoningOutputTokens != 19 || recs[1].OutputTokens != 201 {
		t.Fatalf("reasoning must stay a subset of output: %+v", recs[1])
	}
	if r.SessionID != codexTestThread || r.Cwd != "/home/u/proj" || r.GitBranch != "main" || r.GitRepo != "https://github.com/acme/proj" || r.CCVersion != "0.162.1" {
		t.Fatalf("context = %+v", r)
	}
	if !st.HasRecords || st.AccountID != "acct-1" || st.UserID != "user-1" {
		t.Fatalf("state = %+v", st)
	}
}

func TestExtractCodexUsageTokenCountFallbackForOldCLI(t *testing.T) {
	var st codexFileState
	buf := codexMetaLine(codexTestThread, codexTestThread, "codex-tui", `"cli"`) +
		codexTurnLine("gpt-5.6-sol") +
		codexTokenCountLine(1000, 900, 400, 100) +
		// A repeated token_count for the same step (rate-limit refresh).
		codexTokenCountLine(1000, 900, 400, 100) +
		codexTokenCountLine(2500, 1400, 1000, 100)
	recs, _ := extractCodexUsage([]byte(buf), &st)
	recs = dedupKeepMax(recs)
	if len(recs) != 2 {
		t.Fatalf("records = %d, want 2 distinct steps", len(recs))
	}
	if recs[0].InputTokens != 500 || recs[0].CacheReadTokens != 400 || recs[0].OutputTokens != 100 {
		t.Fatalf("fallback classes = %+v", recs[0])
	}
	if !strings.HasPrefix(recs[0].MessageID, "codex-tc:"+codexTestThread+":") || recs[0].RequestID != "codex-tc:"+codexTestThread {
		t.Fatalf("fallback identity = %s / %s", recs[0].MessageID, recs[0].RequestID)
	}
}

func TestExtractCodexUsageSubagentUsesRootSessionAndSidechain(t *testing.T) {
	var st codexFileState
	child := "01a0ee8e-c70f-70a0-a802-94a6bf5d0c4e"
	buf := codexMetaLine(child, codexTestRoot, "codex-tui", `{"subagent":{"thread_spawn":{"parent_thread_id":"`+codexTestRoot+`"}}}`) +
		codexTurnLine("gpt-6-astra") +
		strings.ReplaceAll(codexRecordLine("resp_c", 100, 0, 10, 0), `"session_id":"`+codexTestThread+`"`, `"session_id":"`+codexTestRoot+`"`)
	recs, _ := extractCodexUsage([]byte(buf), &st)
	if len(recs) != 1 || !recs[0].IsSidechain || recs[0].SessionID != codexTestRoot {
		t.Fatalf("subagent record = %+v", recs)
	}
}

func TestExtractCodexUsageKeepsPartialLineAndFastTier(t *testing.T) {
	var st codexFileState
	full := codexMetaLine(codexTestThread, codexTestThread, "codex-tui", `"cli"`) +
		`{"timestamp":"x","type":"turn_context","payload":{"model":"gpt-6.1-sol","service_tier":"priority"}}` + "\n"
	partial := codexRecordLine("resp_d", 10, 0, 1, 0)
	buf := full + partial[:len(partial)-20]
	recs, consumed := extractCodexUsage([]byte(buf), &st)
	if len(recs) != 0 || consumed != len(full) {
		t.Fatalf("partial line must be left unconsumed: recs=%d consumed=%d want %d", len(recs), consumed, len(full))
	}
	recs, _ = extractCodexUsage([]byte(partial), &st)
	if len(recs) != 1 || recs[0].Model != "gpt-6.1-sol-fast" {
		t.Fatalf("priority tier must price as -fast: %+v", recs)
	}
}

func TestCodexAuthUsageIdentityReadsClaims(t *testing.T) {
	claims := `{"email":"Dev@Example.com","https://api.openai.com/auth":{"chatgpt_account_id":"acct-9","chatgpt_user_id":"user-9"}}`
	jwt := "h." + base64.RawURLEncoding.EncodeToString([]byte(claims)) + ".s"
	auth := `{"tokens":{"id_token":"` + jwt + `","access_token":"x"}}`
	acct, user, email := codexAuthUsageIdentity([]byte(auth))
	if acct != "acct-9" || user != "user-9" || email != "dev@example.com" {
		t.Fatalf("identity = %q %q %q", acct, user, email)
	}
}

func TestCodexAttributionPrefersLedgerThenAccountResolver(t *testing.T) {
	ledger := newInAppLedger(t.TempDir())
	st := codexFileState{ThreadID: codexTestThread, SessionID: codexTestThread, Originator: "codex_exec", AccountID: "acct-1", UserID: "user-1"}
	resolver := func(a, u string) string {
		if a == "acct-1" && u == "user-1" {
			return "personal"
		}
		return ""
	}
	r := codexAttribution(ledger, resolver, st)
	if r.inApp || r.account != "personal" || r.key == "" {
		t.Fatalf("resolver attribution = %+v", r)
	}
	ledger.RecordWorkflow(codexTestThread, "inv-1", "implementer", "", "")
	ledger.RecordAccountAuthoritative(codexTestThread, "lane-acct")
	r2 := codexAttribution(ledger, resolver, st)
	if r2.account != "lane-acct" || r2.workflowInvocationID != "inv-1" || r2.workflowRole != "implementer" || r2.key == r.key {
		t.Fatalf("ledger attribution = %+v", r2)
	}
	st.Originator = "soluterminal_live_auth"
	if !codexAttribution(nil, nil, st).inApp {
		t.Fatal("soluterminal originator must be in-app")
	}
	st.ModelProvider = "solu_team_broker"
	st.Originator = "codex-tui"
	if got := codexAttribution(nil, resolver, st).account; got != "" {
		t.Fatalf("team-broker rollout must not be labelled from its creator: %q", got)
	}
}

func TestLedgerAuthoritativeAccountOutranksHookGuess(t *testing.T) {
	ledger := newInAppLedger(t.TempDir())
	ledger.RecordAccount("s1", "personal")
	ledger.RecordAccountAuthoritative("s1", "lane")
	ledger.RecordAccount("s1", "other")
	ledger.RecordAccountAuthoritative("s1", "later-lane")
	if got := ledger.LookupAccount("s1"); got != "lane" {
		t.Fatalf("account = %q, want first authoritative value", got)
	}
}

// codexTestCloud records every reported record and can be told to fail.
type codexTestCloud struct {
	mu   sync.Mutex
	recs []ccUsageRecord
	fail bool
}

func (c *codexTestCloud) handler(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.fail {
		w.WriteHeader(http.StatusBadGateway)
		return
	}
	var b ccUsageBatch
	_ = json.Unmarshal(body, &b)
	c.recs = append(c.recs, b.Records...)
	w.WriteHeader(http.StatusNoContent)
}

func TestCodexScanOnceIncrementalCursorsAndHorizon(t *testing.T) {
	root := filepath.Join(t.TempDir(), "sessions")
	day := filepath.Join(root, "2026", "10", "01")
	if err := os.MkdirAll(day, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(day, "rollout-2026-10-01T09-00-00-"+codexTestThread+".jsonl")
	head := codexMetaLine(codexTestThread, codexTestThread, "codex-tui", `"cli"`) + codexTurnLine("gpt-6-astra") + codexRecordLine("resp_1", 100, 50, 10, 0)
	if err := os.WriteFile(path, []byte(head), 0o600); err != nil {
		t.Fatal(err)
	}
	oldPath := filepath.Join(day, "rollout-2026-01-01T09-00-00-"+codexTestRoot+".jsonl")
	if err := os.WriteFile(oldPath, []byte(codexMetaLine(codexTestRoot, codexTestRoot, "codex-tui", `"cli"`)+codexTurnLine("gpt-6-sol")+codexRecordLine("resp_old", 5, 0, 1, 0)), 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-200 * 24 * time.Hour)
	if err := os.Chtimes(oldPath, old, old); err != nil {
		t.Fatal(err)
	}

	cloud := &codexTestCloud{}
	srv := httptest.NewServer(http.HandlerFunc(cloud.handler))
	defer srv.Close()
	dataDir := t.TempDir()
	cursors := newCodexCursorStore(dataDir)
	ledger := newInAppLedger(dataDir)
	ctx := context.Background()

	codexScanOnce(ctx, cursors, ledger, srv.Client(), srv.URL, "tok", nil, []string{root}, time.Now())
	if len(cloud.recs) != 1 || cloud.recs[0].MessageID != "resp_1" {
		t.Fatalf("first pass = %+v (old rollout beyond horizon must be skipped)", cloud.recs)
	}

	// Append a new turn; only it is sent, with the model carried in state.
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	_, _ = f.WriteString(codexRecordLine("resp_2", 200, 100, 20, 5))
	_ = f.Close()
	codexScanOnce(ctx, cursors, ledger, srv.Client(), srv.URL, "tok", nil, []string{root}, time.Now())
	if len(cloud.recs) != 2 || cloud.recs[1].MessageID != "resp_2" || cloud.recs[1].Model != "gpt-6-astra" {
		t.Fatalf("incremental pass = %+v", cloud.recs)
	}

	// A failed report holds the cursor so the turn is re-sent next pass.
	f, _ = os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	_, _ = f.WriteString(codexRecordLine("resp_3", 300, 0, 30, 0))
	_ = f.Close()
	cloud.fail = true
	codexScanOnce(ctx, cursors, ledger, srv.Client(), srv.URL, "tok", nil, []string{root}, time.Now())
	cloud.fail = false
	codexScanOnce(ctx, cursors, ledger, srv.Client(), srv.URL, "tok", nil, []string{root}, time.Now())
	if len(cloud.recs) != 3 || cloud.recs[2].MessageID != "resp_3" {
		t.Fatalf("retry pass = %+v", cloud.recs)
	}

	// Growth of the skipped old rollout is scanned, with identity recovered
	// from its leading session_meta.
	f, _ = os.OpenFile(oldPath, os.O_APPEND|os.O_WRONLY, 0)
	_, _ = f.WriteString(codexTurnLine("gpt-6-sol") + strings.ReplaceAll(codexRecordLine("resp_new", 7, 0, 1, 0), codexTestThread, codexTestRoot))
	_ = f.Close()
	codexScanOnce(ctx, cursors, ledger, srv.Client(), srv.URL, "tok", nil, []string{root}, time.Now())
	last := cloud.recs[len(cloud.recs)-1]
	if last.MessageID != "resp_new" || last.SessionID != codexTestRoot || last.Model != "gpt-6-sol" {
		t.Fatalf("resumed old rollout = %+v", last)
	}

	// Stronger attribution later replays the file once so the cloud can
	// enrich the acknowledged rows.
	before := len(cloud.recs)
	ledger.RecordWorkflow(codexTestThread, "inv-7", "reviewer", "", "")
	codexScanOnce(ctx, cursors, ledger, srv.Client(), srv.URL, "tok", nil, []string{root}, time.Now())
	replayed := cloud.recs[before:]
	if len(replayed) != 3 || replayed[0].WorkflowInvocationID != "inv-7" {
		t.Fatalf("replay = %+v", replayed)
	}
	codexScanOnce(ctx, cursors, ledger, srv.Client(), srv.URL, "tok", nil, []string{root}, time.Now())
	if len(cloud.recs) != before+3 {
		t.Fatalf("replay must happen once; got %d new records", len(cloud.recs)-before)
	}
}

func TestExtractCodexUsageForkKeepsOwnIdentity(t *testing.T) {
	var st codexFileState
	child := "01a0ee8e-c70f-70a0-a802-94a6bf5d0c4e"
	// A forked subagent rollout: its own meta first, then a copy of the
	// parent's meta (different id, plain source).
	buf := codexMetaLine(child, codexTestRoot, "codex-tui", `{"subagent":{"thread_spawn":{}}}`) +
		codexMetaLine(codexTestRoot, codexTestRoot, "codex_vscode", `"vscode"`) +
		codexTurnLine("gpt-6-astra") +
		codexRecordLine("resp_f", 100, 0, 10, 0)
	recs, _ := extractCodexUsage([]byte(buf), &st)
	if st.ThreadID != child || !st.Subagent || st.Originator != "codex-tui" {
		t.Fatalf("fork identity = %+v", st)
	}
	if len(recs) != 1 || !recs[0].IsSidechain {
		t.Fatalf("fork record = %+v", recs)
	}
	// A resume of the SAME thread still refreshes per-segment context.
	more := codexMetaLine(child, codexTestRoot, "soluterminal_live_auth", `{"subagent":{}}`)
	_, _ = extractCodexUsage([]byte(more), &st)
	if st.Originator != "soluterminal_live_auth" {
		t.Fatalf("same-thread resume must update context: %+v", st)
	}
}

func TestExtractCodexUsageServiceTierFromThreadSettings(t *testing.T) {
	var st codexFileState
	buf := codexMetaLine(codexTestThread, codexTestThread, "codex-tui", `"cli"`) +
		`{"timestamp":"x","type":"event_msg","payload":{"type":"thread_settings_applied","thread_settings":{"model":"gpt-6.1-sol","service_tier":"priority"}}}` + "\n" +
		// turn_context without a service_tier key must not reset it.
		codexTurnLine("gpt-6.1-sol") +
		codexRecordLine("resp_p", 100, 0, 10, 0) +
		`{"timestamp":"x","type":"event_msg","payload":{"type":"thread_settings_applied","thread_settings":{"model":"gpt-6.1-sol","service_tier":"default"}}}` + "\n" +
		codexRecordLine("resp_q", 100, 0, 10, 0)
	recs, _ := extractCodexUsage([]byte(buf), &st)
	if len(recs) != 2 || recs[0].Model != "gpt-6.1-sol-fast" || recs[1].Model != "gpt-6.1-sol" {
		t.Fatalf("tier models = %+v", recs)
	}
}

func TestCodexAttributionStrongerOnlyOnNewInformation(t *testing.T) {
	k := func(inApp, acct, inv, role string) string {
		return strings.Join([]string{inApp, acct, inv, role}, "\x1f")
	}
	cases := []struct {
		nk, ok string
		want   bool
	}{
		{k("1", "a", "", ""), "", true},
		{k("1", "", "", ""), k("1", "a@x", "", ""), false},
		{k("1", "b", "", ""), k("1", "a", "", ""), false},
		{k("1", "a", "inv", "r"), k("1", "a", "", ""), true},
		{k("1", "a", "", ""), k("0", "a", "", ""), true},
		{"", k("1", "a", "", ""), false},
	}
	for _, c := range cases {
		if got := codexAttributionStronger(c.nk, c.ok); got != c.want {
			t.Errorf("stronger(%q, %q) = %v, want %v", c.nk, c.ok, got, c.want)
		}
	}
}

func TestCloudSupportsUsageProvidersProbe(t *testing.T) {
	for _, tc := range []struct {
		body string
		want bool
	}{{`{"provider":"codex","hero":{}}`, true}, {`{"hero":{}}`, false}} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Query().Get("provider") != "codex" || r.Header.Get("Authorization") != "Bearer tok" {
				w.WriteHeader(400)
				return
			}
			_, _ = w.Write([]byte(tc.body))
		}))
		if got := cloudSupportsUsageProviders(context.Background(), srv.Client(), srv.URL, "tok"); got != tc.want {
			t.Errorf("probe(%s) = %v, want %v", tc.body, got, tc.want)
		}
		srv.Close()
	}
}

func TestHookAccountRetryDueRateLimits(t *testing.T) {
	now := time.Now()
	if !hookAccountRetryDue("sess-rl", now) || hookAccountRetryDue("sess-rl", now.Add(time.Minute)) || !hookAccountRetryDue("sess-rl", now.Add(11*time.Minute)) {
		t.Fatal("retry gate must allow once per interval")
	}
}
