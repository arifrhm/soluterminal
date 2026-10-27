package main

// Schedule reports → Aria chats. A task schedule may carry a DeliverChat
// ref; when its run fires, the report posts into that chat as one of the
// user's Aria agents via the gateway's REST /v1/send — deterministic, no
// model run, journaled and rate-capped server-side. The agent's key is the
// only credential involved; the responder loop doesn't need to be enabled
// (sends work in any Aria-side mode — mode only governs who ANSWERS).

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// AriaChatRef is a schedule's delivery destination.
type AriaChatRef struct {
	AgentID  string `json:"agentId,omitempty"` // "" = first configured agent
	Source   string `json:"source"`            // wa | telegram | soluverse | email
	ChatID   string `json:"chatId"`
	ChatName string `json:"chatName,omitempty"` // display only
}

func (ref AriaChatRef) label() string {
	if strings.TrimSpace(ref.ChatName) != "" {
		return ref.Source + "/" + strings.TrimSpace(ref.ChatName)
	}
	return ref.Source + "/" + ref.ChatID
}

// SetAriaResponder wires the responder in after boot (the schedule store is
// constructed first). Nil-safe consumers: delivery just stamps a failure.
func (s *ScheduleStore) SetAriaResponder(a *AriaResponder) {
	s.mu.Lock()
	s.aria = a
	s.mu.Unlock()
}

func (s *ScheduleStore) ariaResponder() *AriaResponder {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.aria
}

// deliverRunToAriaChat posts a finished task run's report into the schedule's
// chosen chat. Runs on its own goroutine off recordRun; every outcome is
// stamped onto the run record so the reports reader shows where (and
// whether) the report went. All the "which agent, which key, which gateway"
// logic lives behind AriaResponder.SendAsAgent — this code never sees the
// agent config shape.
func (s *ScheduleStore) deliverRunToAriaChat(schedID string, run ScheduleRun, name string, ref AriaChatRef) {
	aria := s.ariaResponder()
	if aria == nil {
		// Delivery configured but the responder isn't wired yet (a fire in
		// the boot window). Loud, not silent — the user picked chat delivery
		// precisely because they're not watching the app.
		s.deliveryFailed(schedID, run, name, ref, "the Aria responder isn't ready yet — try again after the daemon finishes starting")
		return
	}
	text := composeAriaReport(name, run, s.readRunLog(schedID, run))
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := aria.SendAsAgent(ctx, ref.AgentID, ref.Source, ref.ChatID, text); err != nil {
		s.deliveryFailed(schedID, run, name, ref, err.Error())
		return
	}
	log.Printf("schedule %s: report delivered to %s", schedID, ref.label())
	s.stampRunDetail(schedID, run.At, "chat ✓ "+ref.label())
}

// deliveryFailed makes an undelivered report LOUD: stamped on the run and
// pushed (the user chose chat delivery precisely because they're not
// watching the app — a silent failure defeats the point).
func (s *ScheduleStore) deliveryFailed(schedID string, run ScheduleRun, name string, ref AriaChatRef, reason string) {
	log.Printf("schedule %s: chat delivery to %s failed: %s", schedID, ref.label(), reason)
	s.stampRunDetail(schedID, run.At, "chat ✗ "+ref.label())
	s.mu.Lock()
	pusher := s.pusher
	s.mu.Unlock()
	if pusher != nil {
		pusher.FireSchedule(schedID, name, "schedule-error",
			fmt.Sprintf("report couldn't be delivered to %s: %s", ref.label(), ariaTruncate([]byte(reason), 200)))
	}
}

// readRunLog pulls the run's full report text (best-effort — Summary is the
// fallback when the log is missing or oversized on read).
func (s *ScheduleStore) readRunLog(schedID string, run ScheduleRun) string {
	if run.LogFile == "" {
		return ""
	}
	b, err := os.ReadFile(filepath.Join(s.taskRunDir(schedID), filepath.Base(run.LogFile)))
	if err != nil {
		return ""
	}
	return string(b)
}

const ariaReportTruncNote = "\n\n…(truncated — the full report is in soluterminal → Inbox)"

// composeAriaReport shapes a run into one chat message under the gateway's
// text cap, with an explicit truncation note instead of a silent cut.
func composeAriaReport(name string, run ScheduleRun, logText string) string {
	body := strings.TrimSpace(logText)
	if body == "" {
		body = strings.TrimSpace(run.Summary)
	}
	if body == "" {
		body = strings.TrimSpace(run.Detail)
	}
	if body == "" {
		body = "(the run produced no output)"
	}
	// Schedule names are unbounded (validate() never caps them), so a
	// pathological name could eat the whole budget — clamp it before it can
	// drive `cut` negative and panic body[:cut].
	header := "📋 " + name + " — " + run.At.Local().Format("Mon 15:04") + "\n\n"
	if len(header) > ariaSendMaxChars-len(ariaReportTruncNote) {
		return truncateRunes(header, ariaSendMaxChars, ariaReportTruncNote)
	}
	budget := ariaSendMaxChars - len(header)
	if len(body) > budget {
		body = truncateRunes(body, budget, ariaReportTruncNote)
	}
	return header + body
}

// stampRunDetail appends a note to an existing run record (matched by
// timestamp) — how async delivery outcomes reach the reports reader.
func (s *ScheduleStore) stampRunDetail(id string, at time.Time, note string) {
	s.mu.Lock()
	sched, ok := s.items[id]
	if ok {
		for i := range sched.Runs {
			if sched.Runs[i].At.Equal(at) {
				if sched.Runs[i].Detail != "" {
					sched.Runs[i].Detail += " · "
				}
				sched.Runs[i].Detail += note
				break
			}
		}
		_ = s.saveUnlocked()
	}
	s.mu.Unlock()
	if ok {
		s.notifyChanged()
	}
}

// ---------------------------------------------------------------------------
// GET /api/aria/responder/chats — the schedule dialog's chat picker source
// ---------------------------------------------------------------------------

// getAriaResponderChats lists every configured agent's granted chats live
// from the gateway (grants change Aria-side; a cache would show ghosts). The
// fan-out lives on AriaResponder so this handler stays thin.
func (a *apiServer) getAriaResponderChats(w http.ResponseWriter, r *http.Request) {
	if a.ariaResponder == nil {
		http.Error(w, "aria responder unavailable", http.StatusServiceUnavailable)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Agents []ariaAgentChatsDTO `json:"agents"`
	}{Agents: a.ariaResponder.ListAgentChats(r.Context())})
}
