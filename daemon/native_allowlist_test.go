package main

import (
	"bytes"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

// native_allowlist_test.go covers the per-pane tool-approval allowlist ("always
// allow this tool") on the daemon side:
//   - on-arrival auto-allow: an allowlisted tool's can_use_tool is resolved as
//     ALLOW for its EXACT RequestID with NO pending card, while a
//     non-allowlisted tool in the same burst is still parked;
//   - GET/POST endpoint round-trip (add → list → remove → gone) + error cases;
//   - persistence across a snapshot / FromPane round-trip.

// TestAllowlistAutoAllowsOnArrival is the core behavior test: adding "Read" to
// the pane's allowlist makes an incoming can_use_tool[Read] auto-allow on
// arrival (correct control_response for that exact RequestID, NO pending card),
// while a can_use_tool[Bash] in the SAME burst stays parked for the user.
func TestAllowlistAutoAllowsOnArrival(t *testing.T) {
	t.Parallel()
	p, agentEnd := nativePaneForApproval(t, "pane_allowlist_autoallow")

	// User opts in to always-allow Read for this pane.
	tools := p.allowlistAddTool("Read")
	if len(tools) != 1 || tools[0] != "Read" {
		t.Fatalf("allowlistAddTool(Read) = %v, want [Read]", tools)
	}

	const (
		readReqID = "019c298e-1111-aaaa-bbbb-allowlistread01"
		readUseID = "toolu_01ReadAllowlistAutoAllow"
		bashReqID = "019c298e-2222-cccc-dddd-allowlistbash01"
		bashUseID = "toolu_01BashAllowlistAutoAllow"
	)
	readReq := `{"type":"control_request","request_id":"` + readReqID + `","request":{"subtype":"can_use_tool","tool_name":"Read","tool_use_id":"` + readUseID + `","input":{"file_path":"/tmp/safe.txt"}}}`
	bashReq := `{"type":"control_request","request_id":"` + bashReqID + `","request":{"subtype":"can_use_tool","tool_name":"Bash","tool_use_id":"` + bashUseID + `","input":{"command":"rm -rf /","description":"dangerous"}}}`

	// Read the auto-allow control_response off the agent end concurrently (the
	// emit path writes it synchronously over net.Pipe).
	frameCh := make(chan []byte, 1)
	go func() {
		_ = agentEnd.SetReadDeadline(time.Now().Add(2 * time.Second))
		_, fr, err := ReadFrame(agentEnd)
		if err != nil {
			frameCh <- nil
			return
		}
		frameCh <- fr
	}()

	// Feed the Read FIRST — it must be auto-allowed on arrival (the emit path
	// parks then immediately resolves it before returning).
	p.processNativeLine([]byte(readReq), false, false)

	frame := <-frameCh
	if frame == nil {
		t.Fatal("no control_response frame written for the allowlisted Read auto-allow")
	}
	var got map[string]any
	if err := json.Unmarshal(bytes.TrimRight(frame, "\n"), &got); err != nil {
		t.Fatalf("auto-allow frame not JSON: %v (%q)", err, frame)
	}
	resp, _ := got["response"].(map[string]any)
	if resp == nil || resp["request_id"] != readReqID {
		t.Fatalf("auto-allow request_id = %v, want the Read's %q", resp["request_id"], readReqID)
	}
	inner, _ := resp["response"].(map[string]any)
	if inner == nil || inner["behavior"] != "allow" {
		t.Fatalf("inner response = %#v, want behavior=allow", inner)
	}
	if inner["toolUseID"] != readUseID {
		t.Fatalf("inner.toolUseID = %v, want the Read's %q", inner["toolUseID"], readUseID)
	}

	// The Read must NOT be parked (it was auto-allowed and consumed).
	if p.peekNativePending(readReqID) != nil {
		t.Fatal("allowlisted Read left a pending approval; should have been auto-allowed silently")
	}

	// A non-allowlisted Bash in the same burst is STILL parked for the user.
	p.processNativeLine([]byte(bashReq), false, false)
	bash := p.peekNativePending(bashReqID)
	if bash == nil {
		t.Fatal("non-allowlisted Bash was not parked; the user must still approve it")
	}
	if bash.ToolName != "Bash" {
		t.Fatalf("parked tool = %q, want Bash", bash.ToolName)
	}
	if got := p.nativePendingCount.Load(); got != 1 {
		t.Fatalf("nativePendingCount = %d, want 1 (only Bash parked)", got)
	}
	if !p.hasNativePending() {
		t.Fatal("hasNativePending() false; the parked Bash should keep the pane in choice-prompt")
	}
}

// TestAllowlistRemoveStopsAutoAllow verifies that after removing a tool from the
// allowlist, its can_use_tool is parked again (no longer auto-allowed).
func TestAllowlistRemoveStopsAutoAllow(t *testing.T) {
	t.Parallel()
	p, _ := nativePaneForApproval(t, "pane_allowlist_remove")

	p.allowlistAddTool("Grep")
	tools := p.allowlistRemoveTool("Grep")
	if len(tools) != 0 {
		t.Fatalf("after remove, allowlist = %v, want empty", tools)
	}

	const reqID = "019c298e-3333-eeee-ffff-allowlistgrep01"
	req := `{"type":"control_request","request_id":"` + reqID + `","request":{"subtype":"can_use_tool","tool_name":"Grep","tool_use_id":"toolu_01Grep","input":{"pattern":"x"}}}`
	p.processNativeLine([]byte(req), false, false)

	if p.peekNativePending(reqID) == nil {
		t.Fatal("Grep was auto-allowed after being removed from the allowlist; should be parked")
	}
	if p.nativePendingCount.Load() != 1 {
		t.Fatalf("nativePendingCount = %d, want 1", p.nativePendingCount.Load())
	}
}

// newAllowlistMux registers ONLY the two allowlist routes on a real ServeMux so
// r.PathValue("id") resolves (it is empty on a bare httptest request).
func newAllowlistMux(a *apiServer) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/panes/{id}/native/allowlist", a.paneNativeAllowlistGet)
	mux.HandleFunc("POST /api/panes/{id}/native/allowlist", a.paneNativeAllowlistPost)
	return mux
}

