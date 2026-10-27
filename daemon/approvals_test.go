package main

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"
)

// approvals_test.go covers the parallel-tool-call safety property of the native
// auto-approval path: ARIA's allow/deny decision, made about ONE specific
// tool+args, must be applied to THAT approval — never an arbitrary pending one.
//
// The bug it guards against: nativeAutoResolve used to call
// resolveApproval("") with an empty RequestID, and takeNativePending("") then
// grabbed a map-iteration-arbitrary entry. With Read/Grep/Bash pending in
// parallel, ARIA could approve a safe Read but the arbitrary pick could be a
// dangerous Bash → the wrong tool gets allowed. Resolution is now keyed by the
// concrete RequestID end to end, and an empty reqID is a refused no-op.

// TestNativeAutoResolveTargetsCorrectApproval parks TWO pending approvals (a
// benign Read and a dangerous Bash), drives the ARIA auto-resolve path for the
// READ's RequestID, and asserts ONLY the Read was resolved (the control_response
// allow frame carries the Read's request_id / tool_use_id) and the Bash is
// still pending afterward.
func TestNativeAutoResolveTargetsCorrectApproval(t *testing.T) {
	t.Parallel()
	p, agentEnd := nativePaneForApproval(t, "pane_native_autoresolve_target")

	// Two can_use_tool control_requests arriving "at once" (parallel tool
	// calls). The Read is benign; the Bash is the dangerous one that must NOT
	// be auto-allowed by a decision made about the Read.
	const (
		readReqID = "019c298e-1111-2222-3333-readreadread01"
		readUseID = "toolu_01ReadExampleParallelApproval"
		bashReqID = "019c298e-4444-5555-6666-bashbashbash01"
		bashUseID = "toolu_01BashExampleParallelApproval"
	)
	readReq := `{"type":"control_request","request_id":"` + readReqID + `","request":{"subtype":"can_use_tool","tool_name":"Read","tool_use_id":"` + readUseID + `","input":{"file_path":"/tmp/safe.txt"}}}`
	bashReq := `{"type":"control_request","request_id":"` + bashReqID + `","request":{"subtype":"can_use_tool","tool_name":"Bash","tool_use_id":"` + bashUseID + `","input":{"command":"rm -rf /","description":"dangerous"}}}`

	// Drive both through the real parser line path (what the tee goroutine calls).
	p.processNativeLine([]byte(readReq), false, false)
	p.processNativeLine([]byte(bashReq), false, false)

	// Both approvals are parked simultaneously.
	if p.peekNativePending(readReqID) == nil {
		t.Fatal("Read approval not parked")
	}
	if p.peekNativePending(bashReqID) == nil {
		t.Fatal("Bash approval not parked")
	}
	if got := p.nativePendingCount.Load(); got != 2 {
		t.Fatalf("nativePendingCount = %d, want 2", got)
	}

	// Drive the ARIA auto-resolve path keyed by the READ's RequestID. A bare
	// zero-value Aria is enough: it isn't paused, the fire-budget caps start
	// empty, uiBus is nil (publishStatusChanged no-ops) and autoFireLogPath is
	// empty (saveAutoFireTimesLocked no-ops).
	a := &Aria{}

	// net.Pipe is synchronous, so read the frame off the agent end concurrently
	// while nativeAutoResolve writes it.
	type frameResult struct {
		frame []byte
		err   error
	}
	frameCh := make(chan frameResult, 1)
	go func() {
		_ = agentEnd.SetReadDeadline(time.Now().Add(2 * time.Second))
		_, fr, err := ReadFrame(agentEnd)
		frameCh <- frameResult{fr, err}
	}()

	confirmID := "auto_native_" + readReqID
	ok := a.nativeAutoResolve(p, readReqID, confirmID, false /* no registry */)
	if !ok {
		t.Fatal("nativeAutoResolve(read) returned false; want true")
	}

	fres := <-frameCh
	if fres.err != nil {
		t.Fatalf("read control_response frame: %v", fres.err)
	}

	// The allow frame must answer the READ — its request_id and (camelCase)
	// toolUseID — NOT the Bash. This is the core safety assertion.
	var got map[string]any
	if err := json.Unmarshal(bytes.TrimRight(fres.frame, "\n"), &got); err != nil {
		t.Fatalf("encoded frame is not JSON: %v (%q)", err, fres.frame)
	}
	resp, _ := got["response"].(map[string]any)
	if resp == nil || resp["request_id"] != readReqID {
		t.Fatalf("resolved frame request_id = %v, want the Read's %q (wrong approval resolved!)", resp["request_id"], readReqID)
	}
	inner, _ := resp["response"].(map[string]any)
	if inner == nil || inner["behavior"] != "allow" {
		t.Fatalf("inner response = %#v, want behavior=allow", inner)
	}
	if inner["toolUseID"] != readUseID {
		t.Fatalf("inner.toolUseID = %v, want the Read's %q (wrong tool allowed!)", inner["toolUseID"], readUseID)
	}
	// Belt-and-suspenders: the Bash identifiers must NOT appear anywhere in the
	// frame — the dangerous tool was never answered.
	if bytes.Contains(fres.frame, []byte(bashReqID)) || bytes.Contains(fres.frame, []byte(bashUseID)) {
		t.Fatalf("resolved frame %q references the Bash approval; the wrong tool was resolved", fres.frame)
	}

	// The Read is now consumed; the Bash MUST still be pending (untouched).
	if p.peekNativePending(readReqID) != nil {
		t.Fatal("Read approval still pending after resolve; should be consumed")
	}
	bashStill := p.peekNativePending(bashReqID)
	if bashStill == nil {
		t.Fatal("Bash approval was consumed by the Read's auto-resolve — the safety bug")
	}
	if bashStill.ToolName != "Bash" {
		t.Fatalf("remaining pending ToolName = %q, want Bash", bashStill.ToolName)
	}
	if got := p.nativePendingCount.Load(); got != 1 {
		t.Fatalf("nativePendingCount after resolve = %d, want 1 (only Bash left)", got)
	}
	if !p.hasNativePending() {
		t.Fatal("hasNativePending() false; Bash should keep the pane in choice-prompt")
	}

	// The native ring must carry an immediate, request-correlated resolution
	// edge. This is what lets Focus remove the Read approval card before the
	// tool's eventual result without accidentally dismissing the parallel Bash.
	p.nativeMu.Lock()
	var resolution *NativeEvent
	for i := range p.nativeRing {
		ev := &p.nativeRing[i]
		if ev.Type == EvPermissionResolved {
			copy := *ev
			resolution = &copy
		}
	}
	p.nativeMu.Unlock()
	if resolution == nil {
		t.Fatal("no permission_resolved event emitted after successful resolve")
	}
	if resolution.RequestID != readReqID || resolution.CallID != readUseID || resolution.ToolName != "Read" {
		t.Fatalf("permission_resolved = %+v, want the resolved Read only", resolution)
	}
}

