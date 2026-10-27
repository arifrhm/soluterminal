package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// Real Spark envelope lines captured from muse 0.1.0 `exec --json`
// (echo provider probe, 2026-08-08), plus the driver's synthetic lines.

const museInitLine = `{"solu_muse":"driver_init","session_id":"11111111-2222-4333-8444-555555555555","cwd":"/home/u/proj"}`

const museMetadataLine = `{"schema_version":1,"id":"d4236efc","stream":{"kind":"session","id":"11111111-2222-4333-8444-555555555555"},"sequence":1,"record_type":"event","payload_type":"runtime.session.metadata","payload_schema_version":1,"payload":{"kind":"metadata","record":{"build":{"semver":"0.1.0"},"model_id":"muse-spark-1.2-contributor","provider_id":"meta","workspace_root":"/home/u/proj"}}}`

const museUserLineJSON = `{"schema_version":1,"id":"x","stream":{"kind":"session","id":"11111111-2222-4333-8444-555555555555"},"sequence":3,"record_type":"status","payload_type":"turn.input.user","payload_schema_version":1,"payload":{"command_id":"cmd-1","kind":"turn_input_user","prompt":"hello muse"}}`

const museDeltaLine = `{"schema_version":1,"id":"x","stream":{"kind":"session","id":"11111111-2222-4333-8444-555555555555"},"sequence":19,"record_type":"status","payload_type":"run.output.delta","payload_schema_version":1,"payload":{"command_id":"cmd-1","kind":"run_output_delta","text":"echo: hello muse"}}`

const museTerminalLine = `{"schema_version":1,"id":"x","stream":{"kind":"session","id":"11111111-2222-4333-8444-555555555555"},"sequence":33,"record_type":"event","payload_type":"run.terminal.completed","payload_schema_version":1,"payload":{"command_id":"cmd-1","kind":"run_terminal","reason":null,"terminal":"completed","text":"echo: hello muse"}}`

const museTaskFailedLine = `{"schema_version":1,"id":"x","stream":{"kind":"session","id":"1"},"sequence":29,"record_type":"event","payload_type":"task.lifecycle.failed","payload_schema_version":1,"payload":{"command_id":"c","event":{"kind":"failed","reason":"provider does not support base instructions","task_id":"t1"},"kind":"task_lifecycle"}}`

func feedAll(t *testing.T, a *MuseAdapter, lines ...string) []NativeEvent {
	t.Helper()
	var out []NativeEvent
	for _, l := range lines {
		out = append(out, a.Feed([]byte(l))...)
	}
	return out
}

func TestMuseAdapterHappyTurn(t *testing.T) {
	t.Parallel()
	a := NewMuseAdapter()
	evs := feedAll(t, a,
		museInitLine,
		"muse: workspace root: /home/u/proj (cwd default)", // non-JSON preamble
		museMetadataLine,
		museUserLineJSON,
		museDeltaLine,
		museTerminalLine,
		`{"solu_muse":"turn_exit","code":0,"interrupted":false}`,
	)

	var types []string
	for _, e := range evs {
		types = append(types, string(e.Type))
	}
	want := []string{"session_init", "session_init", "user_text", "text_delta", "text", "turn_end"}
	if strings.Join(types, ",") != strings.Join(want, ",") {
		t.Fatalf("event types = %v, want %v", types, want)
	}

	if evs[0].SessionID != "11111111-2222-4333-8444-555555555555" || evs[0].Cwd != "/home/u/proj" {
		t.Errorf("init event: %+v", evs[0])
	}
	if evs[0].Model != "muse-spark" {
		t.Errorf("placeholder model = %q", evs[0].Model)
	}
	// Metadata upgrades the announced model.
	if evs[1].Model != "muse-spark-1.2-contributor" {
		t.Errorf("upgraded model = %q", evs[1].Model)
	}
	if evs[2].Text != "hello muse" {
		t.Errorf("user text = %q", evs[2].Text)
	}
	if evs[3].Text != "echo: hello muse" || evs[3].MessageID != "cmd-1" {
		t.Errorf("delta = %+v", evs[3])
	}
	if evs[4].Text != "echo: hello muse" {
		t.Errorf("final text = %q", evs[4].Text)
	}
	if evs[5].Subtype != "success" {
		t.Errorf("turn_end subtype = %q", evs[5].Subtype)
	}
	if n := a.UnknownCounts()["nonjson"]; n != 1 {
		t.Errorf("nonjson count = %d", n)
	}
}

