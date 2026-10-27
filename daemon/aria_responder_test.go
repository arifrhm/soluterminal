package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

const testAgentKey = "ariak_0123456789abcdef0123456789abcdef0123456789abcdef"
const testAgentKey2 = "ariak_fedcba9876543210fedcba9876543210fedcba9876543210"

func testSupervisor(t *testing.T) *AriaResponder {
	t.Helper()
	dir := t.TempDir()
	st, err := newAriaResponderStore(dir)
	if err != nil {
		t.Fatalf("newAriaResponderStore: %v", err)
	}
	return newAriaResponder(st, nil, nil, dir)
}

func testRunner(t *testing.T) *ariaAgentRunner {
	t.Helper()
	cfg := ariaAgentConfig{ID: ariaDefaultAgentID, Enabled: true, AgentKey: testAgentKey}
	return newAriaAgentRunner(cfg, nil, nil, t.TempDir(), http.DefaultClient)
}

// suppressFlusher marks the flusher as already running so queue mutations in
// tests never launch the real goroutine (which would race the assertions).
func suppressFlusher(r *ariaAgentRunner) {
	r.queueMu.Lock()
	r.flusherOn = true
	r.queueMu.Unlock()
}

func TestCollectAriaMentions(t *testing.T) {
	t.Parallel()
	alice := "alice"
	out := &ariaWaitResponse{
		MentionHandle: "helper",
		Updates: []ariaWaitUpdate{
			{Source: "wa", ChatID: "c1", Messages: []ariaWaitMessage{
				{Sender: &alice, Text: "hey @helper", Mentioned: true, Timestamp: "t1"},
				{Sender: &alice, Text: "unrelated", Mentioned: false},
			}},
			{Source: "telegram", ChatID: "c2", Messages: []ariaWaitMessage{
				{Sender: nil, Text: "@helper ping", Mentioned: true, Timestamp: "t2"},
			}},
		},
	}
	got := collectAriaMentions(out)
	if len(got) != 2 {
		t.Fatalf("got %d mentions, want 2", len(got))
	}
	if got[0].Sender != "alice" || got[0].Source != "wa" {
		t.Errorf("mention[0] = %+v", got[0])
	}
	if got[1].Sender != "someone" {
		t.Errorf("nil sender should fall back to %q, got %q", "someone", got[1].Sender)
	}
}

func TestAriaAgentDTOMasksKey(t *testing.T) {
	t.Parallel()
	dto := ariaAgentToDTO(ariaAgentConfig{ID: "a1", AgentKey: testAgentKey, Handle: "helper"})
	if !dto.HasKey || dto.Handle != "helper" {
		t.Fatalf("dto = %+v", dto)
	}
	if dto.KeyPrefix != testAgentKey[:12] {
		t.Errorf("KeyPrefix = %q", dto.KeyPrefix)
	}
	b, _ := json.Marshal(dto)
	if strings.Contains(string(b), testAgentKey[12:]) {
		t.Fatalf("full key leaked into DTO JSON: %s", b)
	}
}

func TestAriaBackoffLadder(t *testing.T) {
	t.Parallel()
	cases := []struct {
		streak, status int
		want           time.Duration
	}{
		{1, 0, 5 * time.Second},
		{3, 500, 15 * time.Second},
		{100, 0, ariaResponderBackoffMax},
		{1, http.StatusUnauthorized, ariaResponderAuthBackoff},
		{7, http.StatusForbidden, ariaResponderAuthBackoff},
		{1, http.StatusConflict, 2 * time.Second},
	}
	for _, c := range cases {
		if got := ariaBackoff(c.streak, c.status); got != c.want {
			t.Errorf("ariaBackoff(%d, %d) = %s, want %s", c.streak, c.status, got, c.want)
		}
	}
}

