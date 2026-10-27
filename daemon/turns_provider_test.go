package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const turnProviderSession = "11111111-1111-4111-8111-111111111111"
const turnProviderStart = int64(1791178654056)

func providerCodexLine(kind, id string, ms int64) string {
	return fmt.Sprintf(`{"type":"event_msg","timestamp":%q,"payload":{"type":%q,"turn_id":%q}}`, time.UnixMilli(ms).UTC().Format(time.RFC3339Nano), kind, id)
}
func TestTurnProviderCodexBoundaries(t *testing.T) {
	p := turnProviderParser{agent: agentCodex, session: turnProviderSession}
	feed := func(line string) { p.feed([]byte(line), turnProviderStart+100000) }
	feed(providerCodexLine("task_started", "turn-1", turnProviderStart))
	// Progress, nested quoted events and a different turn cannot settle work.
	for _, line := range []string{providerCodexLine("agent_message", "turn-1", turnProviderStart+1000), providerCodexLine("task_complete", "other", turnProviderStart+2000), `{"type":"response_item","payload":{"type":"task_complete","turn_id":"turn-1"}}`} {
		feed(line)
	}
	if p.active == nil || len(p.completed) != 0 {
		t.Fatal("non-boundary output completed turn")
	}
	feed(providerCodexLine("task_complete", "turn-1", turnProviderStart+13955))
	if p.active != nil || len(p.completed) != 1 || p.completed[0].EndedMs != 1791178668011 || p.completed[0].StartedMs != turnProviderStart || p.completed[0].Outcome != "completed" {
		t.Fatalf("provider timestamps lost: %+v", p.completed)
	}
	feed(providerCodexLine("task_complete", "turn-1", turnProviderStart+15000))
	if len(p.completed) != 1 {
		t.Fatal("replayed completion duplicated")
	}
	feed(providerCodexLine("task_started", "turn-2", turnProviderStart+20000))
	feed(providerCodexLine("turn_aborted", "turn-2", turnProviderStart+21000))
	if len(p.completed) != 2 || p.completed[1].Outcome != "interrupted" {
		t.Fatal("interrupt claimed success")
	}
}
func claudeTurnLine(kind, id, parent string, ms int64, extra map[string]any) string {
	d := map[string]any{"type": kind, "uuid": id, "parentUuid": parent, "timestamp": time.UnixMilli(ms).UTC().Format(time.RFC3339Nano), "sessionId": turnProviderSession}
	for k, v := range extra {
		d[k] = v
	}
	raw, _ := json.Marshal(d)
	return string(raw)
}
func TestTurnProviderClaudeCorrelatedCompletion(t *testing.T) {
	for _, source := range []string{"typed", "sdk", "queued"} {
		t.Run(source, func(t *testing.T) {
			p := turnProviderParser{agent: agentClaude, session: turnProviderSession}
			feed := func(line string) { p.feed([]byte(line), turnProviderStart+100000) }
			feed(claudeTurnLine("user", "u", "old", turnProviderStart, map[string]any{"promptSource": source, "message": map[string]any{"role": "user", "content": "work"}}))
			feed(claudeTurnLine("assistant", "a", "u", turnProviderStart+1000, map[string]any{"message": map[string]any{"stop_reason": "tool_use"}}))
			feed(claudeTurnLine("system", "duration", "a", turnProviderStart+2000, map[string]any{"subtype": "turn_duration"}))
			if p.active == nil || len(p.completed) != 0 {
				t.Fatal("tool work settled prematurely")
			}
			feed(claudeTurnLine("assistant", "side", "a", turnProviderStart+3000, map[string]any{"isSidechain": true, "message": map[string]any{"stop_reason": "end_turn"}}))
			feed(claudeTurnLine("assistant", "foreign", "a", turnProviderStart+3000, map[string]any{"sessionId": "another", "message": map[string]any{"stop_reason": "end_turn"}}))
			feed(claudeTurnLine("user", "tool-result", "a", turnProviderStart+4000, map[string]any{"message": map[string]any{"content": []any{map[string]any{"type": "tool_result", "tool_use_id": "tool"}}}}))
			feed(claudeTurnLine("assistant", "answer", "tool-result", turnProviderStart+5000, map[string]any{"message": map[string]any{"stop_reason": "end_turn"}}))
			if p.active == nil {
				t.Fatal("final assistant is not yet turn_duration")
			}
			feed(claudeTurnLine("system", "duration", "answer", turnProviderStart+6000, map[string]any{"subtype": "turn_duration"}))
			if p.active != nil || len(p.completed) != 1 || p.completed[0].EndedMs != turnProviderStart+6000 {
				t.Fatalf("missing correlated boundary: %+v", p)
			}
			feed(claudeTurnLine("user", "notice", "duration", turnProviderStart+7000, map[string]any{"promptSource": "system", "message": map[string]any{"content": "background task notification"}}))
			if p.active != nil {
				t.Fatal("notification fabricated a turn")
			}
		})
	}
}
func TestTurnProviderClaudeInterruptIsNotCompletion(t *testing.T) {
	p := turnProviderParser{agent: agentClaude, session: turnProviderSession}
	p.feed([]byte(claudeTurnLine("user", "u", "old", turnProviderStart, map[string]any{"promptSource": "typed", "message": map[string]any{"content": "work"}})), turnProviderStart+10000)
	p.feed([]byte(claudeTurnLine("user", "stop", "u", turnProviderStart+1000, map[string]any{"message": map[string]any{"content": []any{map[string]any{"type": "text", "text": "[Request interrupted by user]"}}}})), turnProviderStart+10000)
	if len(p.completed) != 1 || p.completed[0].Outcome != "interrupted" {
		t.Fatal("user interrupt claimed success")
	}
}