func allowlistGET(t *testing.T, mux *http.ServeMux, id string) []string {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/panes/"+id+"/native/allowlist", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET allowlist status = %d, want 200; body=%q", rec.Code, rec.Body.String())
	}
	var out struct {
		Tools []string `json:"tools"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("GET allowlist body not JSON: %v (%q)", err, rec.Body.String())
	}
	if out.Tools == nil {
		t.Fatalf("GET allowlist tools is null; the wire contract requires an array")
	}
	return out.Tools
}

func allowlistPOST(t *testing.T, mux *http.ServeMux, id, body string) (int, []string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/panes/"+id+"/native/allowlist", strings.NewReader(body))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		return rec.Code, nil
	}
	var out struct {
		Tools []string `json:"tools"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("POST allowlist body not JSON: %v (%q)", err, rec.Body.String())
	}
	return rec.Code, out.Tools
}

// TestAllowlistEndpointRoundTrip exercises the WIRE CONTRACT: empty list →
// add Read → list reflects it → add Grep → remove Read → only Grep left.
func TestAllowlistEndpointRoundTrip(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	store, err := NewPaneStore(dir)
	if err != nil {
		t.Fatalf("NewPaneStore: %v", err)
	}
	mgr := NewManager(dir)
	p := transactionTestPane("pane_allowlist_endpoint")
	p.Kind = "native"
	mgr.panes[p.ID] = p
	a := &apiServer{mgr: mgr, panes: store}
	mux := newAllowlistMux(a)

	// Empty to start — and an array, never null.
	if got := allowlistGET(t, mux, p.ID); len(got) != 0 {
		t.Fatalf("initial allowlist = %v, want empty", got)
	}

	// Add Read.
	if code, tools := allowlistPOST(t, mux, p.ID, `{"tool":"Read","action":"add"}`); code != 200 || len(tools) != 1 || tools[0] != "Read" {
		t.Fatalf("add Read → (%d, %v), want (200, [Read])", code, tools)
	}
	if got := allowlistGET(t, mux, p.ID); len(got) != 1 || got[0] != "Read" {
		t.Fatalf("GET after add Read = %v, want [Read]", got)
	}

	// Add Grep — list is sorted.
	if code, tools := allowlistPOST(t, mux, p.ID, `{"tool":"Grep","action":"add"}`); code != 200 || len(tools) != 2 || tools[0] != "Grep" || tools[1] != "Read" {
		t.Fatalf("add Grep → (%d, %v), want (200, [Grep Read])", code, tools)
	}

	// Remove Read → only Grep remains.
	if code, tools := allowlistPOST(t, mux, p.ID, `{"tool":"Read","action":"remove"}`); code != 200 || len(tools) != 1 || tools[0] != "Grep" {
		t.Fatalf("remove Read → (%d, %v), want (200, [Grep])", code, tools)
	}
	if got := allowlistGET(t, mux, p.ID); len(got) != 1 || got[0] != "Grep" {
		t.Fatalf("GET after remove Read = %v, want [Grep]", got)
	}

	// The change was persisted (POST persists through panes.Put).
	list := store.List()
	if len(list) != 1 || len(list[0].NativeAllowlist) != 1 || list[0].NativeAllowlist[0] != "Grep" {
		t.Fatalf("persisted allowlist = %#v, want [Grep]", list)
	}
}

