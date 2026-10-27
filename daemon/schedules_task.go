package main

// Task-kind schedules: the Schedules engine's third work kind, and the one
// every user gets. On fire it runs a FRESH headless Claude session
// (`claude -p "<prompt>"`) in a chosen directory, captures the transcript as
// the run's log, and records the exit status.
//
// Why not just "prompt into a pane" (the original kind)? That path needs a
// pane to be alive AND idle at fire time — the #1 recorded skip reason — it
// interleaves with whatever the human is doing in that pane, and it can't run
// on a headless box. A task run has none of those constraints: it is
// self-contained, parallel-safe, and observable after the fact.
//
// Security stance: `cwd` and `prompt` come from the local API, but so does
// `POST /api/panes`'s arbitrary `cmd` — a task schedule therefore adds NO
// remote-exec authority beyond what the same-origin API already grants. The
// argv is built here, never accepted from the client: the caller picks a
// permission PRESET by name, not a flag string.
//
// Safety rails (mirroring schedules_agent.go, which has run in production):
//   • per-schedule single-flight — a slow run never stacks on itself;
//   • a global concurrency cap so N schedules firing at once can't fork-bomb;
//   • a hard timeout (default 30m, ceiling 4h) with a killed child tree;
//   • dry-run-first defaults: "plan" (read + plan, no writes) unless the user
//     explicitly opts up to "edit". bypassPermissions is NOT offered at all.

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"soluterminal-shared/providers"
)

const (
	taskRunTimeoutDefault = 30 * time.Minute
	taskRunTimeoutMax     = 4 * time.Hour
	// taskLogLimit caps captured output per run — same bound as agent runs.
	taskLogLimit = 256 * 1024
)

// taskPermission maps a UI preset to Claude's --permission-mode. Only these
// two are reachable: "plan" cannot write, and "edit" (acceptEdits) writes
// files but still refuses the destructive tool classes Claude gates. The
// client sends the preset NAME; the flag never crosses the API boundary.
var taskPermissionModes = map[string]string{
	"plan": "plan",
	"edit": "acceptEdits",
}

func validTaskPermission(p string) bool {
	_, ok := taskPermissionModes[p]
	return ok
}

// taskRunDir is where a task schedule's per-run logs live:
// <dataDir>/schedule-runs/<scheduleID>/. Agent runs use the agent's own home
// (see writeRunLog callers) — the runlog endpoint resolves both.
func (s *ScheduleStore) taskRunDir(schedID string) string {
	return filepath.Join(filepath.Dir(s.path), "schedule-runs", schedID)
}

// agentRunSlotKey namespaces a managed-agent run inside the taskRunning map.
// Schedule ids are sched_-prefixed, so an "agent:" key can never collide with
// one — which is what lets ONE map bound both kinds.
func agentRunSlotKey(agentID string) string { return "agent:" + agentID }

// acquireAgentRunSlot admits a managed-agent run against the SAME counter
// fireTask checks (docs/DESIGN-agents-surface.md §5.2, §5.3). An event storm
// of agent runs and a user's nightly task schedules draw from one configurable
// pool and one admission rule. Board-steward templates can take the bookkeeping
// reserve before the general pool, so a long general run cannot starve routine
// board maintenance.
//
// Returns the verdict plus the exact skip detail the agent's run record shows.
func (s *ScheduleStore) acquireAgentRunSlot(agentID string, bookkeeping bool) (bool, string) {
	if s == nil {
		return false, "schedules unavailable on this daemon"
	}
	key := agentRunSlotKey(agentID)
	s.mu.Lock()
	defer s.mu.Unlock()
	ok, detail := admitRunSlot(s.taskRunning, s.taskReserved, key, bookkeeping, s.runCapsUnlocked(), "agent")
	if ok && s.taskReserved[key] {
		general := len(s.taskRunning) - len(s.taskReserved)
		log.Printf("scheduler: %s admitted on the bookkeeping reserve (%d general runs in flight)", agentID, general)
	}
	return ok, detail
}

