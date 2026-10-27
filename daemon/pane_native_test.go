package main

import (
	"encoding/json"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestNativeTurnCompletionBeforeTransportWriteReturnsDoesNotRearmFleetLatch(t *testing.T) {
	daemonConn, agentConn := net.Pipe()
	p := newTestNativePane()
	p.agentConn = daemonConn
	if err := agentConn.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatalf("set peer read deadline: %v", err)
	}
	t.Cleanup(func() {
		_ = daemonConn.Close()
		_ = agentConn.Close()
	})

	payload := []byte("native request")
	writeDone := make(chan error, 1)
	go func() {
		_, err := p.writeNativeTurn(payload)
		writeDone <- err
	}()

	// Consume only the frame header. WriteFrame has exposed the request on the
	// transport but remains blocked writing its payload, which permits a fast
	// provider completion to race the writer's post-write bookkeeping.
	header := make([]byte, 5)
	if _, err := io.ReadFull(agentConn, header); err != nil {
		t.Fatalf("read frame header: %v", err)
	}
	p.emitNativeEvent(NativeEvent{Type: EvTurnEnd})
	if _, err := io.ReadFull(agentConn, make([]byte, len(payload))); err != nil {
		t.Fatalf("read frame payload: %v", err)
	}
	select {
	case err := <-writeDone:
		if err != nil {
			t.Fatalf("write native turn: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("write native turn did not settle")
	}
	if p.fleetTurnAdmitted.Load() {
		t.Fatal("completion observed before WriteFrame returned was overwritten by late admission")
	}
}

// newTestNativePane builds a minimal native-mode Pane with the parser state
// wired up but WITHOUT a real PTY/agent — enough to exercise the parser
// goroutine surface (consume/emit/ring/subs/approval) in isolation.
func newTestNativePane() *Pane {
	p := &Pane{
		ID:   "test-native",
		Kind: "native",
		done: make(chan struct{}),
	}
	p.nativeAdapter = NewClaudeAdapter()
	p.nativeSubs = map[int64]chan NativeEvent{}
	p.nativeStop = make(chan struct{})
	return p
}

const (
	fxNativeInit = `{"type":"system","subtype":"init","cwd":"/tmp/x","session_id":"sess-1","tools":["Bash"],"model":"claude-opus-4-8[1m]","permissionMode":"default","slash_commands":[]}`
	fxNativeReq  = `{"type":"control_request","request_id":"req-1","request":{"subtype":"can_use_tool","tool_name":"Bash","input":{"command":"echo hi"},"tool_use_id":"toolu_1"}}`
	fxNativeDone = `{"type":"result","subtype":"success","is_error":false,"duration_ms":10,"num_turns":1,"result":"ok"}`
	fxNativeCanc = `{"type":"control_cancel_request","request_id":"req-1"}`
)

// drainSeqs collects all events the parser produced into the ring (the ring IS
// the post-stamp record), returning their Seq + Type for assertion.
func ringTypes(p *Pane) []EventType {
	p.nativeMu.Lock()
	defer p.nativeMu.Unlock()
	out := make([]EventType, len(p.nativeRing))
	for i, ev := range p.nativeRing {
		out[i] = ev.Type
	}
	return out
}

func TestEnsureNativeCodexFreshSpawnDoesNotTrustPersistedThreadBeforeHandshake(t *testing.T) {
	t.Parallel()
	p := &Pane{
		ID: "codex-fresh", Kind: "native", Agent: agentCodex,
		CodexThreadID: "thread-to-resume", done: make(chan struct{}),
	}
	p.ensureNative()
	defer p.shutdownNative()

	adapter, ok := p.NativeAdapterRef()
	if !ok {
		t.Fatal("native adapter missing")
	}
	driver, ok := adapter.(*CodexAdapter)
	if !ok {
		t.Fatalf("adapter type = %T, want *CodexAdapter", adapter)
	}
	if driver.Ready() || p.nativeGotInit.Load() {
		t.Fatal("fresh Codex process was marked ready before initialize + thread/resume response")
	}
}

func TestCodexSuccessorCompletesResponseDrivenBootstrap(t *testing.T) {
	daemonConn, agentConn := net.Pipe()
	p := &Pane{
		ID: "codex-fleet-successor", Kind: "native", Agent: agentCodex,
		Status: PaneRunning, Cwd: "/work", CodexThreadID: "thread-existing",
		agentConn: daemonConn, done: make(chan struct{}), buffer: NewRingBuffer(1024),
	}
	p.ensureNative()
	t.Cleanup(func() {
		p.shutdownNative()
		_ = daemonConn.Close()
		_ = agentConn.Close()
	})

	type frameResult struct {
		type_   FrameType
		payload []byte
		err     error
	}
	readFrame := func() <-chan frameResult {
		ch := make(chan frameResult, 1)
		go func() {
			type_, payload, err := ReadFrame(agentConn)
			ch <- frameResult{type_: type_, payload: payload, err: err}
		}()
		return ch
	}
	bootstrapDone := make(chan error, 1)
	firstFrame := readFrame()
	go func() { bootstrapDone <- p.bootstrapNativeCodex() }()
	var initPayload []byte
	select {
	case err := <-bootstrapDone:
		if err != nil {
			t.Fatalf("bootstrap Codex successor: %v", err)
		}
		got := <-firstFrame
		initPayload = got.payload
	case got := <-firstFrame:
		if got.err != nil || got.type_ != FrameData {
			t.Fatalf("initialize frame type=%v err=%v", got.type_, got.err)
		}
		initPayload = got.payload
		if err := <-bootstrapDone; err != nil {
			t.Fatalf("bootstrap Codex successor: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Codex successor bootstrap did not settle")
	}
	initLines := codexFixtureLines(t, initPayload)
	if len(initLines) != 1 || initLines[0]["method"] != "initialize" {
		t.Fatalf("initialize payload = %v", initLines)
	}
	initID := initLines[0]["id"]
	response, err := json.Marshal(map[string]any{"id": initID, "result": map[string]any{"userAgent": "codex_cli_rs/test"}})
	if err != nil {
		t.Fatal(err)
	}
	outboundFrame := readFrame()
	processed := make(chan struct{})
	go func() {
		p.processNativeLine(response, false, false)
		close(processed)
	}()
	got := <-outboundFrame
	if got.err != nil || got.type_ != FrameData {
		t.Fatalf("post-initialize frame type=%v err=%v", got.type_, got.err)
	}
	select {
	case <-processed:
	case <-time.After(time.Second):
		t.Fatal("response-driven Codex handshake did not settle")
	}
	outbound := codexFixtureLines(t, got.payload)
	if len(outbound) != 2 || outbound[0]["method"] != "initialized" || outbound[1]["method"] != "thread/resume" {
		t.Fatalf("post-initialize handshake = %v", outbound)
	}
}

func TestSubmitNativeCodexCompactRoutesDedicatedControlRPC(t *testing.T) {
	t.Parallel()
	daemonConn, agentConn := net.Pipe()
	p := &Pane{
		ID: "codex-compact-input", Kind: "native", Agent: agentCodex,
		Status: PaneRunning, Cwd: "/work", agentConn: daemonConn,
		done: make(chan struct{}), buffer: NewRingBuffer(1024),
	}
	driver := NewCodexAdapter()
	driver.RestoreThread("thread-compact-input", p.Cwd, "gpt-test")
	p.nativeAdapter = driver
	p.nativeSubs = map[int64]chan NativeEvent{}
	p.nativeStop = make(chan struct{})
	t.Cleanup(func() {
		p.shutdownNative()
		_ = daemonConn.Close()
		_ = agentConn.Close()
	})

	type readResult struct {
		frameType FrameType
		payload   []byte
		err       error
	}
	readCh := make(chan readResult, 1)
	go func() {
		frameType, payload, err := ReadFrame(agentConn)
		readCh <- readResult{frameType: frameType, payload: payload, err: err}
	}()

	result, err := p.submitNativeInput("/compact")
	if err != nil {
		t.Fatalf("submit compact: %v", err)
	}
	got := <-readCh
	if got.err != nil || got.frameType != FrameData {
		t.Fatalf("compact frame type=%v err=%v", got.frameType, got.err)
	}
	var request struct {
		Method string `json:"method"`
		Params struct {
			ThreadID string `json:"threadId"`
		} `json:"params"`
	}
	if err := json.Unmarshal(got.payload, &request); err != nil {
		t.Fatalf("decode compact frame: %v", err)
	}
	if request.Method != "thread/compact/start" || request.Params.ThreadID != "thread-compact-input" {
		t.Fatalf("compact request=%+v", request)
	}
	if result.Bytes != len(got.payload) || result.UserSeq == 0 || !p.nativeInTurnNow() {
		t.Fatalf("submit result=%+v inTurn=%v", result, p.nativeInTurnNow())
	}
	if p.consumeEchoSuppress("/compact") {
		t.Fatal("compact control RPC incorrectly armed user-message echo suppression")
	}
	if got := p.Snapshot().LastPrompts; len(got) != 1 || got[0] != "/compact" {
		t.Fatalf("compact prompt history=%q", got)
	}
	p.nativeMu.Lock()
	defer p.nativeMu.Unlock()
	if len(p.nativeRing) != 1 || p.nativeRing[0].Type != EvUserText || p.nativeRing[0].Text != "/compact" {
		t.Fatalf("compact input event=%+v", p.nativeRing)
	}
}

func TestRequestCodexThreadHistoryWritesAfterRestoredReattach(t *testing.T) {
	t.Parallel()
	daemonConn, agentConn := net.Pipe()
	p := &Pane{
		ID: "codex-reattach", Kind: "native", Agent: agentCodex,
		CodexThreadID: "thread-restored", Cwd: "/work", Model: "gpt-test",
		agentConn: daemonConn, done: make(chan struct{}),
	}
	p.ensureNative()
	defer func() {
		p.shutdownNative()
		_ = daemonConn.Close()
		_ = agentConn.Close()
	}()

	adapter, ok := p.NativeAdapterRef()
	if !ok {
		t.Fatal("native adapter missing")
	}
	driver, ok := adapter.(*CodexAdapter)
	if !ok {
		t.Fatalf("adapter type = %T, want *CodexAdapter", adapter)
	}
	driver.RestoreThread(p.CodexThreadID, p.Cwd, p.Model)

	type readResult struct {
		type_   FrameType
		payload []byte
		err     error
	}
	readCh := make(chan readResult, 1)
	go func() {
		type_, payload, err := ReadFrame(agentConn)
		readCh <- readResult{type_: type_, payload: payload, err: err}
	}()
	p.requestCodexThreadHistory()

	got := <-readCh
	if got.err != nil {
		t.Fatalf("read history frame: %v", got.err)
	}
	if got.type_ != FrameData {
		t.Fatalf("history frame type = %v, want FrameData", got.type_)
	}
	var request struct {
		Method string `json:"method"`
		Params struct {
			ThreadID     string `json:"threadId"`
			IncludeTurns bool   `json:"includeTurns"`
		} `json:"params"`
	}
	if err := json.Unmarshal(got.payload, &request); err != nil {
		t.Fatalf("decode history request: %v; payload=%q", err, got.payload)
	}
	if request.Method != "thread/read" || request.Params.ThreadID != "thread-restored" || !request.Params.IncludeTurns {
		t.Fatalf("history request = %+v", request)
	}
}

// TestNativeChunkCarry_SplitAtEveryOffset proves consumeNativeChunk is invariant
// under arbitrary chunk boundaries: feeding the same multi-line byte stream split
// at every possible offset yields the identical stamped event sequence (seq +
// type), matching feeding it whole. This is the chunked-stream-stripper contract.
func TestNativeChunkCarry_SplitAtEveryOffset(t *testing.T) {
	t.Parallel()
	stream := []byte(fxNativeInit + "\n" + fxNativeReq + "\n" + fxNativeDone + "\n")

	// Reference: feed whole.
	ref := newTestNativePane()
	ref.consumeNativeChunk(nil, stream, false)
	wantTypes := ringTypes(ref)
	if len(wantTypes) == 0 {
		t.Fatal("reference produced no events")
	}

	for split := 0; split <= len(stream); split++ {
		p := newTestNativePane()
		var carry []byte
		carry = p.consumeNativeChunk(carry, stream[:split], false)
		_ = p.consumeNativeChunk(carry, stream[split:], false)
		got := ringTypes(p)
		if len(got) != len(wantTypes) {
			t.Fatalf("split=%d: got %d events, want %d (%v)", split, len(got), len(wantTypes), got)
		}
		for i := range got {
			if got[i] != wantTypes[i] {
				t.Fatalf("split=%d: event[%d]=%s want %s", split, i, got[i], wantTypes[i])
			}
		}
		// Seq must be contiguous 1..N regardless of split.
		p.nativeMu.Lock()
		for i, ev := range p.nativeRing {
			if ev.Seq != uint64(i+1) {
				t.Fatalf("split=%d: event[%d] seq=%d want %d", split, i, ev.Seq, i+1)
			}
			if ev.PaneID != "test-native" {
				t.Fatalf("split=%d: event[%d] paneID=%q", split, i, ev.PaneID)
			}
		}
		p.nativeMu.Unlock()
	}
}

// TestNativeChunkCarry_OneByteAtATime feeds the stream a single byte per chunk —
// the worst case for cross-chunk carry — and asserts the same event count.
func TestNativeChunkCarry_OneByteAtATime(t *testing.T) {
	t.Parallel()
	stream := []byte(fxNativeInit + "\n" + fxNativeReq + "\n" + fxNativeDone + "\n")
	ref := newTestNativePane()
	ref.consumeNativeChunk(nil, stream, false)
	want := len(ringTypes(ref))

	p := newTestNativePane()
	var carry []byte
	for i := 0; i < len(stream); i++ {
		carry = p.consumeNativeChunk(carry, stream[i:i+1], false)
	}
	if got := len(ringTypes(p)); got != want {
		t.Fatalf("one-byte-at-a-time: got %d events, want %d", got, want)
	}
}

// TestNativePendingApproval_SetAndClear verifies the parser parks a pending
// approval on a permission_request (=> computeState short-circuits to
// StateChoicePrompt) and clears it on a control_cancel_request and on turn_end.
func TestNativePendingApproval_SetAndClear(t *testing.T) {
	t.Parallel()
	// permission_request -> pending set, with the fields the resolve path needs.
	p := newTestNativePane()
	p.consumeNativeChunk(nil, []byte(fxNativeReq+"\n"), false)
	ap := p.peekNativePending("")
	if ap == nil {
		t.Fatal("permission_request did not park a pending approval")
	}
	if ap.RequestID != "req-1" || ap.ToolUseID != "toolu_1" || ap.ToolName != "Bash" {
		t.Fatalf("approval fields wrong: %+v", ap)
	}
	if len(ap.RawInput) == 0 || !strings.Contains(string(ap.RawInput), "echo hi") {
		t.Fatalf("approval RawInput not echoed: %q", ap.RawInput)
	}

	// control_cancel_request -> pending cleared.
	p.consumeNativeChunk(nil, []byte(fxNativeCanc+"\n"), false)
	if p.hasNativePending() {
		t.Fatal("control_cancel_request did not clear pending approval")
	}

	// Re-arm, then turn_end clears it.
	p.consumeNativeChunk(nil, []byte(fxNativeReq+"\n"), false)
	if !p.hasNativePending() {
		t.Fatal("re-arm failed")
	}
	p.consumeNativeChunk(nil, []byte(fxNativeDone+"\n"), false)
	if p.hasNativePending() {
		t.Fatal("turn_end did not clear pending approval")
	}
}

// TestNativePendingApproval_ReplayDoesNotRepark is the regression for the
// ghost-choice-prompt bug: an already-answered AskUserQuestion re-parked itself
// when a client reconnect replayed the transcript, leaving a phantom pending
// approval (state=choice-prompt) + a false "waiting on approval" alert long
// after the turn finished. Because the drain edges (turn_end/cancel/teardown)
// don't re-fire after a post-settle reconnect, the ghost stuck until daemon
// restart. A REPLAYED permission_request must NOT park.
func TestNativePendingApproval_ReplayDoesNotRepark(t *testing.T) {
	t.Parallel()
	p := newTestNativePane()

	// Original live run: request parks, turn ends, drain clears it.
	p.consumeNativeChunk(nil, []byte(fxNativeReq+"\n"), false)
	if !p.hasNativePending() {
		t.Fatal("live permission_request must park")
	}
	p.consumeNativeChunk(nil, []byte(fxNativeDone+"\n"), false)
	if p.hasNativePending() {
		t.Fatal("turn_end must drain the live approval")
	}

	// Client reconnect: historyBackfill replays the SAME permission_request as a
	// transcript-seeded (FromReplay=true) line. It must NOT re-park a ghost.
	p.processNativeLine([]byte(fxNativeReq), true, false)
	if p.hasNativePending() {
		t.Fatal("replayed permission_request re-parked a ghost approval (state would falsely show choice-prompt)")
	}
	if got := int(p.nativePendingCount.Load()); got != 0 {
		t.Fatalf("pending count after replay = %d, want 0", got)
	}
}

// TestNativeConcurrentApprovals is the regression for the parallel-tool-call bug:
// Claude can emit SEVERAL can_use_tool requests at once (e.g. two WebSearch). A
// single approval slot let the 2nd overwrite the 1st, orphaning it and hanging
// the agent forever. Both must park, both must be independently resolvable.
func TestNativeConcurrentApprovals(t *testing.T) {
	t.Parallel()
	const req2 = `{"type":"control_request","request_id":"req-2","request":{"subtype":"can_use_tool","tool_name":"WebSearch","input":{"query":"x"},"tool_use_id":"toolu_2"}}`
	p := newTestNativePane()

	// Two concurrent permission requests (parallel tool calls).
	p.consumeNativeChunk(nil, []byte(fxNativeReq+"\n"), false)
	p.consumeNativeChunk(nil, []byte(req2+"\n"), false)

	if got := int(p.nativePendingCount.Load()); got != 2 {
		t.Fatalf("pending count = %d, want 2 (the 2nd must NOT overwrite the 1st)", got)
	}
	if p.peekNativePending("req-1") == nil || p.peekNativePending("req-2") == nil {
		t.Fatal("both req-1 and req-2 must be parked")
	}

	// Resolve req-2 first (out of arrival order) — the other must survive.
	got2, err := p.takeNativePending("req-2"), error(nil)
	if got2 == nil || got2.ToolName != "WebSearch" {
		t.Fatalf("take req-2 returned %+v", got2)
	}
	_ = err
	if !p.hasNativePending() {
		t.Fatal("resolving req-2 must NOT clear req-1 (the orphaned-approval hang)")
	}
	if p.peekNativePending("req-1") == nil {
		t.Fatal("req-1 must still be pending after req-2 resolved")
	}

	// Resolve req-1 — now empty.
	if got1 := p.takeNativePending("req-1"); got1 == nil || got1.ToolName != "Bash" {
		t.Fatalf("take req-1 returned %+v", got1)
	}
	if p.hasNativePending() {
		t.Fatal("both resolved — nothing should be pending")
	}
	// Idempotency: a duplicate take is a safe no-op.
	if p.takeNativePending("req-1") != nil {
		t.Fatal("duplicate take of req-1 must be nil")
	}
}

// TestNativeRingBounds confirms the ring drops oldest beyond the count cap and
// that seq keeps climbing past evictions (so ?since= gap-fill stays correct).
func TestNativeRingBounds(t *testing.T) {
	t.Parallel()
	p := newTestNativePane()
	// Emit more text events than the count cap.
	total := nativeRingMaxEvents + 500
	for i := 0; i < total; i++ {
		p.emitNativeEvent(NativeEvent{Type: EvText, Text: "x"})
	}
	p.nativeMu.Lock()
	defer p.nativeMu.Unlock()
	if len(p.nativeRing) > nativeRingMaxEvents {
		t.Fatalf("ring count %d exceeds cap %d", len(p.nativeRing), nativeRingMaxEvents)
	}
	// Seq monotonic and equal to total (no resets on eviction).
	last := p.nativeRing[len(p.nativeRing)-1]
	if last.Seq != uint64(total) {
		t.Fatalf("last seq=%d want %d", last.Seq, total)
	}
	// Oldest retained seq should be total-cap+1 (drop-oldest).
	first := p.nativeRing[0]
	if first.Seq != uint64(total-nativeRingMaxEvents+1) {
		t.Fatalf("first retained seq=%d want %d", first.Seq, total-nativeRingMaxEvents+1)
	}
}

// TestNativeRingByteCap confirms a burst of large payloads is bounded by bytes,
// not just count.
func TestNativeRingByteCap(t *testing.T) {
	t.Parallel()
	p := newTestNativePane()
	big := strings.Repeat("y", 64*1024) // 64 KiB each
	n := (nativeRingMaxBytes / (64 * 1024)) + 50
	for i := 0; i < n; i++ {
		p.emitNativeEvent(NativeEvent{Type: EvText, Text: big})
	}
	p.nativeMu.Lock()
	defer p.nativeMu.Unlock()
	if p.nativeRingBytes > nativeRingMaxBytes && len(p.nativeRing) > 1 {
		t.Fatalf("ring bytes %d exceed cap %d with %d events",
			p.nativeRingBytes, nativeRingMaxBytes, len(p.nativeRing))
	}
}

// TestSubscribeNative_SnapshotThenLive verifies a subscriber gets the ring
// backfill snapshot AND subsequent live events, and that cancel removes it.
func TestSubscribeNative_SnapshotThenLive(t *testing.T) {
	t.Parallel()
	p := newTestNativePane()
	// Pre-load two events into the ring.
	p.emitNativeEvent(NativeEvent{Type: EvText, Text: "a"})
	p.emitNativeEvent(NativeEvent{Type: EvText, Text: "b"})

	id, snap, ch, cancel, ok := p.SubscribeNative()
	if !ok {
		t.Fatal("SubscribeNative not ok on native pane")
	}
	if len(snap) != 2 {
		t.Fatalf("snapshot len=%d want 2", len(snap))
	}
	if snap[0].Seq != 1 || snap[1].Seq != 2 {
		t.Fatalf("snapshot seqs = %d,%d want 1,2", snap[0].Seq, snap[1].Seq)
	}

	// Live event arrives on the channel.
	p.emitNativeEvent(NativeEvent{Type: EvText, Text: "c"})
	select {
	case ev := <-ch:
		if ev.Seq != 3 || ev.Text != "c" {
			t.Fatalf("live event = seq %d text %q", ev.Seq, ev.Text)
		}
	default:
		t.Fatal("no live event delivered to subscriber")
	}

	// NativeSince backfill.
	since := p.NativeSince(2)
	if len(since) != 1 || since[0].Seq != 3 {
		t.Fatalf("NativeSince(2) = %+v, want one event seq 3", since)
	}

	cancel()
	p.nativeMu.Lock()
	_, stillThere := p.nativeSubs[id]
	p.nativeMu.Unlock()
	if stillThere {
		t.Fatal("cancel did not remove subscriber")
	}
}

// TestSubscribeNative_NonNativeFalse verifies a terminal pane (no native state)
// reports ok=false rather than constructing a bus.
func TestSubscribeNative_NonNativeFalse(t *testing.T) {
	t.Parallel()
	p := &Pane{ID: "term"}
	if _, _, _, _, ok := p.SubscribeNative(); ok {
		t.Fatal("SubscribeNative should be false for a terminal pane")
	}
	if got := p.NativeSince(0); got != nil {
		t.Fatalf("NativeSince on terminal pane = %v, want nil", got)
	}
}

// TestShutdownNative_NoLeak verifies shutdownNative closes subscriber channels
// (their SSE loops terminate), clears the pending approval, and the parser
// goroutine exits. Runs the real parse loop goroutine to catch send-on-closed
// races (run under -race).
func TestShutdownNative_NoLeak(t *testing.T) {
	t.Parallel()
	p := newTestNativePane()
	tee := make(chan nativeChunk, nativeTeeBuf)
	p.nativeTeeCh = tee
	stop := p.nativeStop
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); p.nativeParseLoop(tee, stop) }()

	// Feed a benign (non-approval) event through the live loop and let it land.
	tee <- nativeChunk{data: []byte(fxNativeInit + "\n")}

	_, _, ch, _, ok := p.SubscribeNative()
	if !ok {
		t.Fatal("subscribe failed")
	}
	// Park an approval directly (simulating a prior permission_request) so we
	// can assert shutdownNative clears it.
	p.setNativePending(&NativeApproval{RequestID: "stale"})

	p.shutdownNative()

	// Parser goroutine must exit (stop closed).
	wg.Wait()

	// Subscriber channel closed — draining it must terminate, not block.
	for range ch {
	}

	if p.hasNativePending() {
		t.Fatal("shutdownNative did not clear pending approval")
	}
	if p.nativeTeeCh != nil {
		t.Fatal("shutdownNative did not nil the tee channel")
	}
}
