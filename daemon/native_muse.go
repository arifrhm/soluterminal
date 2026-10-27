package main

import (
	"encoding/json"
	"strconv"
	"strings"
)

// MuseAdapter normalizes the Muse Spark JSONL stream into NativeEvents.
//
// Muse Code has no long-lived interactive protocol — `muse exec --json` is a
// one-shot headless turn. The pane's child is therefore NOT muse itself but
// the daemon binary's own `solud muse-driver` subcommand (muse_driver.go): a
// long-lived process that runs one `muse exec --json --session-id <uuid>`
// child per submitted turn and passes the Spark envelope lines through
// verbatim, adding two synthetic line types of its own:
//
//	{"solu_muse":"driver_init","session_id":…,"cwd":…}   once at startup
//	{"solu_muse":"turn_exit","code":N,"interrupted":b}    after each child exit
//
// That keeps the whole native pipeline unchanged: the agent wrapper owns
// process survival + replay, EncodeUserTurn is a line written to the driver's
// stdin, and Feed here stays a pure line→events map (Spark envelopes are
// versioned {payload_type, payload} records — see the shapes captured from
// muse 0.1.0 in native_muse_test.go).
//
// Approvals: v1 runs muse with its OS sandbox ON and --approval-mode never —
// the sandbox is the guard, mirroring how Codex Focus launched before
// interactive approvals. SupportsInteractiveApproval is honestly false and
// EncodeApproval returns nil.
type MuseAdapter struct {
	sessionID   string
	model       string
	cwd         string
	sessionSent bool

	// turnText accumulates run.output.delta text per command id so the final
	// EvText can fall back to the accumulation when run.terminal carries none.
	turnText  map[string]*strings.Builder
	turnEnded bool

	unknown map[string]uint64
}

func NewMuseAdapter() *MuseAdapter {
	return &MuseAdapter{
		turnText: map[string]*strings.Builder{},
		unknown:  map[string]uint64{},
	}
}

var _ NativeAdapter = (*MuseAdapter)(nil)

func (a *MuseAdapter) SupportsInteractiveApproval() bool { return false }

func (a *MuseAdapter) UnknownCounts() map[string]uint64 {
	out := make(map[string]uint64, len(a.unknown))
	for k, v := range a.unknown {
		out[k] = v
	}
	return out
}

func (a *MuseAdapter) dropUnknown(key string) []NativeEvent {
	a.unknown[key]++
	return nil
}

// museDriverLine is the driver's own synthetic line shape.
type museDriverLine struct {
	SoluMuse    string `json:"solu_muse"`
	SessionID   string `json:"session_id"`
	Cwd         string `json:"cwd"`
	Model       string `json:"model"`
	Code        int    `json:"code"`
	Interrupted bool   `json:"interrupted"`
	StderrTail  string `json:"stderr_tail"`
}

// museSparkLine is the subset of the Spark envelope the adapter reads.
type museSparkLine struct {
	PayloadType string `json:"payload_type"`
	Stream      struct {
		Kind string `json:"kind"`
		ID   string `json:"id"`
	} `json:"stream"`
	Payload json.RawMessage `json:"payload"`
}

func (a *MuseAdapter) sessionInitEvent() NativeEvent {
	model := a.model
	if model == "" {
		model = "muse-spark"
	}
	return NativeEvent{
		Type:      EvSessionInit,
		SessionID: a.sessionID,
		Model:     model,
		Cwd:       a.cwd,
	}
}

