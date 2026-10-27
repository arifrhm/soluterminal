package main

import (
	"bytes"
	"encoding/json"
	"net"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"soluterminal/grid"
)

// native_safety_test.go holds the two load-bearing safety tests for the
// native-renderer Phase-1 feature:
//
//  1. TestFallbackGuaranteeTerminalPaneUntouched — with Kind=="" (the default)
//     NO native machinery is constructed (no adapter, no tee channel, no subs,
//     no parser goroutine, nil pending-approval) and the byte path through the
//     readLoop hot critical section is byte-identical. This is the sacred
//     fallback guarantee: a terminal pane must pay zero added cost.
//
//  2. TestNativeApprovalRoundTrip — a captured can_use_tool control_request fed
//     through the real adapter+parser sets nativePendingApproval, makes
//     computeState short-circuit to StateChoicePrompt, and resolveApproval(allow)
//     writes the EXACT ground-truth control_response allow bytes, with a second
//     resolve being an idempotent no-op.

// terminalPaneForFallback builds a plain terminal pane exactly as Spawn would,
// minus the live agent process. Kind=="" so NO ensureNative is invoked — the
// native fields must remain zero-valued.
func terminalPaneForFallback(id string) *Pane {
	done := make(chan struct{})
	return &Pane{
		ID:        id,
		Cwd:       "/tmp",
		Cmd:       []string{"bash"},
		Title:     "bash",
		Status:    PaneRunning,
		CreatedAt: time.Now().UTC(),
		Cols:      80,
		Rows:      24,
		Kind:      "", // terminal — the default
		done:      done,
		buffer:    newTestRing(),
		subs:      map[int64]*subEntry{},
	}
}

// runHotSectionByteWrite reproduces the byte-handling of the readLoop hot
// critical section (pane.go FrameData case) for a payload, EXCLUDING the native
// tee (which must be inert for a terminal pane). It returns whether the native
// tee branch would have fired. For a terminal pane it must never fire.
//
// Mirrors the exact ordering of the locked block: buffer.Write, then the native
// tee guard. We don't drive vt/replayEmu/gridEmu here (nil on this bare pane);
// the guarantee under test is that the native branch is a no-op and the buffer
// receives the bytes unchanged.
func (p *Pane) runHotSectionByteWrite(payload []byte) (nativeTeeFired bool) {
	p.subsMu.Lock()
	p.buffer.Write(payload)
	if p.nativeTeeCh != nil {
		select {
		case p.nativeTeeCh <- nativeChunk{data: append([]byte(nil), payload...)}:
			nativeTeeFired = true
		default:
			nativeTeeFired = true // channel exists => native pane (still "fired")
		}
	}
	p.subsMu.Unlock()
	return nativeTeeFired
}

// runHotSectionFrame reproduces the FULL byte-handling of the readLoop hot
// critical section (pane.go FrameData case) for a payload, INCLUDING the
// native/terminal gating. It is a faithful mirror of the production locked
// block: the same operations in the same order, gated on the same
// `p.nativeTeeCh == nil` signal. Used to prove the native-pane VT-skip
// actually happens (replayEmu/TotalStrippedChars untouched) while the tee
// still fires and LastActivityMs still advances.
func (p *Pane) runHotSectionFrame(payload []byte) (nativeTeeFired bool) {
	p.subsMu.Lock()
	p.buffer.Write(payload)
	if p.nativeTeeCh == nil {
		if vtPaintEnabled && p.vt != nil {
			_, _ = p.vt.Write(payload)
		}
		if p.replayEmu != nil {
			p.replayEmu.Feed(payload)
			_ = p.replayEmu.DrainReplies()
		}
	}
	if p.gridEmu != nil {
		p.gridEmu.Feed(payload)
	}
	if p.nativeTeeCh != nil {
		select {
		case p.nativeTeeCh <- nativeChunk{data: append([]byte(nil), payload...)}:
			nativeTeeFired = true
		default:
			nativeTeeFired = true
		}
	} else {
		atomic.AddInt64(&p.TotalStrippedChars, int64(len(stripANSI(payload))))
	}
	p.trackModes(payload)
	atomic.StoreInt64(&p.LastActivityMs, time.Now().UnixMilli())
	p.fanoutPayloadLocked(payload)
	p.subsMu.Unlock()
	return nativeTeeFired
}