// TestAllowlistEndpointErrors covers the contract's error responses:
// 400 on bad body / unknown action / missing tool, 404 on unknown pane.
func TestAllowlistEndpointErrors(t *testing.T) {
	t.Parallel()
	mgr := NewManager(t.TempDir())
	p := transactionTestPane("pane_allowlist_errors")
	p.Kind = "native"
	mgr.panes[p.ID] = p
	a := &apiServer{mgr: mgr}
	mux := newAllowlistMux(a)

	// 404: unknown pane (GET).
	req := httptest.NewRequest(http.MethodGet, "/api/panes/nope/native/allowlist", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("GET unknown pane status = %d, want 404", rec.Code)
	}

	// 404: unknown pane (POST).
	if code, _ := allowlistPOST(t, mux, "nope", `{"tool":"Read","action":"add"}`); code != http.StatusNotFound {
		t.Fatalf("POST unknown pane status = %d, want 404", code)
	}

	// 400: malformed JSON.
	if code, _ := allowlistPOST(t, mux, p.ID, `{not json`); code != http.StatusBadRequest {
		t.Fatalf("POST bad json status = %d, want 400", code)
	}

	// 400: missing tool.
	if code, _ := allowlistPOST(t, mux, p.ID, `{"action":"add"}`); code != http.StatusBadRequest {
		t.Fatalf("POST missing tool status = %d, want 400", code)
	}

	// 400: unknown action.
	if code, _ := allowlistPOST(t, mux, p.ID, `{"tool":"Read","action":"toggle"}`); code != http.StatusBadRequest {
		t.Fatalf("POST unknown action status = %d, want 400", code)
	}
}

// TestAllowlistPersistsAcrossSnapshot asserts the allowlist rides the pane
// snapshot → PersistedPane round-trip and restores via setNativeAllowlist.
func TestAllowlistPersistsAcrossSnapshot(t *testing.T) {
	t.Parallel()
	p := transactionTestPane("pane_allowlist_persist")
	p.Kind = "native"
	p.allowlistAddTool("Write")
	p.allowlistAddTool("Read")

	snap := p.Snapshot()
	if len(snap.NativeAllowlist) != 2 || snap.NativeAllowlist[0] != "Read" || snap.NativeAllowlist[1] != "Write" {
		t.Fatalf("snapshot allowlist = %v, want sorted [Read Write]", snap.NativeAllowlist)
	}

	store := &PaneStore{}
	persisted := store.FromPane(p)
	if len(persisted.NativeAllowlist) != 2 || persisted.NativeAllowlist[0] != "Read" || persisted.NativeAllowlist[1] != "Write" {
		t.Fatalf("persisted allowlist = %v, want [Read Write]", persisted.NativeAllowlist)
	}

	// Restore onto a fresh pane: the set comes back intact.
	restored := transactionTestPane("pane_allowlist_restored")
	restored.Kind = "native"
	restored.setNativeAllowlist(persisted.NativeAllowlist)
	if !restored.isToolAllowlisted("Read") || !restored.isToolAllowlisted("Write") {
		t.Fatal("restored pane lost its allowlist after setNativeAllowlist")
	}
	if restored.isToolAllowlisted("Bash") {
		t.Fatal("restored pane has a tool it never opted into")
	}
}