// TestNativeAutoResolveRefusesEmptyReqID asserts the auto-resolve path refuses
// an empty RequestID outright: no budget slot is reserved, nothing is resolved,
// and every pending approval is left untouched. This closes the recurrence
// vector for the parallel-tool-call safety hole.
func TestNativeAutoResolveRefusesEmptyReqID(t *testing.T) {
	t.Parallel()
	p, _ := nativePaneForApproval(t, "pane_native_autoresolve_empty")

	const reqID = "019c298e-7777-8888-9999-onlyoneparked01"
	req := `{"type":"control_request","request_id":"` + reqID + `","request":{"subtype":"can_use_tool","tool_name":"Read","tool_use_id":"toolu_01OnlyOne","input":{"file_path":"/tmp/x"}}}`
	p.processNativeLine([]byte(req), false, false)
	if p.nativePendingCount.Load() != 1 {
		t.Fatalf("setup: nativePendingCount = %d, want 1", p.nativePendingCount.Load())
	}

	a := &Aria{}
	if a.nativeAutoResolve(p, "" /* empty reqID */, "auto_native_empty", false) {
		t.Fatal("nativeAutoResolve with empty reqID returned true; want refused no-op")
	}

	// Nothing resolved: the approval is still pending and no budget was spent.
	if p.peekNativePending(reqID) == nil {
		t.Fatal("approval was resolved despite empty reqID")
	}
	if p.nativePendingCount.Load() != 1 {
		t.Fatalf("nativePendingCount = %d after empty-reqID refusal, want 1 (untouched)", p.nativePendingCount.Load())
	}

	// takeNativePending("") is likewise a refused no-op, not an arbitrary pick.
	if got := p.takeNativePending(""); got != nil {
		t.Fatalf("takeNativePending(\"\") returned %#v; want nil (no arbitrary pick)", got)
	}
	if p.nativePendingCount.Load() != 1 {
		t.Fatalf("takeNativePending(\"\") consumed an entry; nativePendingCount = %d, want 1", p.nativePendingCount.Load())
	}
}