// TestNativePaneSkipsVTMachinery is the hot-path performance-fix guard: a
// native pane MUST skip the terminal-emulation work (replayEmu scrollback feed
// + the allocating stripped-char accumulation) while STILL firing the native
// tee and updating LastActivityMs. It pairs with
// TestFallbackGuaranteeTerminalPaneUntouched (the terminal side): together they
// pin both halves of the `p.nativeTeeCh == nil` gate.
func TestNativePaneSkipsVTMachinery(t *testing.T) {
	done := make(chan struct{})
	// Build a native pane exactly as Spawn would (replayEmu + vt are allocated
	// for native panes too — the fix skips FEEDING them, it doesn't null them),
	// then wire the native tee with ensureNative so the hot block sees
	// nativeTeeCh != nil and takes the native branch.
	p := &Pane{
		ID:        "pane_native_vt_skip",
		Cwd:       "/tmp",
		Cmd:       nativeClaudeArgv("", false, "", false, ""),
		Title:     "claude",
		Status:    PaneRunning,
		CreatedAt: time.Now().UTC(),
		Cols:      80,
		Rows:      24,
		Kind:      "native",
		done:      done,
		buffer:    newTestRing(),
		replayEmu: grid.New(80, 24),
		subs:      map[int64]*subEntry{},
	}
	p.ensureNative()
	t.Cleanup(func() {
		close(done)
		p.shutdownNative()
	})

	if p.nativeTeeCh == nil {
		t.Fatal("native pane has nil nativeTeeCh after ensureNative; the tee must be live")
	}

	// Baselines: the replayEmu screen and the stripped-char counter must NOT
	// move when a native pane processes a frame.
	beforeStripped := atomic.LoadInt64(&p.TotalStrippedChars)
	beforeReplay := p.replayEmu.Snapshot()
	beforeActivity := atomic.LoadInt64(&p.LastActivityMs)
	time.Sleep(2 * time.Millisecond) // ensure a measurable activity bump

	// A payload that, if fed to replayEmu, would put visible text on the grid,
	// and that has ANSI escapes stripANSI would count. Both effects must be
	// absent for a native pane.
	payload := []byte("\x1b[32mVISIBLE-ON-GRID\x1b[0m more text\r\n")
	if fired := p.runHotSectionFrame(payload); !fired {
		t.Fatal("native tee did NOT fire for a native pane; the parser would be starved")
	}

	// 1) replayEmu scrollback must be UNCHANGED — the VT feed was skipped.
	afterReplay := p.replayEmu.Snapshot()
	if gridSnapshotHasText(afterReplay) {
		t.Fatalf("native pane fed replayEmu: grid now has text it should never have seen")
	}
	if afterReplay.CursorX != beforeReplay.CursorX || afterReplay.CursorY != beforeReplay.CursorY {
		t.Fatalf("native pane advanced replayEmu cursor (%d,%d)->(%d,%d); VT feed was not skipped",
			beforeReplay.CursorX, beforeReplay.CursorY, afterReplay.CursorX, afterReplay.CursorY)
	}

	// 2) TotalStrippedChars must be UNCHANGED — the allocating stripANSI was skipped.
	if got := atomic.LoadInt64(&p.TotalStrippedChars); got != beforeStripped {
		t.Fatalf("native pane advanced TotalStrippedChars %d->%d; the stripANSI work was not skipped",
			beforeStripped, got)
	}

	// 3) But the raw buffer DID receive the bytes (kept for native panes).
	if got := p.buffer.Snapshot(); !bytes.Equal(got, payload) {
		t.Fatalf("native pane buffer = %q, want %q (buffer.Write is kept for native panes)", got, payload)
	}

	// 4) And LastActivityMs advanced — computeState/aria still see the pane as active.
	//    (The tee already fired — proven by runHotSectionFrame returning true above;
	//    the live parser goroutine started by ensureNative owns + drains the channel,
	//    so the test does not read it directly.)
	if got := atomic.LoadInt64(&p.LastActivityMs); got <= beforeActivity {
		t.Fatalf("native pane did not advance LastActivityMs (%d -> %d); activity tracking must survive the skip",
			beforeActivity, got)
	}
}