// TestAllowlistCautiousGate: under cautious auto-mode the allowlist opt-in
// stops being absolute — an allowlisted tool whose ARGUMENTS trip the
// deterministic deny needles stays parked for the user, while innocuous args
// still auto-allow. In off mode ("") the opt-in keeps its original absolute
// meaning even for red-flag args. Without this gate, "always allow Bash" +
// cautious mode behaved exactly like "always approve" for every Bash command.
func TestAllowlistCautiousGate(t *testing.T) {
	t.Parallel()
	p, agentEnd := nativePaneForApproval(t, "pane_allowlist_cautious")
	p.allowlistAddTool("Bash")
	// Set the mode directly (SetAutoMode also spins up grid bookkeeping this
	// unit test doesn't need).
	p.mu.Lock()
	p.AutoMode = "cautious"
	p.mu.Unlock()

	// Red-flag args: stays parked despite the opt-in.
	const riskyReqID = "019c298e-4444-aaaa-bbbb-cautiousrisk01"
	risky := `{"type":"control_request","request_id":"` + riskyReqID + `","request":{"subtype":"can_use_tool","tool_name":"Bash","tool_use_id":"toolu_01CautiousRisky","input":{"command":"rm -rf /tmp/x"}}}`
	p.processNativeLine([]byte(risky), false, false)
	if p.peekNativePending(riskyReqID) == nil {
		t.Fatal("cautious mode auto-allowed an allowlisted Bash with red-flag args; must stay parked")
	}

	// Innocuous args: the opt-in still applies (allow frame written, no card).
	frameCh := make(chan []byte, 1)
	go func() {
		_ = agentEnd.SetReadDeadline(time.Now().Add(2 * time.Second))
		_, fr, err := ReadFrame(agentEnd)
		if err != nil {
			frameCh <- nil
			return
		}
		frameCh <- fr
	}()
	const safeReqID = "019c298e-5555-cccc-dddd-cautioussafe01"
	safe := `{"type":"control_request","request_id":"` + safeReqID + `","request":{"subtype":"can_use_tool","tool_name":"Bash","tool_use_id":"toolu_01CautiousSafe","input":{"command":"git status"}}}`
	p.processNativeLine([]byte(safe), false, false)
	if frame := <-frameCh; frame == nil {
		t.Fatal("safe allowlisted Bash was not auto-allowed under cautious mode")
	}
	if p.peekNativePending(safeReqID) != nil {
		t.Fatal("safe allowlisted Bash left a pending card; should have been auto-allowed")
	}

	// Off mode: the opt-in is absolute — red-flag args still auto-allow.
	p2, agentEnd2 := nativePaneForApproval(t, "pane_allowlist_offmode")
	p2.allowlistAddTool("Bash")
	frameCh2 := make(chan []byte, 1)
	go func() {
		_ = agentEnd2.SetReadDeadline(time.Now().Add(2 * time.Second))
		_, fr, err := ReadFrame(agentEnd2)
		if err != nil {
			frameCh2 <- nil
			return
		}
		frameCh2 <- fr
	}()
	const offReqID = "019c298e-6666-eeee-ffff-offmoderisk001"
	offRisky := `{"type":"control_request","request_id":"` + offReqID + `","request":{"subtype":"can_use_tool","tool_name":"Bash","tool_use_id":"toolu_01OffRisky","input":{"command":"rm -rf /tmp/y"}}}`
	p2.processNativeLine([]byte(offRisky), false, false)
	if frame := <-frameCh2; frame == nil {
		t.Fatal("off-mode allowlisted Bash was not auto-allowed; the opt-in must stay absolute outside cautious")
	}
	if p2.peekNativePending(offReqID) != nil {
		t.Fatal("off-mode allowlisted Bash left a pending card; the opt-in must stay absolute")
	}
}