func TestTurnProviderClaudeContinuationInvalidatesFinalMarker(t *testing.T) {
	p := turnProviderParser{agent: agentClaude, session: turnProviderSession}
	feed := func(line string) { p.feed([]byte(line), turnProviderStart+10000) }
	feed(claudeTurnLine("user", "u", "old", turnProviderStart, map[string]any{"promptSource": "typed", "message": map[string]any{"content": "work"}}))
	feed(claudeTurnLine("assistant", "answer", "u", turnProviderStart+1000, map[string]any{"message": map[string]any{"stop_reason": "end_turn"}}))
	feed(claudeTurnLine("user", "result", "answer", turnProviderStart+2000, map[string]any{"message": map[string]any{"content": []any{map[string]any{"type": "tool_result", "tool_use_id": "tool"}}}}))
	feed(claudeTurnLine("system", "duration", "result", turnProviderStart+3000, map[string]any{"subtype": "turn_duration"}))
	if p.active == nil || len(p.completed) != 0 {
		t.Fatal("continuation reused stale final marker")
	}
}
func TestTurnProviderMuseRootRunOnly(t *testing.T) {
	p := turnProviderParser{agent: agentMuse, session: turnProviderSession}
	feed := func(kind, run, event, outcome string, ms int64) {
		d := map[string]any{"payload_type": kind, "stream": map[string]any{"kind": "session", "id": turnProviderSession}, "recorded_at": ms*1000 + 999,
			"payload": map[string]any{"kind": "run", "run_id": run, "event": map[string]any{"kind": event, "terminal": outcome}, "record": map[string]any{"payload_type": "top_level_turn.run_origin"}}}
		raw, _ := json.Marshal(d)
		p.feed(raw, turnProviderStart+10000)
	}
	feed("runtime.session", "child", "started", "", turnProviderStart)
	if p.active != nil {
		t.Fatal("unproven child run started turn")
	}
	feed("runtime.retained_fact", "root", "", "", turnProviderStart)
	feed("runtime.session", "root", "started", "", turnProviderStart)
	feed("runtime.session", "child", "terminal", "completed", turnProviderStart+1000)
	if p.active == nil {
		t.Fatal("child completed root")
	}
	feed("runtime.session", "root", "terminal", "failed", turnProviderStart+2000)
	if len(p.completed) != 1 || p.completed[0].StartedMs != turnProviderStart || p.completed[0].EndedMs != turnProviderStart+2000 || p.completed[0].Outcome != "failed" {
		t.Fatalf("Muse timestamp/outcome: %+v", p.completed)
	}
}
func TestTurnProviderReaderPartialRevalidationAndRotation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	start := providerCodexLine("task_started", "a", turnProviderStart) + "\n"
	end := providerCodexLine("task_complete", "a", turnProviderStart+1000)
	if err := os.WriteFile(path, []byte(start+end[:len(end)/2]), 0600); err != nil {
		t.Fatal(err)
	}
	r := &turnProviderReader{}
	valid := true
	read := func() turnProviderObservation {
		f, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		return r.read(&turnProviderSource{file: f, key: "source", agent: agentCodex, session: turnProviderSession, valid: func() bool { return valid }}, turnProviderStart+10000)
	}
	got := read()
	if !got.available || got.active == nil || len(got.completed) != 0 {
		t.Fatalf("partial boundary: %+v", got)
	}
	os.WriteFile(path, []byte(start+end+"\n"), 0600)
	valid = false
	got = read()
	if got.available || r.parser.active == nil {
		t.Fatal("lost ownership published boundary")
	}
	valid = true
	got = read()
	if len(got.completed) != 1 || got.active != nil {
		t.Fatal("append not consumed once")
	}
	if got = read(); len(got.completed) != 0 {
		t.Fatal("unchanged source duplicated completion")
	}
	os.WriteFile(path, []byte("{}\n"), 0600)
	got = read()
	if !got.initial || got.active != nil || len(got.completed) != 0 {
		t.Fatal("truncation reused old lifecycle")
	}
}
func TestTurnProviderReaderOversizedAndPartialRecords(t *testing.T) {
	path := filepath.Join(t.TempDir(), "large.jsonl")
	os.WriteFile(path, []byte(strings.Repeat("x", turnSourceReadLimit+5)+"\n"+providerCodexLine("task_started", "a", turnProviderStart)+"\n"+providerCodexLine("task_complete", "a", turnProviderStart+1000)+"\n"), 0600)
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	r := &turnProviderReader{}
	got := r.read(&turnProviderSource{file: f, key: "source", agent: agentCodex, session: turnProviderSession, valid: func() bool { return true }}, turnProviderStart+10000)
	if !got.available || len(got.completed) != 1 {
		t.Fatalf("bounded tail lost complete boundary: %+v", got)
	}
}