// A child that dies without a run.terminal event (crash) still settles the
// turn via the driver's turn_exit line — with an error surfaced.
func TestMuseAdapterCrashSettlesTurn(t *testing.T) {
	t.Parallel()
	a := NewMuseAdapter()
	feedAll(t, a, museInitLine, museUserLineJSON)
	evs := a.Feed([]byte(`{"solu_muse":"turn_exit","code":1,"interrupted":false,"stderr_tail":"boom"}`))
	if len(evs) != 2 || evs[0].Type != EvError || evs[1].Type != EvTurnEnd {
		t.Fatalf("crash events = %+v", evs)
	}
	if !strings.Contains(evs[0].Message, "boom") {
		t.Errorf("stderr tail lost: %q", evs[0].Message)
	}
	if evs[1].Subtype != "error_during_execution" {
		t.Errorf("subtype = %q", evs[1].Subtype)
	}
}

func TestMuseAdapterInterrupt(t *testing.T) {
	t.Parallel()
	a := NewMuseAdapter()
	feedAll(t, a, museInitLine)
	evs := a.Feed([]byte(`{"solu_muse":"turn_exit","code":130,"interrupted":true}`))
	if len(evs) != 1 || evs[0].Type != EvTurnEnd || evs[0].Subtype != "error_interrupted" {
		t.Fatalf("interrupt events = %+v", evs)
	}
}

// turn_exit after a run.terminal is bookkeeping — no duplicate turn_end.
func TestMuseAdapterNoDoubleTurnEnd(t *testing.T) {
	t.Parallel()
	a := NewMuseAdapter()
	feedAll(t, a, museInitLine, museDeltaLine, museTerminalLine)
	if evs := a.Feed([]byte(`{"solu_muse":"turn_exit","code":0,"interrupted":false}`)); len(evs) != 0 {
		t.Fatalf("expected no events after settled turn, got %+v", evs)
	}
}

func TestMuseAdapterTaskFailureIsStatusNotError(t *testing.T) {
	t.Parallel()
	a := NewMuseAdapter()
	evs := a.Feed([]byte(museTaskFailedLine))
	if len(evs) != 1 || evs[0].Type != EvStatus || evs[0].Subtype != "task_failed" {
		t.Fatalf("task failure events = %+v", evs)
	}
}

func TestMuseAdapterUnknownCountsNotEvents(t *testing.T) {
	t.Parallel()
	a := NewMuseAdapter()
	if evs := a.Feed([]byte(`{"payload_type":"totally.new.event","payload":{}}`)); len(evs) != 0 {
		t.Fatalf("unknown produced events: %+v", evs)
	}
	if a.UnknownCounts()["totally.new.event"] != 1 {
		t.Fatalf("unknown not counted: %v", a.UnknownCounts())
	}
}

func TestMuseAdapterEncoders(t *testing.T) {
	t.Parallel()
	a := NewMuseAdapter()
	var op struct {
		Op   string `json:"op"`
		Text string `json:"text"`
	}
	turn := a.EncodeUserTurn("do the thing\nline two")
	if err := json.Unmarshal(turn, &op); err != nil || op.Op != "user" || op.Text != "do the thing\nline two" {
		t.Fatalf("EncodeUserTurn = %s (%v)", turn, err)
	}
	if !strings.HasSuffix(string(turn), "\n") {
		t.Errorf("user turn not newline-terminated")
	}
	intr := a.EncodeInterrupt()
	if err := json.Unmarshal(intr, &op); err != nil || op.Op != "interrupt" {
		t.Fatalf("EncodeInterrupt = %s", intr)
	}
	if a.EncodeApproval("r", "t", true, nil, "") != nil {
		t.Errorf("EncodeApproval should be nil (no interactive approvals)")
	}
	if a.SupportsInteractiveApproval() {
		t.Errorf("SupportsInteractiveApproval must be false")
	}
}

func TestNewMuseSessionIDShape(t *testing.T) {
	t.Parallel()
	id := newMuseSessionID()
	if canon, ok := canonicalMuseSessionID(id); !ok || canon != id {
		t.Fatalf("minted id not canonical: %q", id)
	}
	if id2 := newMuseSessionID(); id2 == id {
		t.Fatalf("two mints collided: %q", id)
	}
}

func TestNativeMuseArgv(t *testing.T) {
	t.Parallel()
	argv := nativeMuseArgv("11111111-2222-4333-8444-555555555555", "", "")
	if len(argv) != 4 || argv[1] != "muse-driver" || argv[2] != "--session-id" {
		t.Fatalf("argv = %v", argv)
	}
	argv = nativeMuseArgv("id", "muse-spark-1.2", "high")
	joined := strings.Join(argv, " ")
	if !strings.Contains(joined, "--model muse-spark-1.2") || !strings.Contains(joined, "--effort high") {
		t.Fatalf("argv missing model/effort: %v", argv)
	}
}