func (a *MuseAdapter) Feed(line []byte) []NativeEvent {
	trimmed := strings.TrimSpace(string(line))
	if trimmed == "" {
		return nil
	}
	if !strings.HasPrefix(trimmed, "{") {
		// muse writes a human preamble ("muse: workspace root: …") and the
		// launcher can print update notices. Not protocol — count and skip.
		return a.dropUnknown("nonjson")
	}

	// Driver synthetic lines first — they are ours and unambiguous.
	if strings.Contains(trimmed, `"solu_muse"`) {
		var d museDriverLine
		if err := json.Unmarshal([]byte(trimmed), &d); err != nil {
			return a.dropUnknown("driver-parse")
		}
		switch d.SoluMuse {
		case "driver_init":
			a.sessionID = d.SessionID
			a.cwd = d.Cwd
			if d.Model != "" {
				a.model = d.Model
			}
			a.sessionSent = true
			return []NativeEvent{a.sessionInitEvent()}
		case "turn_exit":
			ended := a.turnEnded
			a.turnEnded = false
			if ended {
				// run.terminal already settled this turn; the exit line is
				// bookkeeping only.
				return nil
			}
			// The muse child died without a terminal event — crash or
			// interrupt. Surface it and settle the turn.
			var out []NativeEvent
			subtype := "success"
			if d.Interrupted {
				subtype = "error_interrupted"
			} else if d.Code != 0 {
				subtype = "error_during_execution"
				msg := "muse exec exited with code " + strconv.Itoa(d.Code)
				if tail := strings.TrimSpace(d.StderrTail); tail != "" {
					msg += ": " + tail
				}
				out = append(out, NativeEvent{Type: EvError, Message: msg})
			}
			return append(out, NativeEvent{Type: EvTurnEnd, Subtype: subtype})
		default:
			return a.dropUnknown("driver:" + d.SoluMuse)
		}
	}

	var env museSparkLine
	if err := json.Unmarshal([]byte(trimmed), &env); err != nil {
		return a.dropUnknown("parse")
	}
	if env.PayloadType == "" {
		return a.dropUnknown("no-payload-type")
	}

	switch env.PayloadType {
	case "runtime.session.metadata":
		var p struct {
			Record struct {
				ModelID       string `json:"model_id"`
				WorkspaceRoot string `json:"workspace_root"`
			} `json:"record"`
		}
		_ = json.Unmarshal(env.Payload, &p)
		changed := false
		if p.Record.ModelID != "" && p.Record.ModelID != a.model {
			a.model = p.Record.ModelID
			changed = true
		}
		if p.Record.WorkspaceRoot != "" && a.cwd == "" {
			a.cwd = p.Record.WorkspaceRoot
		}
		if a.sessionID == "" && env.Stream.Kind == "session" {
			a.sessionID = env.Stream.ID
		}
		// Re-announce the session with the real model id so the metabar
		// upgrades from the "muse-spark" placeholder.
		if changed && a.sessionSent {
			return []NativeEvent{a.sessionInitEvent()}
		}
		return nil

	case "turn.input.user":
		var p struct {
			Prompt string `json:"prompt"`
		}
		_ = json.Unmarshal(env.Payload, &p)
		if strings.TrimSpace(p.Prompt) == "" {
			return nil
		}
		return []NativeEvent{{Type: EvUserText, Text: p.Prompt}}

	case "run.output.delta":
		var p struct {
			CommandID string `json:"command_id"`
			Text      string `json:"text"`
		}
		_ = json.Unmarshal(env.Payload, &p)
		if p.Text == "" {
			return nil
		}
		b := a.turnText[p.CommandID]
		if b == nil {
			b = &strings.Builder{}
			a.turnText[p.CommandID] = b
		}
		b.WriteString(p.Text)
		return []NativeEvent{{Type: EvTextDelta, Text: p.Text, MessageID: p.CommandID}}

	case "run.terminal.completed", "run.terminal.failed", "run.terminal.interrupted":
		var p struct {
			CommandID string `json:"command_id"`
			Terminal  string `json:"terminal"`
			Text      string `json:"text"`
			Reason    string `json:"reason"`
		}
		_ = json.Unmarshal(env.Payload, &p)
		text := p.Text
		if text == "" {
			if b := a.turnText[p.CommandID]; b != nil {
				text = b.String()
			}
		}
		delete(a.turnText, p.CommandID)
		a.turnEnded = true
		var out []NativeEvent
		if text != "" {
			out = append(out, NativeEvent{Type: EvText, Text: text, MessageID: p.CommandID})
		}
		subtype := "success"
		switch p.Terminal {
		case "", "completed":
		case "interrupted":
			subtype = "error_interrupted"
		default:
			subtype = "error_during_execution"
			msg := "muse turn ended: " + p.Terminal
			if strings.TrimSpace(p.Reason) != "" {
				msg += " (" + p.Reason + ")"
			}
			out = append(out, NativeEvent{Type: EvError, Message: msg})
		}
		return append(out, NativeEvent{Type: EvTurnEnd, Subtype: subtype})

	case "tool_batch.effect.started":
		var p struct {
			BatchID string `json:"batch_id"`
			TaskID  string `json:"task_id"`
			Name    string `json:"name"`
		}
		_ = json.Unmarshal(env.Payload, &p)
		id := p.BatchID
		if id == "" {
			id = p.TaskID
		}
		name := p.Name
		if name == "" {
			name = "muse.tool"
		}
		return []NativeEvent{{Type: EvToolCall, CallID: id, Name: name, ToolKind: "exec"}}

	case "tool_batch.effect.terminal":
		var p struct {
			BatchID string `json:"batch_id"`
			TaskID  string `json:"task_id"`
		}
		_ = json.Unmarshal(env.Payload, &p)
		id := p.BatchID
		if id == "" {
			id = p.TaskID
		}
		return []NativeEvent{{Type: EvToolResult, CallID: id}}

	case "task.lifecycle.failed":
		var p struct {
			Event struct {
				Reason string `json:"reason"`
			} `json:"event"`
		}
		_ = json.Unmarshal(env.Payload, &p)
		return []NativeEvent{{Type: EvStatus, Subtype: "task_failed", Detail: p.Event.Reason}}

	case "runtime.command.accepted", "session.run.linked", "run.lifecycle.started",
		"task.stream.linked", "task.lifecycle.proposed", "task.lifecycle.accepted",
		"task.lifecycle.scheduled", "task.lifecycle.started", "task.lifecycle.completed",
		"task.lifecycle.side_effect_intent", "session.end", "runtime.session",
		"run.model.configured", "session.workspace_branch.observed",
		"session.opened.observed", "session.startup_phases.observed",
		"runtime.command_intake.received", "runtime.command_intake.settled",
		"reminder.cleanup_effect.started", "reminder.cleanup_effect.terminal":
		// Known-and-uninteresting for the Focus surface: lifecycle plumbing,
		// reminders, observability. The pane's idle clock still resets on
		// every live line, so these keep the turn "working".
		return nil
	}
	return a.dropUnknown(env.PayloadType)
}

// museUserLine is what EncodeUserTurn writes to the DRIVER's stdin (not to
// muse itself) — one JSON line per operation.
type museUserLine struct {
	Op   string `json:"op"`
	Text string `json:"text,omitempty"`
}

func (a *MuseAdapter) EncodeUserTurn(text string) []byte {
	b, err := json.Marshal(museUserLine{Op: "user", Text: text})
	if err != nil {
		return nil
	}
	return append(b, '\n')
}

func (a *MuseAdapter) EncodeApproval(requestID, toolUseID string, allow bool, updatedInput json.RawMessage, denyMsg string) []byte {
	// No interactive approvals in v1 — muse runs sandboxed with
	// --approval-mode never; there is nothing to answer.
	return nil
}

func (a *MuseAdapter) EncodeInterrupt() []byte {
	b, err := json.Marshal(museUserLine{Op: "interrupt"})
	if err != nil {
		return nil
	}
	return append(b, '\n')
}