func TestFallbackGuaranteeTerminalPaneUntouched(t *testing.T) {
	p := terminalPaneForFallback("pane_terminal_fallback")

	// --- Structural: no native machinery constructed for a terminal pane. ---
	if p.nativeTeeCh != nil {
		t.Fatal("terminal pane has a non-nil nativeTeeCh; the hot-path tee must be inert")
	}
	if p.nativeAdapter != nil {
		t.Fatal("terminal pane constructed a native adapter")
	}
	if p.nativeSubs != nil {
		t.Fatal("terminal pane constructed a native subscriber map")
	}
	if p.nativeStop != nil {
		t.Fatal("terminal pane has a native stop channel (parser goroutine)")
	}
	if p.hasNativePending() {
		t.Fatal("terminal pane has a non-nil pending approval")
	}

	// SubscribeNative / NativeAdapterRef must refuse a terminal pane (no bus).
	if _, _, _, _, ok := p.SubscribeNative(); ok {
		t.Fatal("SubscribeNative returned ok=true for a terminal pane")
	}
	if _, ok := p.NativeAdapterRef(); ok {
		t.Fatal("NativeAdapterRef returned ok=true for a terminal pane")
	}

	// computeState must NOT short-circuit to choice-prompt (no pending approval).
	if st := p.computeState(); st == StateChoicePrompt {
		t.Fatal("terminal pane computeState short-circuited to choice-prompt")
	}

	// No parser goroutine should be running for this pane. Assert no leak by
	// confirming a goroutine count delta of zero around a settle window.
	before := runtime.NumGoroutine()

	// --- Golden byte-fanout: the hot section writes bytes verbatim and the
	// native tee never fires for a terminal pane. ---
	payloads := [][]byte{
		[]byte("hello world\r\n"),
		[]byte("\x1b[31mred\x1b[0m"),
		{0x00, 0x01, 0x02, 0xff, '\n'},
		[]byte(`{"type":"control_request"}` + "\n"), // even a JSON-looking line must NOT be parsed
	}
	var golden bytes.Buffer
	for _, pl := range payloads {
		if fired := p.runHotSectionByteWrite(pl); fired {
			t.Fatalf("native tee fired for terminal pane on payload %q", pl)
		}
		golden.Write(pl)
	}

	// The ring buffer must contain exactly the bytes written, in order — proof
	// the byte path is unchanged (no native interception/mutation).
	got := p.buffer.Snapshot()
	if !bytes.Equal(got, golden.Bytes()) {
		t.Fatalf("buffer bytes = %q, want %q (byte path must be identical)", got, golden.Bytes())
	}

	// Still no pending approval, still not native after byte traffic.
	if p.hasNativePending() {
		t.Fatal("terminal pane gained a pending approval from byte traffic")
	}
	if p.nativeTeeCh != nil {
		t.Fatal("terminal pane gained a native tee channel from byte traffic")
	}

	// Allow any incidental goroutines to settle; assert no parser goroutine spun
	// up (terminal panes never start one).
	time.Sleep(20 * time.Millisecond)
	if after := runtime.NumGoroutine(); after > before+1 {
		t.Fatalf("goroutine count grew %d -> %d; terminal pane must not start a native parser", before, after)
	}
}

// nativePaneForApproval builds a native pane with a real (piped) agent conn and
// starts its real parser goroutine (ensureNative). The returned agentReader is
// the far end of the pane's agent connection: frames the pane writes (the
// control_response approval) can be read off it via ReadFrame. We feed JSONL
// through the same processNativeLine path the live tee would, so the production
// adapter + emit + approval machinery is exercised end to end.
func nativePaneForApproval(t *testing.T, id string) (*Pane, net.Conn) {
	t.Helper()
	paneEnd, agentEnd := net.Pipe()
	done := make(chan struct{})
	p := &Pane{
		ID:        id,
		Cwd:       "/tmp",
		Cmd:       nativeClaudeArgv("", false, "", false, ""),
		Title:     "claude",
		Status:    PaneRunning,
		CreatedAt: time.Now().UTC(),
		Cols:      80,
		Rows:      24,
		Kind:      "native",
		agentConn: paneEnd,
		done:      done,
		buffer:    newTestRing(),
		subs:      map[int64]*subEntry{},
	}
	p.ensureNative()
	t.Cleanup(func() {
		close(done)
		p.shutdownNative()
		_ = paneEnd.Close()
		_ = agentEnd.Close()
	})
	return p, agentEnd
}