func TestLegacySettingsMigrateToAgents(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	legacy := `{"enabled":true,"agent_key":"` + testAgentKey + `","pane_id":"pane_1","permission":"edit"}`
	if err := os.WriteFile(filepath.Join(dir, "aria_responder.json"), []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	// A queued-mentions file from the v1 build must follow the agent.
	if err := os.WriteFile(filepath.Join(dir, "aria_queue.json"), []byte(`{"handle":"h","mentions":[{"source":"wa","chat_id":"c","enqueued_at":"2026-07-18T00:00:00Z"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	st, err := newAriaResponderStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	cfg := st.get()
	if len(cfg.Agents) != 1 {
		t.Fatalf("agents = %+v, want 1", cfg.Agents)
	}
	a := cfg.Agents[0]
	if a.ID != ariaDefaultAgentID || !a.Enabled || a.AgentKey != testAgentKey || a.PaneID != "pane_1" || a.Permission != "edit" || a.McpName != "aria-comms" {
		t.Fatalf("migrated agent = %+v", a)
	}
	if _, err := os.Stat(ariaQueuePath(dir, ariaDefaultAgentID)); err != nil {
		t.Errorf("legacy queue file not adopted: %v", err)
	}
}

func TestLegacyKeylessButConfiguredMigrates(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	// A v1 user who set a custom gateway + pane but never pasted a key and
	// left it disabled must NOT lose those fields on upgrade.
	legacy := `{"enabled":false,"gateway_url":"https://gw.custom","pane_id":"pane_9","permission":"edit"}`
	if err := os.WriteFile(filepath.Join(dir, "aria_responder.json"), []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	st, err := newAriaResponderStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	cfg := st.get()
	if len(cfg.Agents) != 1 {
		t.Fatalf("configured-but-keyless v1 file should migrate, got %+v", cfg.Agents)
	}
	if cfg.Agents[0].GatewayURL != "https://gw.custom" || cfg.Agents[0].PaneID != "pane_9" {
		t.Fatalf("custom fields lost in migration: %+v", cfg.Agents[0])
	}

	// A truly-empty/default v1 file stays empty (nothing worth migrating).
	dir2 := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir2, "aria_responder.json"), []byte(`{"enabled":false}`), 0o600)
	st2, _ := newAriaResponderStore(dir2)
	if len(st2.get().Agents) != 0 {
		t.Fatalf("empty v1 file should not synthesize an agent")
	}
}

func TestTruncateRunes(t *testing.T) {
	t.Parallel()
	if got := truncateRunes("hello", 100, "…"); got != "hello" {
		t.Errorf("under budget = %q", got)
	}
	got := truncateRunes(strings.Repeat("a", 50), 10, "…")
	if len(got) > 10 || !strings.HasSuffix(got, "…") {
		t.Errorf("truncated = %q (len %d)", got, len(got))
	}
	// Suffix longer than max → clamped, never a negative slice.
	if got := truncateRunes("abcdef", 2, "……"); len(got) > 6 {
		t.Errorf("clamp = %q", got)
	}
	// Multibyte boundary respected.
	multi := truncateRunes(strings.Repeat("界", 20), 10, "…")
	if !utf8.ValidString(multi) {
		t.Errorf("truncation split a rune: %q", multi)
	}
}

func TestQueueCapDropsOldest(t *testing.T) {
	t.Parallel()
	r := testRunner(t)
	suppressFlusher(r)
	var ms []ariaMention
	for i := 0; i < ariaPaneQueueMaxLen+5; i++ {
		ms = append(ms, ariaMention{Source: "wa", ChatID: "c", Text: strings.Repeat("x", i%3+1)})
	}
	r.enqueueForPane("h", "", ms[:10])
	r.enqueueForPane("h", "", ms[10:])
	batch, handle, _ := r.peekQueue()
	if handle != "h" {
		t.Errorf("handle = %q", handle)
	}
	if len(batch) != ariaPaneQueueMaxLen {
		t.Fatalf("queue len = %d, want cap %d", len(batch), ariaPaneQueueMaxLen)
	}
	// The 5 dropped entries must be the OLDEST (front of the line).
	if batch[0].Text != ms[5].Text {
		t.Errorf("front of queue = %q, want %q", batch[0].Text, ms[5].Text)
	}
}

func TestQueuePersistAndResume(t *testing.T) {
	t.Parallel()
	r := testRunner(t)
	suppressFlusher(r)
	r.enqueueForPane("helper", "be nice", []ariaMention{{Source: "wa", ChatID: "c1", Text: "hi"}})
	if _, err := os.Stat(r.queuePath()); err != nil {
		t.Fatalf("queue file not written: %v", err)
	}

	// A fresh runner over the same dataDir + agent id resumes the same queue.
	r2 := newAriaAgentRunner(r.cfg, nil, nil, r.dataDir, http.DefaultClient)
	suppressFlusher(r2)
	r2.resumeQueue()
	batch, handle, persona := r2.peekQueue()
	if len(batch) != 1 || batch[0].Text != "hi" || handle != "helper" || persona != "be nice" {
		t.Fatalf("resumed queue = %+v handle=%q persona=%q", batch, handle, persona)
	}

	// Draining removes the file so a later restart resumes nothing.
	r2.dropDelivered(1)
	if _, err := os.Stat(r2.queuePath()); !os.IsNotExist(err) {
		t.Errorf("queue file should be gone after drain, stat err = %v", err)
	}
}

func TestExpiredPrefix(t *testing.T) {
	t.Parallel()
	now := time.Now()
	qs := []ariaQueuedMention{
		{EnqueuedAt: now.Add(-40 * time.Minute)},
		{EnqueuedAt: now.Add(-31 * time.Minute)},
		{EnqueuedAt: now.Add(-5 * time.Minute)},
	}
	if n := expiredPrefix(qs, now.Add(-30*time.Minute)); n != 2 {
		t.Errorf("expiredPrefix = %d, want 2", n)
	}
	if n := expiredPrefix(qs, now.Add(-time.Hour)); n != 0 {
		t.Errorf("expiredPrefix(all fresh) = %d, want 0", n)
	}
}

func TestWriteAriaMcpConfig(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path, err := writeAriaMcpConfig(dir, "https://gw.example", testAgentKey, "aria-comms-helper")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(path)
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("mcp config mode = %v, want 0600", fi.Mode().Perm())
	}
	b, _ := os.ReadFile(path)
	var cfg struct {
		McpServers map[string]struct {
			URL     string            `json:"url"`
			Headers map[string]string `json:"headers"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal(b, &cfg); err != nil {
		t.Fatalf("config is not valid JSON: %v", err)
	}
	srv, ok := cfg.McpServers["aria-comms-helper"]
	if !ok || srv.URL != "https://gw.example/mcp" || srv.Headers["Authorization"] != "Bearer "+testAgentKey {
		t.Fatalf("config = %s", b)
	}
}

func TestAriaGatewaySendTruncates(t *testing.T) {
	var got struct {
		Source string `json:"source"`
		ChatID string `json:"chat_id"`
		Text   string `json:"text"`
	}
	gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/send" {
			t.Errorf("path = %s", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Errorf("decode: %v", err)
		}
		w.Write([]byte(`{"ok":true}`))
	}))
	defer gw.Close()
	long := strings.Repeat("a", ariaSendMaxChars+500)
	if err := ariaGatewaySend(t.Context(), gw.Client(), gw.URL, testAgentKey, "wa", "c1", long); err != nil {
		t.Fatal(err)
	}
	if got.Source != "wa" || got.ChatID != "c1" {
		t.Errorf("payload = %+v", got)
	}
	if len(got.Text) > ariaSendMaxChars {
		t.Errorf("text len = %d, want ≤ %d", len(got.Text), ariaSendMaxChars)
	}
	if !strings.HasSuffix(got.Text, "…") {
		t.Errorf("truncated text should end with ellipsis")
	}
}