// releaseAgentRunSlot frees what acquireAgentRunSlot took.
func (s *ScheduleStore) releaseAgentRunSlot(agentID string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	releaseRunSlot(s.taskRunning, s.taskReserved, agentRunSlotKey(agentID))
	s.mu.Unlock()
}

// ownedRunHooks reads a schedule's owning agent and the gate to ask about it,
// under one lock acquisition. The gate is returned rather than called here: it
// reaches into the ManagedAgentStore, and this store's mutex must not be held across
// that hop.
func (s *ScheduleStore) ownedRunHooks(schedID string) (string, func(string) (bool, string)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sched, ok := s.items[schedID]
	if !ok {
		return "", nil
	}
	return sched.OwnedByAgentID, s.ownedRunGate
}

// fireTask is the task-kind counterpart of fire()/fireAgent(). Cheap checks
// run synchronously so skips are recorded in order; the spawn+wait detaches.
//
// This is the CLOCK's door: the runner's own cron fire, and any hand fire of a
// schedule the user can still see. An imported schedule's pause walls are asked
// about here because this path never passes runAgent.
func (s *ScheduleStore) fireTask(schedID, cwd, prompt, permission, model string, timeoutMin int) {
	s.fireTaskGated(schedID, cwd, prompt, permission, model, timeoutMin, true)
}

// fireTaskPreGated is the imported-agent door (runAgent's legacy branch,
// agents_run.go). runAgent has ALREADY applied the very walls the gate applies,
// with the one distinction the gate cannot see: "Run now" is an explicit human
// action and is exempt from pause on every card (§6.5, runAgent's
// `agent.Paused && !manual`). Re-asking here would swallow the run the user
// just clicked while the API answered "started" — so the decision is not taken
// twice.
//
// Everything past the gate is identical between the two doors; this is only
// about WHO already decided.
func (s *ScheduleStore) fireTaskPreGated(schedID, cwd, prompt, permission, model string, timeoutMin int) {
	s.fireTaskGated(schedID, cwd, prompt, permission, model, timeoutMin, false)
}

func (s *ScheduleStore) fireTaskGated(schedID, cwd, prompt, permission, model string, timeoutMin int, askOwner bool) {
	run := ScheduleRun{At: time.Now()}
	if providers.HasRetiredDeepSeekReference(model) {
		run.Status = "error"
		run.Detail = "retired DeepSeek model is not dispatchable"
		s.recordRun(schedID, run)
		return
	}
	// An IMPORTED schedule answers to its agent card's pause AND to the
	// daemon-wide "Pause all autonomy" (docs/DESIGN-agents-surface.md §6.5,
	// §8.3). This is the only place that can be true for its CRON fire: that
	// fire arrives here directly and never passes runAgent's gates, so without
	// it the one agent the user most wants to be able to stop would be the one
	// Pause all does not reach.
	//
	// Everything below this block — argv, stdin, logs, single-flight — is
	// untouched by the import, which is what makes the migration a no-op.
	if askOwner {
		if ownedBy, gate := s.ownedRunHooks(schedID); ownedBy != "" && gate != nil {
			if skip, detail := gate(ownedBy); skip {
				run.Status = "skipped"
				run.Detail = detail
				s.recordRun(schedID, run)
				return
			}
		}
	}
	if !validTaskPermission(permission) {
		run.Status = "error"
		run.Detail = "unknown permission preset: " + permission
		s.recordRun(schedID, run)
		return
	}
	if fi, err := os.Stat(cwd); err != nil || !fi.IsDir() {
		run.Status = "error"
		run.Detail = "working directory is gone: " + cwd
		s.recordRun(schedID, run)
		return
	}
	bin, err := resolveClaudeBin()
	if err != nil {
		run.Status = "error"
		run.Detail = err.Error()
		s.recordRun(schedID, run)
		return
	}

	s.mu.Lock()
	ok, detail := admitRunSlot(s.taskRunning, s.taskReserved, schedID, false, s.runCapsUnlocked(), "task")
	s.mu.Unlock()
	if !ok {
		run.Status = "skipped"
		run.Detail = detail
		s.recordRun(schedID, run)
		return
	}
	s.notifyChanged() // sidebar flips to "running…" immediately

	go s.runTaskProcess(schedID, bin, cwd, prompt, permission, model, timeoutMin)
}