// TestAllowlistPrefixRules covers argument-scoped rules: `Bash(git status)`
// auto-allows exactly the commands it can FULLY see matching the prefix —
// and nothing else. Composition (`git status; rm -rf /`), expansions the
// parser can't resolve, and different subcommands all stay parked. A prefix
// rule also never behaves as a whole-tool opt-in.
func TestAllowlistPrefixRules(t *testing.T) {
	t.Parallel()
	p, agentEnd := nativePaneForApproval(t, "pane_allowlist_prefix")
	p.allowlistAddTool("Bash(git status)")

	if p.isToolAllowlisted("Bash") {
		t.Fatal("a prefix rule must not read as a whole-tool opt-in")
	}

	// Covered: `git status --short` (prefix + extra args) auto-allows.
	frameCh := make(chan []byte, 1)
	go func() {
		_ = agentEnd.SetReadDeadline(time.Now().Add(2 * time.Second))
		_, fr, err := ReadFrame(agentEnd)
		if err != nil {
			frameCh <- nil
			return
		}
		frameCh <- fr
	}()
	const okReqID = "019c298e-7777-aaaa-bbbb-prefixmatch001"
	okReq := `{"type":"control_request","request_id":"` + okReqID + `","request":{"subtype":"can_use_tool","tool_name":"Bash","tool_use_id":"toolu_01PrefixOK","input":{"command":"git status --short"}}}`
	p.processNativeLine([]byte(okReq), false, false)
	if frame := <-frameCh; frame == nil {
		t.Fatal("prefix-covered Bash command was not auto-allowed")
	}
	if p.peekNativePending(okReqID) != nil {
		t.Fatal("prefix-covered command left a pending card; should have been auto-allowed")
	}

	// NOT covered: composition, expansions, other subcommands — all parked.
	for i, cmd := range []string{
		"git status; rm -rf /",   // second command not covered
		"git status $(evil)",     // command substitution — parser can't see it
		"git push origin main",   // different subcommand
		"git status > ~/.bashrc", // file redirect defeats the guarantee
	} {
		reqID := "019c298e-8888-cccc-dddd-prefixmiss00" + string(rune('1'+i))
		req := `{"type":"control_request","request_id":"` + reqID + `","request":{"subtype":"can_use_tool","tool_name":"Bash","tool_use_id":"toolu_01PrefixMiss` + string(rune('1'+i)) + `","input":{"command":` + strconv.Quote(cmd) + `}}}`
		p.processNativeLine([]byte(req), false, false)
		if p.peekNativePending(reqID) == nil {
			t.Fatalf("command %q rode the Bash(git status) rule; must stay parked", cmd)
		}
	}
}