func TestDeriveMcpName(t *testing.T) {
	t.Parallel()
	cfg := ariaResponderConfig{Agents: []ariaAgentConfig{
		{ID: "agent_default", McpName: "aria-comms"},
		{ID: "agent_beef", Handle: "Help-Er 2"},
	}}
	if got := deriveMcpName(cfg, &cfg.Agents[1]); got != "aria-comms-help-er2" {
		t.Errorf("deriveMcpName = %q", got)
	}
	// First agent keeps the historical name.
	solo := ariaResponderConfig{Agents: []ariaAgentConfig{{ID: "agent_default"}}}
	if got := deriveMcpName(solo, &solo.Agents[0]); got != "aria-comms" {
		t.Errorf("solo agent name = %q", got)
	}
	// No handle → id suffix.
	cfg.Agents[1].Handle = ""
	if got := deriveMcpName(cfg, &cfg.Agents[1]); got != "aria-comms-beef" {
		t.Errorf("no-handle name = %q", got)
	}
}

func TestReconcileStartsAndStopsRunners(t *testing.T) {
	// Closed server: runners error instantly and back off — no real traffic.
	gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	url := gw.URL
	gw.Close()

	sup := testSupervisor(t)
	cfg := ariaResponderConfig{Agents: []ariaAgentConfig{
		{ID: "a1", Enabled: true, AgentKey: testAgentKey, GatewayURL: url},
		{ID: "a2", Enabled: false, AgentKey: testAgentKey2, GatewayURL: url},
	}}
	if err := sup.store.set(cfg); err != nil {
		t.Fatal(err)
	}
	sup.reconcile()
	sup.mu.Lock()
	_, hasA1 := sup.runners["a1"]
	_, hasA2 := sup.runners["a2"]
	n := len(sup.runners)
	sup.mu.Unlock()
	if !hasA1 || hasA2 || n != 1 {
		t.Fatalf("runners after reconcile: a1=%v a2=%v n=%d", hasA1, hasA2, n)
	}

	// Editing a1 swaps its runner; disabling removes it.
	cfg.Agents[0].PaneID = "pane_9"
	_ = sup.store.set(cfg)
	sup.reconcile()
	sup.mu.Lock()
	swapped := sup.runners["a1"]
	sup.mu.Unlock()
	if swapped == nil || swapped.cfg.PaneID != "pane_9" {
		t.Fatalf("runner not swapped on config edit: %+v", swapped)
	}

	cfg.Agents[0].Enabled = false
	_ = sup.store.set(cfg)
	sup.reconcile()
	sup.mu.Lock()
	n = len(sup.runners)
	sup.mu.Unlock()
	if n != 0 {
		t.Fatalf("runners after disable = %d, want 0", n)
	}
	if swapped.ctx.Err() == nil {
		t.Error("stopped runner's context should be cancelled")
	}
}

