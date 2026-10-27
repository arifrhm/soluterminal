package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"strings"
	"testing"
	"time"
)

// aria_native_auto_test.go covers the ARIA → native-pane auto-mode wiring:
// the no-LLM "always" fast path (directAutoFireNative), the resolve_permission
// executor gates (toolResolvePermission), and the supporting safety helpers.
//
// The two load-bearing safety properties under test:
//   - AskUserQuestion / ExitPlanMode are NEVER auto-answered.
//   - Native auto-resolution stays inside the shared ARIA fire budget — each
//     fire records exactly once (no double-count).

func nativeCanUseToolLine(reqID, useID, tool, inputJSON string) string {
	return `{"type":"control_request","request_id":"` + reqID +
		`","request":{"subtype":"can_use_tool","tool_name":"` + tool +
		`","tool_use_id":"` + useID + `","input":` + inputJSON + `}}`
}

func testNativeAutoAria(t *testing.T, p *Pane) *Aria {
	t.Helper()
	return &Aria{
		mgr:          testManagerWithPane(p),
		uiBus:        newUIBus(),
		confirms:     map[string]*ariaPendingConfirm{},
		sweepStreaks: map[string]*sweepStreak{},
	}
}

func TestDirectAutoFireNativeApprovesPendingRead(t *testing.T) {
	t.Parallel()
	p, agentEnd := nativePaneForApproval(t, "pane_native_directfire")
	const reqID = "019c2a00-aaaa-bbbb-cccc-readdirectfire1"
	const useID = "toolu_01ReadDirectFire"
	p.processNativeLine([]byte(nativeCanUseToolLine(reqID, useID, "Read", `{"file_path":"/tmp/safe.txt"}`)), false, false)
	if p.peekNativePending(reqID) == nil {
		t.Fatal("Read approval not parked")
	}

	frames := make(chan []byte, 4)
	go func() {
		for {
			_ = agentEnd.SetReadDeadline(time.Now().Add(2 * time.Second))
			_, fr, err := ReadFrame(agentEnd)
			if err != nil {
				return
			}
			frames <- fr
		}
	}()

	a := testNativeAutoAria(t, p)
	a.directAutoFireNative(context.Background(), p.ID, "claude")

	select {
	case fr := <-frames:
		var got map[string]any
		if err := json.Unmarshal(bytes.TrimRight(fr, "\n"), &got); err != nil {
			t.Fatalf("frame not JSON: %v (%q)", err, fr)
		}
		resp, _ := got["response"].(map[string]any)
		if resp == nil || resp["request_id"] != reqID {
			t.Fatalf("frame request_id = %v, want %q", resp["request_id"], reqID)
		}
		inner, _ := resp["response"].(map[string]any)
		if inner == nil || inner["behavior"] != "allow" {
			t.Fatalf("inner = %#v, want behavior=allow", inner)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no allow frame written by directAutoFireNative")
	}

	if p.peekNativePending(reqID) != nil {
		t.Fatal("Read still pending after directAutoFireNative")
	}
	// Exactly one fire recorded — the budget must not be double-counted.
	a.mu.Lock()
	nFires := len(a.autoFireTimes)
	nReserved := len(a.autoFireReserved)
	a.mu.Unlock()
	if nFires != 1 {
		t.Fatalf("autoFireTimes = %d, want exactly 1 (no double-record)", nFires)
	}
	if nReserved != 0 {
		t.Fatalf("autoFireReserved = %d, want 0 (reservation consumed by the fire)", nReserved)
	}
	// The global ARIA event keeps the same correlation for the coarse pane-state
	// UI and diagnostics. The native permission_resolved ring event is the card's
	// durable source of truth, but this payload must not regress to id-only.
	a.uiBus.logMu.Lock()
	var resolvedPayload map[string]any
	for _, event := range a.uiBus.log {
		if event.Type != "aria-auto-confirm-resolved" {
			continue
		}
		var payload map[string]any
		if json.Unmarshal([]byte(event.Text), &payload) == nil {
			resolvedPayload = payload
		}
	}
	a.uiBus.logMu.Unlock()
	if resolvedPayload == nil || resolvedPayload["request_id"] != reqID {
		t.Fatalf("aria-auto-confirm-resolved request_id = %v, want %q", resolvedPayload["request_id"], reqID)
	}
}

func TestDirectAutoFireNativeApprovesCodexStructuredRequest(t *testing.T) {
	t.Parallel()
	paneEnd, agentEnd := net.Pipe()
	done := make(chan struct{})
	p := &Pane{
		ID: "pane_native_codex_directfire", Cwd: "/work", Cmd: nativeCodexArgv(),
		Title: "codex", Status: PaneRunning, CreatedAt: time.Now().UTC(), Cols: 80, Rows: 24,
		Kind: "native", Agent: agentCodex, CodexThreadID: "thr-auto",
		agentConn: paneEnd, done: done, buffer: newTestRing(), subs: map[int64]*subEntry{},
	}
	p.ensureNative()
	driver, ok := p.nativeAdapter.(*CodexAdapter)
	if !ok {
		t.Fatalf("adapter=%T, want *CodexAdapter", p.nativeAdapter)
	}
	driver.RestoreThread("thr-auto", "/work", "gpt-5.6-sol")
	t.Cleanup(func() {
		close(done)
		p.shutdownNative()
		_ = paneEnd.Close()
		_ = agentEnd.Close()
	})

	p.processNativeLine([]byte(`{"method":"turn/started","params":{"threadId":"thr-auto","turn":{"id":"turn-auto","status":"inProgress","items":[]}}}`), false, false)
	p.processNativeLine([]byte(`{"id":"approval-codex","method":"item/fileChange/requestApproval","params":{"threadId":"thr-auto","turnId":"turn-auto","itemId":"patch-auto","startedAtMs":13,"changes":[{"path":"safe.go","kind":"update","diff":"+safe"}]}}`), false, false)
	if ap := p.peekNativePending("approval-codex"); ap == nil || ap.ToolName != "ApplyPatch" {
		t.Fatalf("Codex approval not parked correctly: %+v", ap)
	}

	frames := make(chan []byte, 1)
	go func() {
		_ = agentEnd.SetReadDeadline(time.Now().Add(2 * time.Second))
		_, frame, err := ReadFrame(agentEnd)
		if err == nil {
			frames <- frame
		}
	}()

	a := testNativeAutoAria(t, p)
	a.directAutoFireNative(context.Background(), p.ID, "codex")
	select {
	case frame := <-frames:
		var got map[string]any
		if err := json.Unmarshal(bytes.TrimRight(frame, "\n"), &got); err != nil {
			t.Fatalf("Codex approval frame not JSON: %v (%q)", err, frame)
		}
		result, _ := got["result"].(map[string]any)
		if got["id"] != "approval-codex" || result == nil || result["decision"] != "accept" {
			t.Fatalf("Codex approval response=%#v", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no Codex accept frame written by directAutoFireNative")
	}
	if p.peekNativePending("approval-codex") != nil {
		t.Fatal("Codex approval remained pending after auto-approve")
	}
}

func TestDirectAutoFireNativeResolvesAllParallel(t *testing.T) {
	t.Parallel()
	p, agentEnd := nativePaneForApproval(t, "pane_native_parallel")
	const readReqID = "019c2a01-aaaa-read-parallel0001"
	const grepReqID = "019c2a01-bbbb-grep-parallel0001"
	p.processNativeLine([]byte(nativeCanUseToolLine(readReqID, "toolu_readP", "Read", `{"file_path":"/tmp/a.txt"}`)), false, false)
	p.processNativeLine([]byte(nativeCanUseToolLine(grepReqID, "toolu_grepP", "Grep", `{"pattern":"foo"}`)), false, false)
	if p.nativePendingCount.Load() != 2 {
		t.Fatalf("pending = %d, want 2", p.nativePendingCount.Load())
	}

	frames := make(chan []byte, 8)
	go func() {
		for {
			_ = agentEnd.SetReadDeadline(time.Now().Add(2 * time.Second))
			_, fr, err := ReadFrame(agentEnd)
			if err != nil {
				return
			}
			frames <- fr
		}
	}()

	a := testNativeAutoAria(t, p)
	a.directAutoFireNative(context.Background(), p.ID, "claude")

	// Both approvals should be resolved.
	if p.peekNativePending(readReqID) != nil {
		t.Fatal("Read still pending")
	}
	if p.peekNativePending(grepReqID) != nil {
		t.Fatal("Grep still pending")
	}
	if p.hasNativePending() {
		t.Fatal("pane still has pending approvals after resolving both")
	}
	a.mu.Lock()
	nFires := len(a.autoFireTimes)
	a.mu.Unlock()
	if nFires != 2 {
		t.Fatalf("autoFireTimes = %d, want 2 (one per parallel approval)", nFires)
	}
}

func TestDirectAutoFireNativeSkipsAskUserQuestion(t *testing.T) {
	t.Parallel()
	p, agentEnd := nativePaneForApproval(t, "pane_native_auq")
	const reqID = "019c2a02-auq-leave-for-human01"
	p.processNativeLine([]byte(nativeCanUseToolLine(reqID, "toolu_auq", "AskUserQuestion", `{"questions":[]}`)), false, false)
	if p.peekNativePending(reqID) == nil {
		t.Fatal("AskUserQuestion not parked")
	}

	wrote := make(chan struct{}, 1)
	go func() {
		_ = agentEnd.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
		if _, _, err := ReadFrame(agentEnd); err == nil {
			wrote <- struct{}{}
		}
	}()

	a := testNativeAutoAria(t, p)
	a.directAutoFireNative(context.Background(), p.ID, "claude")

	select {
	case <-wrote:
		t.Fatal("AskUserQuestion was auto-resolved — it must be left for the human")
	case <-time.After(600 * time.Millisecond):
	}
	if p.peekNativePending(reqID) == nil {
		t.Fatal("AskUserQuestion approval was consumed; it must stay pending")
	}
	a.mu.Lock()
	nFires := len(a.autoFireTimes)
	a.mu.Unlock()
	if nFires != 0 {
		t.Fatalf("autoFireTimes = %d, want 0 (nothing should fire for AskUserQuestion)", nFires)
	}
}

func TestDirectAutoFireNativeRespectsPauseCap(t *testing.T) {
	t.Parallel()
	p, _ := nativePaneForApproval(t, "pane_native_paused")
	const reqID = "019c2a03-paused-no-fire00001"
	p.processNativeLine([]byte(nativeCanUseToolLine(reqID, "toolu_paused", "Read", `{"file_path":"/tmp/a.txt"}`)), false, false)

	a := testNativeAutoAria(t, p)
	a.paused = true // ARIA paused → autoAllowed returns false
	a.directAutoFireNative(context.Background(), p.ID, "claude")

	if p.peekNativePending(reqID) == nil {
		t.Fatal("approval resolved while ARIA was paused; must be left pending")
	}
	a.mu.Lock()
	nFires := len(a.autoFireTimes)
	a.mu.Unlock()
	if nFires != 0 {
		t.Fatalf("autoFireTimes = %d, want 0 while paused", nFires)
	}
}

// TestToolResolvePermissionApproves drives the LLM/registry path: the executor
// parks on awaitConfirm, signalConfirm releases it, and the allow frame is
// written for the bound reqID.
func TestToolResolvePermissionApproves(t *testing.T) {
	t.Parallel()
	p, agentEnd := nativePaneForApproval(t, "pane_native_resolve")
	const reqID = "019c2a04-resolve-approve000001"
	const useID = "toolu_resolveApprove"
	p.processNativeLine([]byte(nativeCanUseToolLine(reqID, useID, "Read", `{"file_path":"/tmp/safe.txt"}`)), false, false)

	frames := make(chan []byte, 4)
	go func() {
		for {
			_ = agentEnd.SetReadDeadline(time.Now().Add(2 * time.Second))
			_, fr, err := ReadFrame(agentEnd)
			if err != nil {
				return
			}
			frames <- fr
		}
	}()

	a := testNativeAutoAria(t, p)
	confirmIDCh := make(chan string, 1)
	resolvedCh := make(chan map[string]any, 1)
	emit := func(evt map[string]any) {
		switch et, _ := evt["type"].(string); et {
		case "needs_confirm":
			if id, _ := evt["id"].(string); id != "" {
				select {
				case confirmIDCh <- id:
				default:
				}
			}
		case "confirm_resolved":
			select {
			case resolvedCh <- evt:
			default:
			}
		}
	}
	resultCh := make(chan string, 1)
	go func() {
		resultCh <- a.toolResolvePermission(context.Background(), "tu_1", p.ID, reqID, false, emit)
	}()

	var confirmID string
	select {
	case confirmID = <-confirmIDCh:
	case <-time.After(2 * time.Second):
		t.Fatal("no needs_confirm emitted by toolResolvePermission")
	}
	if !a.signalConfirm(confirmID, true, false) {
		t.Fatal("signalConfirm returned false")
	}

	select {
	case fr := <-frames:
		var got map[string]any
		_ = json.Unmarshal(bytes.TrimRight(fr, "\n"), &got)
		resp, _ := got["response"].(map[string]any)
		if resp == nil || resp["request_id"] != reqID {
			t.Fatalf("frame request_id = %v, want %q", resp["request_id"], reqID)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no allow frame written")
	}

	select {
	case res := <-resultCh:
		if !strings.Contains(res, "approved") {
			t.Fatalf("toolResolvePermission result = %q, want approved", res)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("toolResolvePermission did not return")
	}
	if p.peekNativePending(reqID) != nil {
		t.Fatal("approval still pending after approve")
	}
	select {
	case evt := <-resolvedCh:
		if evt["request_id"] != reqID {
			t.Fatalf("confirm_resolved request_id = %v, want %q", evt["request_id"], reqID)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no request-correlated confirm_resolved emitted")
	}
}

func TestToolResolvePermissionRejectsAskUserQuestion(t *testing.T) {
	t.Parallel()
	p, _ := nativePaneForApproval(t, "pane_native_resolve_auq")
	const reqID = "019c2a05-resolve-auq000000001"
	p.processNativeLine([]byte(nativeCanUseToolLine(reqID, "toolu_resolveAUQ", "AskUserQuestion", `{"questions":[]}`)), false, false)

	a := testNativeAutoAria(t, p)
	// The gate returns before awaitConfirm, so this does not block.
	got := a.toolResolvePermission(context.Background(), "tu_1", p.ID, reqID, false, func(map[string]any) {})
	if !strings.Contains(got, "question for the user") {
		t.Fatalf("result = %q, want AskUserQuestion refusal", got)
	}
	if p.peekNativePending(reqID) == nil {
		t.Fatal("AskUserQuestion was consumed; must stay pending")
	}
}

func TestToolResolvePermissionCautiousDeny(t *testing.T) {
	t.Parallel()
	p, _ := nativePaneForApproval(t, "pane_native_resolve_cautious")
	const reqID = "019c2a06-resolve-cautious00001"
	p.processNativeLine([]byte(nativeCanUseToolLine(reqID, "toolu_resolveCautious", "Bash", `{"command":"rm -rf /tmp/x"}`)), false, false)

	a := testNativeAutoAria(t, p)
	got := a.toolResolvePermission(context.Background(), "tu_1", p.ID, reqID, true /* cautious */, func(map[string]any) {})
	if !strings.Contains(got, "denied by daemon safety gate") {
		t.Fatalf("result = %q, want cautious safety-gate denial", got)
	}
	if p.peekNativePending(reqID) == nil {
		t.Fatal("dangerous Bash was consumed under cautious mode; must stay pending")
	}
}

func TestNonTeamCautiousNativeLeavesPermissionForUser(t *testing.T) {
	t.Setenv("SOLU_ARIA_NATIVE_AUTO", "1")
	p, _ := nativePaneForApproval(t, "pane_native_nonteam_cautious")
	const reqID = "019c2a06-nonteam-cautious0001"
	p.AutoMode = "cautious"
	p.processNativeLine([]byte(nativeCanUseToolLine(reqID, "toolu_nonteamCautious", "Read", `{"file_path":"/tmp/safe.txt"}`)), false, false)
	a := testNativeAutoAria(t, p)
	a.evalLog = newAriaEvalLog()
	a.teamFeatures = func() bool { return false }
	a.evaluateObservation(context.Background(), p.ID, p.Title, "")
	if p.peekNativePending(reqID) == nil {
		t.Fatal("non-team cautious native approval was consumed; it must stay pending")
	}
	outcomes := a.recentEvalOutcomes(p.ID)
	if len(outcomes) == 0 || !strings.Contains(outcomes[0].Reason, "can't check this kind of pane") {
		t.Fatalf("outcomes = %+v, want native fallback reason", outcomes)
	}
}

// TestExecuteToolAllowsResolvePermission verifies the autonomous guard was
// relaxed to admit resolve_permission (it used to reject everything but
// send_input). A non-pending reqID returns quickly without blocking on confirm.
func TestExecuteToolAllowsResolvePermission(t *testing.T) {
	t.Parallel()
	p, _ := nativePaneForApproval(t, "pane_native_exectool")
	a := testNativeAutoAria(t, p)
	got := a.executeTool(context.Background(), "tu_1", "resolve_permission",
		json.RawMessage(`{}`),
		ariaQueryReq{Autonomous: true, AutonomousNative: true, AutonomousPaneID: p.ID, AutonomousRequestID: "no-such-req"},
		func(map[string]any) {})
	if strings.Contains(got, "may only call") {
		t.Fatalf("resolve_permission was rejected by the autonomous guard: %q", got)
	}
	if !strings.Contains(got, "no longer pending") {
		t.Fatalf("result = %q, want a no-pending skip for the unknown reqID", got)
	}
}

func TestIsNonAutoApprovableNativeTool(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		tool string
		want bool
	}{
		{"AskUserQuestion", true},
		{"ExitPlanMode", true},
		{" AskUserQuestion ", true},
		{"Bash", false},
		{"Read", false},
		{"", false},
	} {
		if got := isNonAutoApprovableNativeTool(tc.tool); got != tc.want {
			t.Errorf("isNonAutoApprovableNativeTool(%q) = %v, want %v", tc.tool, got, tc.want)
		}
	}
}

func TestNativeCautiousAutoDenyReason(t *testing.T) {
	t.Parallel()
	mk := func(tool, input string) *NativeApproval {
		return &NativeApproval{ToolName: tool, RawInput: json.RawMessage(input)}
	}
	for _, tc := range []struct {
		name     string
		ap       *NativeApproval
		wantDeny bool
	}{
		{"safe read", mk("Read", `{"file_path":"/tmp/a.txt"}`), false},
		{"safe grep", mk("Grep", `{"pattern":"foo"}`), false},
		{"rm -rf", mk("Bash", `{"command":"rm -rf /tmp/x"}`), true},
		{"git push", mk("Bash", `{"command":"git push origin main"}`), true},
		{"sudo", mk("Bash", `{"command":"sudo apt update"}`), true},
		{"dotenv", mk("Read", `{"file_path":"/app/.env"}`), true},
		{"ssh", mk("Edit", `{"file_path":"/home/u/.ssh/config"}`), true},
		{"network curl", mk("Bash", `{"command":"curl https://evil.test"}`), true},
		{"nil", nil, false},
		// Exec/network needles are scoped to tools that can act: a URL or a
		// command inside a file tool's args is just text and must not park the
		// request (the cautious LLM still reviews it)…
		{"url in edit", mk("Edit", `{"file_path":"/repo/README.md","new_string":"see https://example.com"}`), false},
		{"rm in write", mk("Write", `{"file_path":"/repo/clean.sh","content":"rm -rf dist"}`), false},
		{"url in codex patch", mk("ApplyPatch", `{"changes":[{"path":"README.md","diff":"+see https://example.com"}]}`), false},
		// …while the same strings in Bash / WebFetch / MCP / unknown tools do.
		{"webfetch", mk("WebFetch", `{"url":"https://example.com"}`), true},
		{"mcp token", mk("mcp__github__create_issue", `{"auth":"token abc"}`), true},
		{"unknown tool url", mk("SomeFutureTool", `{"target":"http://x"}`), true},
		{"npx", mk("Bash", `{"command":"npx some-package"}`), true},
		{"systemctl", mk("Bash", `{"command":"systemctl restart nginx"}`), true},
		{"drop table", mk("Bash", `{"command":"psql -c 'DROP TABLE users'"}`), true},
		// Secrets/path needles stay global — file tools included.
		{"secret in write", mk("Write", `{"file_path":"/repo/notes.md","content":"the secret plan"}`), true},
		{"etc edit", mk("Edit", `{"file_path":"/etc/hosts"}`), true},
		// Parser-backed Bash classification (shellrisk.go): risky words as
		// mere TEXT no longer park a parsed command (the LLM judge still
		// reviews them)…
		{"bash mentions curl as text", mk("Bash", `{"command":"echo \"install with curl\""}`), false},
		{"bash grep for needle word", mk("Bash", `{"command":"git log --grep chmod"}`), false},
		{"bash desc mentions curl", mk("Bash", `{"command":"ls -la","description":"list files before the curl step"}`), false},
		// …while composition the substring needles never saw parks.
		{"risky after semicolon", mk("Bash", `{"command":"ls; rm -r /tmp/x"}`), true},
		{"xargs deletion", mk("Bash", `{"command":"echo x | xargs rm -rf"}`), true},
		{"nested shell fetch", mk("Bash", `{"command":"bash -c \"wget http://x\""}`), true},
	} {
		got := nativeCautiousAutoDenyReason(tc.ap)
		if (got != "") != tc.wantDeny {
			t.Errorf("%s: nativeCautiousAutoDenyReason = %q, wantDeny=%v", tc.name, got, tc.wantDeny)
		}
	}
}

func TestNativeApprovalInputPreview(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		raw  string
		want string
	}{
		{`{"command":"git status"}`, "git status"},
		{`{"file_path":"/tmp/x.go"}`, "/tmp/x.go"},
		{`{"url":"https://example.com"}`, "https://example.com"},
		{`{"weird":42}`, `{"weird":42}`},
	} {
		if got := nativeApprovalInputPreview(json.RawMessage(tc.raw)); got != tc.want {
			t.Errorf("nativeApprovalInputPreview(%s) = %q, want %q", tc.raw, got, tc.want)
		}
	}
}

func TestAriaNativeAutonomousToolsExposesResolvePermission(t *testing.T) {
	t.Parallel()
	tools := ariaNativeAutonomousTools()
	if len(tools) != 1 || tools[0].Name != "resolve_permission" {
		t.Fatalf("ariaNativeAutonomousTools() = %#v, want exactly [resolve_permission]", tools)
	}
}