// TestPermissionRequestCarriesWouldAutoAllow proves the emitted
// permission_request tells the client whether the daemon is about to auto-allow
// it — for BOTH rule shapes, and for the cautious veto.
//
// This is the fix for the "I see the full bash/error detail, then it just
// auto-approves" flash: the event fans out BEFORE the post-fanout switch
// resolves it, so without the flag the client draws a card it tears down
// milliseconds later. The client used to guess with a whole-tool name match,
// which missed every argument-scoped rule.
func TestPermissionRequestCarriesWouldAutoAllow(t *testing.T) {
	t.Parallel()
	// Read the flag off the pane's event ring — the same struct that is
	// marshaled to subscribers, so this asserts the actual client contract.
	lastPermReq := func(p *Pane) *NativeEvent {
		p.nativeMu.Lock()
		defer p.nativeMu.Unlock()
		for i := len(p.nativeRing) - 1; i >= 0; i-- {
			if p.nativeRing[i].Type == EvPermissionRequest {
				ev := p.nativeRing[i]
				return &ev
			}
		}
		return nil
	}

	// The agent end is a net.Pipe (UNBUFFERED): an auto-allow's control_response
	// blocks forever unless somebody reads it. Keep draining for the whole
	// subtest — the covered case writes one, the parked cases write none.
	drain := func(t *testing.T, agentEnd net.Conn) {
		done := make(chan struct{})
		go func() {
			defer close(done)
			for {
				_ = agentEnd.SetReadDeadline(time.Now().Add(2 * time.Second))
				if _, _, err := ReadFrame(agentEnd); err != nil {
					return
				}
			}
		}()
		t.Cleanup(func() {
			_ = agentEnd.SetReadDeadline(time.Now())
			<-done
		})
	}

	t.Run("argument-scoped rule tags the covered command", func(t *testing.T) {
		p, agentEnd := nativePaneForApproval(t, "pane_wouldauto_prefix")
		drain(t, agentEnd)
		p.allowlistAddTool("Bash(git status)")

		req := `{"type":"control_request","request_id":"019c298e-1111-aaaa-bbbb-wouldauto0001","request":{"subtype":"can_use_tool","tool_name":"Bash","tool_use_id":"toolu_01WouldAutoOK","input":{"command":"git status --short"}}}`
		p.processNativeLine([]byte(req), false, false)

		ev := lastPermReq(p)
		if ev == nil {
			t.Fatal("no permission_request reached the ring")
		}
		// The regression: a whole-tool match on "Bash" is false here, which is
		// exactly why the client's old string compare flashed a card.
		if p.isToolAllowlisted("Bash") {
			t.Fatal("precondition: a prefix rule must not read as a whole-tool opt-in")
		}
		if !ev.WouldAutoAllow {
			t.Error("WouldAutoAllow = false for a command covered by Bash(git status); the client will flash a card")
		}
	})

	t.Run("uncovered command is not tagged", func(t *testing.T) {
		p, agentEnd := nativePaneForApproval(t, "pane_wouldauto_miss")
		drain(t, agentEnd)
		p.allowlistAddTool("Bash(git status)")

		req := `{"type":"control_request","request_id":"019c298e-2222-aaaa-bbbb-wouldauto0002","request":{"subtype":"can_use_tool","tool_name":"Bash","tool_use_id":"toolu_01WouldAutoMiss","input":{"command":"git push origin main"}}}`
		p.processNativeLine([]byte(req), false, false)

		ev := lastPermReq(p)
		if ev == nil {
			t.Fatal("no permission_request reached the ring")
		}
		// Must stay false: the user has to SEE and answer this one. A true here
		// would hide the card for a request nobody approved — the agent would
		// hang on a question that never renders.
		if ev.WouldAutoAllow {
			t.Error("WouldAutoAllow = true for an uncovered command; its card would be hidden while it stays parked")
		}
		if p.peekNativePending("019c298e-2222-aaaa-bbbb-wouldauto0002") == nil {
			t.Error("precondition: the uncovered command should still be parked")
		}
	})

	t.Run("a backlog replay is never tagged", func(t *testing.T) {
		// The fail-DANGEROUS direction, and the reason the stamp carries the
		// backlog gate at all. A backlog request was already answered by the
		// previous daemon, so this pass must not write a second control_response —
		// and must not tell the client "don't bother drawing this" either, because
		// if the request IS still genuinely open the user is the only one who can
		// answer it. Tagging it would hide the card while nothing resolves it.
		p, agentEnd := nativePaneForApproval(t, "pane_wouldauto_backlog")
		drain(t, agentEnd)
		p.allowlistAddTool("Bash") // would auto-allow if this were live

		req := `{"type":"control_request","request_id":"019c298e-4444-aaaa-bbbb-wouldauto0004","request":{"subtype":"can_use_tool","tool_name":"Bash","tool_use_id":"toolu_01WouldAutoBacklog","input":{"command":"git status"}}}`
		p.processNativeLine([]byte(req), false, true /* fromBacklog */)

		ev := lastPermReq(p)
		if ev == nil {
			t.Fatal("no permission_request reached the ring")
		}
		if ev.WouldAutoAllow {
			t.Error("WouldAutoAllow = true on a BACKLOG replay; its card would be hidden while this pass deliberately never resolves it")
		}
	})

	t.Run("cautious veto is reflected", func(t *testing.T) {
		p, agentEnd := nativePaneForApproval(t, "pane_wouldauto_cautious")
		drain(t, agentEnd)
		p.allowlistAddTool("Bash")
		p.mu.Lock()
		p.AutoMode = "cautious"
		p.mu.Unlock()

		// A red-flag command: whole-tool allowlisted, but cautious mode withholds
		// the auto-allow, so it stays parked and MUST render.
		req := `{"type":"control_request","request_id":"019c298e-3333-aaaa-bbbb-wouldauto0003","request":{"subtype":"can_use_tool","tool_name":"Bash","tool_use_id":"toolu_01WouldAutoCaut","input":{"command":"rm -rf /"}}}`
		p.processNativeLine([]byte(req), false, false)

		ev := lastPermReq(p)
		if ev == nil {
			t.Fatal("no permission_request reached the ring")
		}
		if ev.WouldAutoAllow {
			t.Error("WouldAutoAllow = true for a request cautious mode withheld; the card would be hidden while parked")
		}
	})
}