// putResponder drives the real handler and returns the recorder.
func putResponder(t *testing.T, a *apiServer, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPut, "/api/aria/responder", strings.NewReader(body))
	rec := httptest.NewRecorder()
	a.putAriaResponder(rec, req)
	return rec
}

func TestPutAriaResponderValidation(t *testing.T) {
	infoOK := `{"ok":true,"agent":"Helper","mention_handle":"helper","responder_mode":"self","chats":[{"source":"wa","chat_id":"c1"}]}`
	gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(infoOK))
	}))
	defer gw.Close()

	a := &apiServer{ariaResponder: testSupervisor(t)}
	a.ariaResponder.httpc = gw.Client()

	// Malformed key → 400, nothing stored.
	if rec := putResponder(t, a, `{"agentKey":"nope"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("malformed key: code = %d", rec.Code)
	}

	// Enable without key → 400.
	if rec := putResponder(t, a, `{"enabled":true}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("enable without key: code = %d", rec.Code)
	}

	// Keyless default-agent PUT (set gateway first, paste key later) must
	// SUCCEED — the two-step pairing flow. Only an explicit `id:"new"` add
	// requires a key up front.
	if rec := putResponder(t, a, `{"gatewayUrl":"`+gw.URL+`"}`); rec.Code != http.StatusOK {
		t.Fatalf("keyless gateway PUT: code = %d body=%s", rec.Code, rec.Body)
	}
	if got := a.ariaResponder.store.get(); len(got.Agents) != 1 || got.Agents[0].GatewayURL != gw.URL || got.Agents[0].AgentKey != "" {
		t.Fatalf("keyless default agent = %+v", got.Agents)
	}
	if rec := putResponder(t, a, `{"id":"new","gatewayUrl":"`+gw.URL+`"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("keyless explicit-new should 400, got %d", rec.Code)
	}

	// Valid key + healthy gateway → saved as the default agent, verified,
	// handle cached (the legacy id-less PUT contract Aria pairing uses).
	body := `{"agentKey":"` + testAgentKey + `","gatewayUrl":"` + gw.URL + `"}`
	rec := putResponder(t, a, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("valid key: code = %d body=%s", rec.Code, rec.Body)
	}
	var dto ariaResponderDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &dto); err != nil {
		t.Fatal(err)
	}
	if dto.Verify == nil || !dto.Verify.Ok || dto.Verify.Handle != "helper" || dto.Verify.GrantedChats != 1 {
		t.Fatalf("verify = %+v", dto.Verify)
	}
	got := a.ariaResponder.store.get()
	if len(got.Agents) != 1 || got.Agents[0].ID != ariaDefaultAgentID || got.Agents[0].AgentKey != testAgentKey || got.Agents[0].Handle != "helper" || got.Agents[0].McpName != "aria-comms" {
		t.Fatalf("stored = %+v", got.Agents)
	}

	// Field-only update must not clobber the key, and returns no verify.
	rec = putResponder(t, a, `{"paneId":"pane_1"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("paneId update: code = %d", rec.Code)
	}
	dto = ariaResponderDTO{}
	_ = json.Unmarshal(rec.Body.Bytes(), &dto)
	if dto.Verify != nil {
		t.Errorf("field-only update should not verify, got %+v", dto.Verify)
	}
	got = a.ariaResponder.store.get()
	if got.Agents[0].AgentKey != testAgentKey || got.Agents[0].PaneID != "pane_1" {
		t.Fatalf("stored after pane update = %+v", got.Agents)
	}
}

