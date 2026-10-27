package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"time"
)

// Provider boundaries, rather than terminal output or quiet, define a turn.
// These values contain identifiers/timestamps only, never transcript content.
type providerTurn struct {
	Agent     string
	Session   string
	ID        string
	StartedMs int64
	EndedMs   int64
	Outcome   string
}

type turnProviderParser struct {
	uncertain      bool
	agent, session string
	active         *providerTurn
	completed      []providerTurn
	claudeHead     string
	claudeFinal    bool
	museRoots      map[string]bool
}

func (p *turnProviderParser) start(id string, ms int64) {
	if id == "" || len(id) > 256 || ms <= 0 {
		return
	}
	if p.active != nil && p.active.ID == id {
		return
	}
	p.uncertain = false
	p.active = &providerTurn{Agent: p.agent, Session: p.session, ID: id, StartedMs: ms}
	p.claudeHead, p.claudeFinal = id, false
}
func (p *turnProviderParser) end(id string, ms int64, outcome string) {
	if p.active == nil || id != p.active.ID || ms < p.active.StartedMs {
		return
	}
	p.uncertain = false
	ended := *p.active
	ended.EndedMs, ended.Outcome = ms, outcome
	p.completed = append(p.completed, ended)
	if len(p.completed) > turnHistoryCap {
		p.completed = p.completed[len(p.completed)-turnHistoryCap:]
	}
	p.active = nil
	p.claudeHead, p.claudeFinal = "", false
}
func (p *turnProviderParser) feed(line []byte, nowMs int64) {
	switch p.agent {
	case agentCodex:
		p.codex(line, nowMs)
	case agentClaude:
		p.claude(line, nowMs)
	case agentMuse:
		p.muse(line, nowMs)
	}
}
func turnProviderTime(raw string, nowMs int64) int64 {
	tm, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil || tm.UnixMilli() <= 0 || tm.UnixMilli() > nowMs+5000 {
		return 0
	}
	return tm.UnixMilli()
}
func (p *turnProviderParser) codex(line []byte, nowMs int64) {
	if !bytes.Contains(line, []byte(`"task_started"`)) && !bytes.Contains(line, []byte(`"task_complete"`)) && !bytes.Contains(line, []byte(`"turn_aborted"`)) {
		return
	}
	var env struct {
		Type      string `json:"type"`
		Timestamp string `json:"timestamp"`
		Payload   struct {
			Type   string `json:"type"`
			TurnID string `json:"turn_id"`
		} `json:"payload"`
	}
	if json.Unmarshal(line, &env) != nil || env.Type != "event_msg" {
		return
	}
	ms := turnProviderTime(env.Timestamp, nowMs)
	if ms == 0 {
		return
	}
	switch env.Payload.Type {
	case "task_started":
		p.start(env.Payload.TurnID, ms)
	case "task_complete":
		p.end(env.Payload.TurnID, ms, "completed")
	case "turn_aborted":
		p.end(env.Payload.TurnID, ms, "interrupted")
	}
}
func (p *turnProviderParser) claude(line []byte, nowMs int64) {
	var env struct {
		Type         string          `json:"type"`
		Subtype      string          `json:"subtype"`
		UUID         string          `json:"uuid"`
		Parent       string          `json:"parentUuid"`
		Timestamp    string          `json:"timestamp"`
		Session      string          `json:"sessionId"`
		Sidechain    bool            `json:"isSidechain"`
		PromptSource string          `json:"promptSource"`
		Meta         bool            `json:"isMeta"`
		Compact      bool            `json:"isCompactSummary"`
		Visible      bool            `json:"isVisibleInTranscriptOnly"`
		Message      json.RawMessage `json:"message"`
	}
	if json.Unmarshal(line, &env) != nil || env.Sidechain || env.Session != p.session {
		return
	}
	ms := turnProviderTime(env.Timestamp, nowMs)
	if ms == 0 {
		return
	}
	if env.Type == "user" {
		kind := classifyClaudeUserRecord(env.PromptSource, env.Meta, env.Compact, env.Visible, env.Message)
		if kind == claudeUserPrompt {
			p.start(env.UUID, ms)
			return
		}
		if p.active == nil || env.Parent != p.claudeHead {
			return
		}
		if kind == claudeUserInterrupt {
			p.end(p.active.ID, ms, "interrupted")
			return
		}
		// A continuation after a final-looking assistant record invalidates that
		// final marker. Its own matching final answer must precede duration.
		p.claudeFinal = false
	}
	if p.active == nil || env.Parent != p.claudeHead || env.UUID == "" {
		return
	}
	if env.Type == "system" && env.Subtype == "turn_duration" {
		if p.claudeFinal {
			p.end(p.active.ID, ms, "completed")
		}
		return
	}
	if env.Type == "assistant" {
		var msg struct {
			Stop string `json:"stop_reason"`
		}
		if json.Unmarshal(env.Message, &msg) != nil {
			return
		}
		p.claudeFinal = msg.Stop == "end_turn" || msg.Stop == "stop_sequence"
	}
	// Advance only the main conversation chain. Tool results are continuations,
	// never terminal boundaries. Metadata/sidechain records cannot settle a turn.
	if env.Type == "assistant" || env.Type == "user" {
		p.claudeHead = env.UUID
	}
}
func (p *turnProviderParser) muse(line []byte, nowMs int64) {
	if !bytes.Contains(line, []byte(`"started"`)) && !bytes.Contains(line, []byte(`"terminal"`)) && !bytes.Contains(line, []byte(`"top_level_turn.run_origin"`)) {
		return
	}
	var env struct {
		Stream struct {
			Kind string `json:"kind"`
			ID   string `json:"id"`
		} `json:"stream"`
		Type     string `json:"payload_type"`
		Recorded int64  `json:"recorded_at"`
		Payload  struct {
			Kind   string `json:"kind"`
			RunID  string `json:"run_id"`
			Record struct {
				Type string `json:"payload_type"`
			} `json:"record"`
			Event struct {
				Kind     string `json:"kind"`
				Terminal string `json:"terminal"`
			} `json:"event"`
		} `json:"payload"`
	}
	if json.Unmarshal(line, &env) != nil || env.Stream.Kind != "session" || env.Stream.ID != p.session {
		return
	}
	ms := env.Recorded / 1000 // Muse persists integer microseconds, not float seconds.
	if ms <= 0 || ms > nowMs+5000 {
		return
	}
	run := env.Payload.RunID
	if env.Type == "runtime.retained_fact" && env.Payload.Record.Type == "top_level_turn.run_origin" && run != "" {
		if p.museRoots == nil || len(p.museRoots) >= 64 {
			p.museRoots = map[string]bool{}
		}
		p.museRoots[run] = true
		return
	}
	if env.Type != "runtime.session" || env.Payload.Kind != "run" || !p.museRoots[run] {
		return
	}
	switch env.Payload.Event.Kind {
	case "started":
		p.start(run, ms)
	case "terminal":
		outcome := strings.ToLower(env.Payload.Event.Terminal)
		if outcome != "completed" && outcome != "failed" && outcome != "interrupted" {
			return
		}
		p.end(run, ms, outcome)
		delete(p.museRoots, run)
	}
}