// TestAllowlistPrefixEndpointValidation: malformed prefix rules are rejected on
// add (400) but any string can still be removed.
func TestAllowlistPrefixEndpointValidation(t *testing.T) {
	t.Parallel()
	mgr := NewManager(t.TempDir())
	p := transactionTestPane("pane_allowlist_prefix_api")
	p.Kind = "native"
	mgr.panes[p.ID] = p
	a := &apiServer{mgr: mgr}
	mux := newAllowlistMux(a)

	// Well-formed rule adds fine.
	if code, tools := allowlistPOST(t, mux, p.ID, `{"tool":"Bash(git status)","action":"add"}`); code != 200 || len(tools) != 1 || tools[0] != "Bash(git status)" {
		t.Fatalf("add Bash(git status) → (%d, %v), want (200, [Bash(git status)])", code, tools)
	}
	// Only Bash supports prefixes; empty prefixes are meaningless.
	for _, bad := range []string{"Read(x)", "Bash()", "Bash(", "Bash(   )"} {
		if code, _ := allowlistPOST(t, mux, p.ID, `{"tool":`+strconv.Quote(bad)+`,"action":"add"}`); code != http.StatusBadRequest {
			t.Fatalf("add %q status = %d, want 400", bad, code)
		}
	}
	// Remove stays permissive (legacy junk must always be deletable).
	if code, tools := allowlistPOST(t, mux, p.ID, `{"tool":"Bash(git status)","action":"remove"}`); code != 200 || len(tools) != 0 {
		t.Fatalf("remove → (%d, %v), want (200, [])", code, tools)
	}
}

// TestAllowlistClearedOnSessionRotation verifies the reset semantics: the
// allowlist is KEPT when the pane learns its FIRST session id (restore/resume,
// prior=="") and CLEARED only when the id rotates to a genuinely new one.
func TestAllowlistClearedOnSessionRotation(t *testing.T) {
	t.Parallel()
	p := transactionTestPane("pane_allowlist_session")
	p.Kind = "native"
	p.allowlistAddTool("Read")

	// First session id learned (prior==""): the allowlist must survive — this
	// is the restore/resume / initial-init case, same conversation.
	p.persistNativeSessionID("sess-aaaa-1111")
	if !p.isToolAllowlisted("Read") {
		t.Fatal("allowlist was cleared on the FIRST session id; it should be kept (restore/resume)")
	}

	// Re-asserting the SAME id is a no-op (no clear).
	p.persistNativeSessionID("sess-aaaa-1111")
	if !p.isToolAllowlisted("Read") {
		t.Fatal("allowlist cleared on a redundant same-session init; should be kept")
	}

	// Session ROTATES to a genuinely new id (prior!="") → brand-new
	// conversation → the allowlist must be cleared.
	p.persistNativeSessionID("sess-bbbb-2222")
	if p.isToolAllowlisted("Read") {
		t.Fatal("allowlist survived a session rotation; a new conversation must start clean")
	}
}

// The second consumer of that same edge: the session-start digest. It must fire
// on exactly the rotation branch, so a native pane whose Claude hooks are
// unregistered still gets its digest, and a backlog replay of the id the pane
// already holds cannot fire one.
func TestNativeSessionRotationFiresDigestCallback(t *testing.T) {
	t.Parallel()
	p := transactionTestPane("pane_native_rotation")
	p.Kind = "native"

	// Nil-safe before anything is wired (every test pane, and any spawn path
	// that doesn't wire the Manager callback).
	p.persistNativeSessionID("sess-unwired")

	var got [][2]string
	p.onClaudeSessionRotated = func(paneID, sessionID string) {
		got = append(got, [2]string{paneID, sessionID})
	}

	// prior == "" is the first id a FRESH pane learns — a restore/resume of the
	// same conversation, not a new one.
	fresh := transactionTestPane("pane_native_fresh")
	fresh.Kind = "native"
	fresh.onClaudeSessionRotated = func(paneID, sessionID string) { got = append(got, [2]string{paneID, sessionID}) }
	fresh.persistNativeSessionID("sess-aaaa-1111")
	if len(got) != 0 {
		t.Fatalf("callback fired on the FIRST session id: %v", got)
	}

	// Re-observing the id the pane already holds (a daemon-restart backlog
	// replay) changes nothing.
	p.persistNativeSessionID("sess-unwired")
	if len(got) != 0 {
		t.Fatalf("callback fired on a same-id re-observation: %v", got)
	}

	// A genuine rotation fires it once, with the pane's own id.
	p.persistNativeSessionID("sess-bbbb-2222")
	want := [][2]string{{"pane_native_rotation", "sess-bbbb-2222"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("callback calls = %v, want %v", got, want)
	}
}