func TestTurnProviderReaderUnknownTailAndActiveHandoff(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	start := providerCodexLine("task_started", "long", turnProviderStart) + "\n"
	os.WriteFile(path, []byte(start), 0600)
	makeSource := func() *turnProviderSource {
		f, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		return &turnProviderSource{file: f, key: "same-generation", agent: agentCodex, session: turnProviderSession, valid: func() bool { return true }}
	}
	r := &turnProviderReader{}
	observed := r.read(makeSource(), turnProviderStart+10000)
	if observed.active == nil || observed.checkpoint == nil {
		t.Fatal("missing active checkpoint")
	}
	checkpoint := observed.checkpoint
	// Lots of irrelevant live output pushes the provider start outside initial
	// tail discovery. An unseeded reader must admit uncertainty, not say idle.
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString(strings.Repeat("{}\n", turnSourceReadLimit/3+10))
	f.Close()
	fresh := &turnProviderReader{}
	unknown := fresh.read(makeSource(), turnProviderStart+10000)
	if !unknown.uncertain || unknown.active != nil {
		t.Fatal("tail without start declared known idle")
	}
	restored := &turnProviderReader{restore: checkpoint}
	restored.read(makeSource(), turnProviderStart+10000)
	f, _ = os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
	f.WriteString(providerCodexLine("task_complete", "long", turnProviderStart+5000) + "\n")
	f.Close()
	ended := restored.read(makeSource(), turnProviderStart+10000)
	if len(ended.completed) != 1 || ended.completed[0].StartedMs != turnProviderStart || ended.completed[0].EndedMs != turnProviderStart+5000 {
		t.Fatalf("handoff lost authoritative boundary: %+v", ended)
	}
}

func TestTurnProviderReaderRejectsMalformedCheckpoint(t *testing.T) {
	for _, mode := range []string{"negative", "min-int", "past-end", "foreign-active", "ended-active", "oversized-id", "oversized-roots"} {
		t.Run(mode, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "session.jsonl")
			if err := os.WriteFile(path, []byte("{}\n"), 0600); err != nil {
				t.Fatal(err)
			}
			file, err := os.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			info, _ := file.Stat()
			active := &providerTurn{Agent: agentCodex, Session: turnProviderSession, ID: "a", StartedMs: turnProviderStart}
			checkpoint := &turnReaderCheckpoint{Key: "source", FileID: turnFileIdentity(info), Agent: agentCodex, Session: turnProviderSession, Active: active}
			switch mode {
			case "negative":
				checkpoint.Offset = -1
			case "min-int":
				checkpoint.Offset = -1 << 63
			case "past-end":
				checkpoint.Offset = info.Size() + 1
			case "foreign-active":
				active.Session = "other"
			case "ended-active":
				active.EndedMs = turnProviderStart + 100
			case "oversized-id":
				active.ID = strings.Repeat("x", 257)
			case "oversized-roots":
				checkpoint.MuseRoots = map[string]bool{}
				for i := 0; i < 65; i++ {
					checkpoint.MuseRoots[fmt.Sprint(i)] = true
				}
			}
			r := turnProviderReader{restore: checkpoint}
			got := r.read(&turnProviderSource{file: file, key: "source", agent: agentCodex, session: turnProviderSession, valid: func() bool { return true }}, turnProviderStart+10000)
			if !got.available || !got.initial || !got.uncertain || got.active != nil || len(got.completed) != 0 {
				t.Fatalf("invalid checkpoint reused: %+v", got)
			}
		})
	}
}