// runTaskProcess resolves the timeout from the schedule's TimeoutMin and
// releases the single-flight slot when done.
func (s *ScheduleStore) runTaskProcess(schedID, bin, cwd, prompt, permission, model string, timeoutMin int) {
	defer func() {
		s.mu.Lock()
		releaseRunSlot(s.taskRunning, s.taskReserved, schedID)
		s.mu.Unlock()
		s.notifyChanged()
	}()

	timeout := taskRunTimeoutDefault
	if timeoutMin > 0 {
		timeout = time.Duration(timeoutMin) * time.Minute
		if timeout > taskRunTimeoutMax {
			timeout = taskRunTimeoutMax
		}
	}
	s.runTaskProcessWithTimeout(schedID, bin, cwd, prompt, permission, model, timeout)
}

// runTaskProcessWithTimeout is the spawn+wait body. Split from runTaskProcess
// so tests can drive sub-minute timeouts (TimeoutMin's unit is minutes).
// Does NOT touch the single-flight slot — its caller owns that.
func (s *ScheduleStore) runTaskProcessWithTimeout(schedID, bin, cwd, prompt, permission, model string, timeout time.Duration) {
	run := ScheduleRun{At: time.Now()}

	// Headless, non-interactive. --permission-mode is preset-derived (never
	// client-supplied). Plain text output: the log IS the deliverable a human
	// reads later, so stream-json's frame soup would be a downgrade here.
	//
	// The prompt goes on STDIN, not as `-p <prompt>`. Claude's `-p/--print`
	// takes an OPTIONAL value, so its parser refuses a value beginning with a
	// dash: a task whose prompt starts with "--" (or "-n", …) dies with
	// "error: unknown option". Verified against the installed claude CLI.
	// Stdin sidesteps argv parsing entirely and is the canonical headless form.
	args := []string{
		"-p",
		"--permission-mode", taskPermissionModes[permission],
		"--output-format", "text",
	}
	if model != "" {
		args = append(args, "--model", model)
	}
	cmd := exec.Command(bin, args...)
	cmd.Dir = cwd
	cmd.Env = providerChildEnvForBin(bin, withResolvedNodeBin(os.Environ(), resolveNodeBin))
	cmd.Stdin = strings.NewReader(prompt + reportStyleHint)
	applyDetached(cmd) // own session: survives a daemon restart, killable as a tree
	out := &boundedBuf{limit: taskLogLimit}
	cmd.Stdout = out
	cmd.Stderr = out

	start := time.Now()
	if err := startWithoutCoreDumps(cmd); err != nil {
		run.Status = "error"
		run.Detail = "spawn failed: " + err.Error()
		s.recordRun(schedID, run)
		return
	}
	log.Printf("scheduler: task %s started (perm=%s pid=%d cwd=%s)", schedID, permission, cmd.Process.Pid, cwd)
	kill := time.AfterFunc(timeout, func() { _ = killChildTree(cmd.Process) })
	werr := waitOwnedCommand(cmd)
	timedOut := !kill.Stop()

	// The header is the ONE difference an import makes to a run log
	// (docs/DESIGN-agents-surface.md §9 S8 AC): an owned schedule's header
	// gains an `· agent=agt_…` segment naming the card the run also lands on.
	// Argv, stdin, the log directory, the output cap and the kill behavior are
	// all identical to a pre-import run, which is what the equivalence check
	// rests on.
	header := fmt.Sprintf("# scheduled task · permission=%s · cwd=%s · started=%s\n# prompt: %s\n\n",
		permission, cwd, start.Format(time.RFC3339), prompt)
	if sched, ok := s.Get(schedID); ok && sched.OwnedByAgentID != "" {
		header = fmt.Sprintf("# scheduled task · permission=%s · cwd=%s · agent=%s · started=%s\n# prompt: %s\n\n",
			permission, cwd, sched.OwnedByAgentID, start.Format(time.RFC3339), prompt)
	}
	run.LogFile = writeRunLog(s.taskRunDir(schedID), start, append([]byte(header), out.buf...))

	dur := time.Since(start).Round(time.Second)
	switch {
	case timedOut:
		run.Status = "error"
		run.Detail = fmt.Sprintf("timed out after %s — killed", timeout)
		run.Summary = "failed: task timed out"
	case werr == nil:
		run.Status = "fired"
		run.Detail = fmt.Sprintf("%s · finished in %s", permission, dur)
		run.Summary = taskOutputPreview(out.buf)
	default:
		run.Status = "error"
		code := -1
		if ee, ok := werr.(*exec.ExitError); ok {
			code = ee.ExitCode()
		}
		run.Detail = fmt.Sprintf("%s · exit %d in %s", permission, code, dur)
		if line := lastErrorLine(out.buf); line != "" {
			run.Summary = "failed: " + line
		} else {
			run.Summary = "failed"
		}
	}
	s.recordRun(schedID, run)
	log.Printf("scheduler: task %s finished: %s", schedID, run.Detail)

	if s.uiBus != nil {
		s.uiBus.publish(uiEvent{Type: "schedule-fired", Text: schedID})
		icon := "🤖"
		if run.Status == "error" {
			icon = "⚠️"
		}
		if sched, ok := s.Get(schedID); ok {
			// Task run-results live in the Inbox (reports feed); keep them out
			// of the Alerts "Recent" log to avoid duplicating that surface.
			s.uiBus.publish(uiEvent{Type: "toast", Text: fmt.Sprintf("%s %s: %s", icon, sched.Name, run.Detail), SkipRecent: true})
		}
	}
}