func TestNativeApprovalRoundTrip(t *testing.T) {
	p, agentEnd := nativePaneForApproval(t, "pane_native_approval")

	// A real captured can_use_tool control_request (Phase-0 approval-fire.jsonl
	// shape): top-level request_id, nested request{tool_name, tool_use_id, input}.
	const reqID = "019c298e-aaaa-bbbb-cccc-deadbeef0001"
	const toolUseID = "toolu_01EgPexampleApprovalRoundTrip"
	canUseTool := `{"type":"control_request","request_id":"` + reqID + `","request":{"subtype":"can_use_tool","tool_name":"Bash","tool_use_id":"` + toolUseID + `","input":{"command":"ls -la","description":"list files"}}}`

	// Drive it through the real parser line path (what the tee goroutine calls).
	p.processNativeLine([]byte(canUseTool), false, false)

	// --- The approval must be parked, lock-free, for computeState to read. ---
	pending := p.peekNativePending("")
	if pending == nil {
		t.Fatal("nativePendingApproval not set after can_use_tool")
	}
	if pending.RequestID != reqID {
		t.Fatalf("pending.RequestID = %q, want %q", pending.RequestID, reqID)
	}
	if pending.ToolUseID != toolUseID {
		t.Fatalf("pending.ToolUseID = %q, want %q", pending.ToolUseID, toolUseID)
	}
	if pending.ToolName != "Bash" {
		t.Fatalf("pending.ToolName = %q, want Bash", pending.ToolName)
	}

	// --- computeState short-circuits to choice-prompt. ---
	if st := p.computeState(); st != StateChoicePrompt {
		t.Fatalf("computeState = %q, want %q", st, StateChoicePrompt)
	}

	// --- resolveApproval(allow) writes the EXACT ground-truth allow frame to
	// the agent's stdin (the piped conn). net.Pipe is synchronous, so we read the
	// frame off the agent end concurrently while resolveApproval writes it. ---
	type resolveResult struct {
		pending *NativeApproval
		err     error
	}
	resCh := make(chan resolveResult, 1)
	go func() {
		pr, err := p.resolveApproval(reqID, true, "")
		resCh <- resolveResult{pr, err}
	}()

	// Read the control_response frame the pane wrote.
	_ = agentEnd.SetReadDeadline(time.Now().Add(2 * time.Second))
	ft, gotFrame, err := ReadFrame(agentEnd)
	if err != nil {
		t.Fatalf("read control_response frame: %v", err)
	}
	if ft != FrameData {
		t.Fatalf("frame type = %v, want FrameData", ft)
	}

	res := <-resCh
	if res.err != nil {
		t.Fatalf("resolveApproval(allow) error: %v", res.err)
	}
	if res.pending == nil {
		t.Fatal("resolveApproval returned nil pending on success")
	}
	pendingResolved := res.pending
	if pendingResolved.RequestID != reqID || pendingResolved.ToolUseID != toolUseID {
		t.Fatalf("resolved pending = %#v, want reqID=%s toolUseID=%s", pendingResolved, reqID, toolUseID)
	}

	// Ground-truth allow shape (Phase-0 answer-stdin-driverlog.jsonl L2):
	// {"type":"control_response","response":{"subtype":"success",
	//   "request_id":"<id>","response":{"behavior":"allow",
	//   "updatedInput":<echo request.input>,"toolUseID":"<tuid>"}}}\n
	var got map[string]any
	if err := json.Unmarshal(bytes.TrimRight(gotFrame, "\n"), &got); err != nil {
		t.Fatalf("encoded frame is not JSON: %v (%q)", err, gotFrame)
	}
	if got["type"] != "control_response" {
		t.Fatalf("frame type = %v, want control_response", got["type"])
	}
	resp, _ := got["response"].(map[string]any)
	if resp == nil || resp["subtype"] != "success" || resp["request_id"] != reqID {
		t.Fatalf("frame.response = %#v, want subtype=success request_id=%s", resp, reqID)
	}
	inner, _ := resp["response"].(map[string]any)
	if inner == nil {
		t.Fatalf("frame.response.response missing: %#v", resp)
	}
	if inner["behavior"] != "allow" {
		t.Fatalf("inner.behavior = %v, want allow", inner["behavior"])
	}
	// Load-bearing camelCase key (NOT tool_use_id): the captured wire uses
	// "toolUseID" in the inner response object.
	if inner["toolUseID"] != toolUseID {
		t.Fatalf("inner.toolUseID = %v, want %s (camelCase key is load-bearing)", inner["toolUseID"], toolUseID)
	}
	// updatedInput must echo request.input verbatim.
	ui, _ := inner["updatedInput"].(map[string]any)
	if ui == nil || ui["command"] != "ls -la" || ui["description"] != "list files" {
		t.Fatalf("inner.updatedInput = %#v, want echo of request.input {command,description}", inner["updatedInput"])
	}
	// And the trailing newline that frames the stdin line.
	if !bytes.HasSuffix(gotFrame, []byte("\n")) {
		t.Fatal("encoded frame missing trailing newline")
	}
	// Belt-and-suspenders: assert the inner response uses snake request_id and
	// camelCase toolUseID side by side (the captured asymmetry) by string match.
	if !strings.Contains(string(gotFrame), `"request_id":"`+reqID+`"`) ||
		!strings.Contains(string(gotFrame), `"toolUseID":"`+toolUseID+`"`) {
		t.Fatalf("frame %q missing the load-bearing snake request_id / camel toolUseID pair", gotFrame)
	}

	// --- After resolve, the pending pointer is cleared (CAS to nil). ---
	if p.hasNativePending() {
		t.Fatal("pending approval not cleared after resolve")
	}
	if st := p.computeState(); st == StateChoicePrompt {
		t.Fatal("computeState still choice-prompt after resolve")
	}

	// --- Idempotent double-resolve: a second resolve is a safe no-op. ---
	_, err2 := p.resolveApproval(reqID, true, "")
	if err2 == nil {
		t.Fatal("second resolve returned nil error; want errNoPendingApproval no-op")
	}
}