// TestDropResolvedApprovalCards proves the "dead question after respawn" fix: a
// full remount replays the ring, which still holds an already-RESOLVED
// permission_request; historyBackfill must drop that dead card (a click on it
// resolves nothing) while keeping a still-PENDING one (whose buttons work).
func TestDropResolvedApprovalCards(t *testing.T) {
	t.Parallel()
	p, _ := nativePaneForApproval(t, "pane_native_dropresolved")

	// One approval is genuinely still pending; the other ("req-resolved") is not
	// in the pending map — already answered, but its event survives in the ring.
	p.setNativePending(&NativeApproval{RequestID: "req-live", ToolUseID: "toolu_live", ToolName: "Read"})

	events := []NativeEvent{
		{Type: EvText, Text: "before"},
		{Type: EvPermissionRequest, RequestID: "req-resolved", ToolName: "Bash"},
		{Type: EvPermissionRequest, RequestID: "req-live", ToolName: "Read"},
		{Type: EvText, Text: "after"},
	}
	got := dropResolvedApprovalCards(p, events)

	if len(got) != 3 {
		t.Fatalf("len(got) = %d, want 3 (resolved approval dropped, live + 2 text kept)", len(got))
	}
	var keptLive bool
	for _, ev := range got {
		if ev.Type == EvPermissionRequest && ev.RequestID == "req-resolved" {
			t.Fatal("resolved (dead) approval card was NOT dropped")
		}
		if ev.Type == EvPermissionRequest && ev.RequestID == "req-live" {
			keptLive = true
		}
	}
	if !keptLive {
		t.Fatal("live (still-pending) approval card was wrongly dropped")
	}

	// Fast path: no permission_request in the set → returned unchanged.
	plain := []NativeEvent{{Type: EvText, Text: "x"}, {Type: EvToolCall, CallID: "c1"}}
	if out := dropResolvedApprovalCards(p, plain); len(out) != 2 {
		t.Fatalf("no-approval fast path changed length: got %d, want 2", len(out))
	}
}

// TestSubscribeNativeDropsResolvedApproval is the regression for the
// "approval spam on every refresh" bug. dropResolvedApprovalCards already
// guarded the HTTP /native/history replay, but the live-ring WS/SSE backfill
// (SubscribeNative, replayed by handleNativeWS / paneNativeStream on every
// reconnect) returned the raw ring — so a browser refresh re-streamed every
// already-answered permission_request as a fresh, actionable card. This asserts
// SubscribeNative's snapshot now drops a RESOLVED approval while keeping a
// still-PENDING one (so it can still be answered after a refresh).
func TestSubscribeNativeDropsResolvedApproval(t *testing.T) {
	t.Parallel()
	p, agentEnd := nativePaneForApproval(t, "pane_native_subscribe_filter")

	const (
		readReqID = "019c298e-1111-2222-3333-readreadread03"
		readUseID = "toolu_01ReadSubscribeFilterRegression"
		bashReqID = "019c298e-4444-5555-6666-bashbashbash03"
		bashUseID = "toolu_01BashSubscribeFilterRegression"
	)
	readReq := `{"type":"control_request","request_id":"` + readReqID + `","request":{"subtype":"can_use_tool","tool_name":"Read","tool_use_id":"` + readUseID + `","input":{"file_path":"/tmp/safe.txt"}}}`
	bashReq := `{"type":"control_request","request_id":"` + bashReqID + `","request":{"subtype":"can_use_tool","tool_name":"Bash","tool_use_id":"` + bashUseID + `","input":{"command":"ls -la","description":"list files"}}}`
	p.processNativeLine([]byte(readReq), false, false)
	p.processNativeLine([]byte(bashReq), false, false)

	countSnap := func() (total int, hasRead, hasBash bool) {
		_, snap, _, cancel, ok := p.SubscribeNative()
		if !ok {
			t.Fatal("SubscribeNative ok=false on a native pane")
		}
		defer cancel()
		for _, ev := range snap {
			if ev.Type != EvPermissionRequest {
				continue
			}
			total++
			switch ev.RequestID {
			case readReqID:
				hasRead = true
			case bashReqID:
				hasBash = true
			}
		}
		return
	}

	// Before any resolve, BOTH are live-pending → both must replay (the
	// still-pending case must keep working across a refresh).
	if total, hasRead, hasBash := countSnap(); total != 2 || !hasRead || !hasBash {
		t.Fatalf("pre-resolve SubscribeNative backfill: total=%d read=%v bash=%v, want 2/true/true", total, hasRead, hasBash)
	}

	// Resolve ONLY the Read. net.Pipe is synchronous → drain the response frame.
	resCh := make(chan error, 1)
	go func() {
		_ = agentEnd.SetReadDeadline(time.Now().Add(2 * time.Second))
		_, _, err := ReadFrame(agentEnd)
		resCh <- err
	}()
	if _, err := p.resolveApproval(readReqID, true, ""); err != nil {
		t.Fatalf("resolveApproval(read, allow): %v", err)
	}
	if err := <-resCh; err != nil {
		t.Fatalf("reading allow frame: %v", err)
	}

	// THE FIX: the resolved Read must NOT replay; the still-pending Bash must.
	total, hasRead, hasBash := countSnap()
	if hasRead {
		t.Fatal("resolved Read approval STILL replays from SubscribeNative — the refresh-spam bug")
	}
	if total != 1 || !hasBash {
		t.Fatalf("post-resolve SubscribeNative backfill: total=%d bash=%v, want 1/true (only pending Bash)", total, hasBash)
	}
}