func TestPutAriaResponderSecondAgent(t *testing.T) {
	infoOK := `{"ok":true,"mention_handle":"scout","responder_mode":"self","chats":[]}`
	gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(infoOK))
	}))
	defer gw.Close()
	a := &apiServer{ariaResponder: testSupervisor(t)}
	a.ariaResponder.httpc = gw.Client()

	// Seed the default agent.
	if rec := putResponder(t, a, `{"agentKey":"`+testAgentKey+`","gatewayUrl":"`+gw.URL+`"}`); rec.Code != http.StatusOK {
		t.Fatalf("seed: %d %s", rec.Code, rec.Body)
	}

	// id:"new" without a key → 400.
	if rec := putResponder(t, a, `{"id":"new"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("new without key: code = %d", rec.Code)
	}

	// A second agent gets its own id and a handle-derived MCP name.
	rec := putResponder(t, a, `{"id":"new","agentKey":"`+testAgentKey2+`","gatewayUrl":"`+gw.URL+`"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("second agent: %d %s", rec.Code, rec.Body)
	}
	got := a.ariaResponder.store.get()
	if len(got.Agents) != 2 {
		t.Fatalf("agents = %+v", got.Agents)
	}
	second := got.Agents[1]
	if second.ID == ariaDefaultAgentID || second.ID == "" || second.McpName != "aria-comms-scout" || second.Handle != "scout" {
		t.Fatalf("second agent = %+v", second)
	}

	// Unknown id → 404.
	if rec := putResponder(t, a, `{"id":"agent_missing","model":"x"}`); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown id: code = %d", rec.Code)
	}

	// Editing the second agent by id touches only it.
	rec = putResponder(t, a, `{"id":"`+second.ID+`","paneId":"pane_7"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("edit second: %d", rec.Code)
	}
	got = a.ariaResponder.store.get()
	if got.Agents[1].PaneID != "pane_7" || got.Agents[0].PaneID != "" {
		t.Fatalf("per-agent edit leaked: %+v", got.Agents)
	}

	// DELETE removes it.
	req := httptest.NewRequest(http.MethodDelete, "/api/aria/responder/agents/"+second.ID, nil)
	req.SetPathValue("id", second.ID)
	drec := httptest.NewRecorder()
	a.deleteAriaResponderAgent(drec, req)
	if drec.Code != http.StatusOK {
		t.Fatalf("delete: %d %s", drec.Code, drec.Body)
	}
	if got := a.ariaResponder.store.get(); len(got.Agents) != 1 {
		t.Fatalf("agents after delete = %+v", got.Agents)
	}
}

func TestPutAriaResponderRejectedKeyNeverSaves(t *testing.T) {
	gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"ok":false,"error":"unknown key"}`, http.StatusUnauthorized)
	}))
	defer gw.Close()
	a := &apiServer{ariaResponder: testSupervisor(t)}
	a.ariaResponder.httpc = gw.Client()

	body := `{"agentKey":"` + testAgentKey + `","gatewayUrl":"` + gw.URL + `"}`
	rec := putResponder(t, a, body)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("rejected key: code = %d, want 400", rec.Code)
	}
	if got := a.ariaResponder.store.get(); len(got.Agents) != 0 {
		t.Fatalf("rejected key was stored: %+v", got.Agents)
	}
}

func TestPutAriaResponderUnreachableGatewaySavesUnverified(t *testing.T) {
	gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	url := gw.URL
	gw.Close() // now unreachable

	a := &apiServer{ariaResponder: testSupervisor(t)}
	body := `{"agentKey":"` + testAgentKey + `","gatewayUrl":"` + url + `"}`
	rec := putResponder(t, a, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("unreachable gateway: code = %d body=%s", rec.Code, rec.Body)
	}
	var dto ariaResponderDTO
	_ = json.Unmarshal(rec.Body.Bytes(), &dto)
	if dto.Verify == nil || dto.Verify.Ok || dto.Verify.Error == "" {
		t.Fatalf("verify = %+v, want ok:false with error", dto.Verify)
	}
	if got := a.ariaResponder.store.get(); len(got.Agents) != 1 || got.Agents[0].AgentKey != testAgentKey {
		t.Fatalf("key should save despite outage: %+v", got.Agents)
	}
}