// taskRunningFor reports whether a task schedule has a run in flight (the DTO's
// agentRunning flag doubles for both spawned kinds — the UI just shows "running…").
func (s *ScheduleStore) taskRunningFor(schedID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.taskRunning[schedID]
}

// reportStyleHint rides every task prompt on stdin. Task output renders in
// the web reader's markdown surface, and "## Needs you" first is the
// product's report contract — enforced at RUN time (not create time) so
// schedules created before the contract, or via MCP/API with bare prompts,
// produce structured reports too. Formatting-only guidance: it never
// changes WHAT the task does, and the run-log header still records the
// user's own prompt verbatim.
const reportStyleHint = "\n\n(Format your reply as a markdown report. If anything needs" +
	" the user's decision or action, open with a \"## Needs you\" section;" +
	" otherwise open with the line \"Nothing needs you today.\" Short ##" +
	" sections after that; tables and checklists welcome.)"

// taskOutputPreview turns raw task output into a push-notification body:
// the opening of the report, capped so the OS banner shows a coherent
// lede rather than a mid-word chop. Newlines are kept — Claude's reports
// front-load their point, and Android renders multi-line bodies.
const taskPreviewMaxRunes = 280

func taskOutputPreview(out []byte) string {
	text := strings.TrimSpace(string(out))
	runes := []rune(text)
	if len(runes) <= taskPreviewMaxRunes {
		return text
	}
	cut := runes[:taskPreviewMaxRunes]
	// Back up to the last whitespace so we never split a word (unless the
	// text is one giant token, in which case a hard chop is all there is).
	for i := len(cut) - 1; i > taskPreviewMaxRunes/2; i-- {
		if cut[i] == ' ' || cut[i] == '\n' || cut[i] == '\t' {
			cut = cut[:i]
			break
		}
	}
	return strings.TrimSpace(string(cut)) + "…"
}

// lastErrorLine returns the last non-empty line of output, trimmed of whitespace
// and capped so an error summary never overflows UI notification boundaries.
func lastErrorLine(out []byte) string {
	text := strings.TrimSpace(string(out))
	if text == "" {
		return ""
	}
	lines := strings.Split(text, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line != "" {
			runes := []rune(line)
			if len(runes) > taskPreviewMaxRunes {
				return strings.TrimSpace(string(runes[:taskPreviewMaxRunes])) + "…"
			}
			return line
		}
	}
	return ""
}
