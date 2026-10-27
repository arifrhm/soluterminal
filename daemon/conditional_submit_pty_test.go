package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"soluterminal/grid"
)

func TestConditionalSubmitRejectsComposerShortcuts(t *testing.T) {
	for _, text := range []string{"! printf unsafe", " /logout", "# remember this", "? help", "@file", "Read @pending", "Read @"} {
		t.Run(text, func(t *testing.T) {
			_, _, conn, req, mux := conditionalFixture(t)
			req.Text = text
			rec, result := conditionalRequest(t, mux, "POST", "/api/panes/conditional-pane/conditional-submit", req)
			if rec.Code != 400 || result["outcome"] != "not_sent" || len(conn.data()) != 0 {
				t.Fatal("shortcut admitted as a model prompt", rec.Code, result)
			}
		})
	}
}

func TestConditionalSubmitReservesPasteGapBeforeBody(t *testing.T) {
	a, p, conn, req, _ := conditionalFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 70*time.Millisecond)
	defer cancel()
	result := a.runConditionalSubmit(ctx, p.ID, &req)
	if result.outcome != "not_sent" || result.reason != "deadline_exceeded" || len(conn.data()) != 0 || len(p.promptRec.buf) != 0 {
		t.Fatal("guaranteed incomplete paste wrote a body", result)
	}
}

func TestConditionalSubmitGapCannotDisablePasteSeparation(t *testing.T) {
	for _, provider := range []string{"codex", "claude"} {
		minimum := EnterGapBracketedPaste
		if provider == "claude" {
			minimum = EnterGapClaude
		}
		for _, configured := range []time.Duration{0, minimum / 2, minimum, 800 * time.Millisecond} {
			got := conditionalSubmitGap(provider, configured)
			want := configured
			if want < minimum {
				want = minimum
			}
			if got != want {
				t.Fatal(provider, configured, got)
			}
		}
	}
}

func conditionalClaudeReplay(t *testing.T, p *Pane, screen string) {
	t.Helper()
	if p.replayEmu == nil {
		p.replayEmu = grid.New(120, 35)
		t.Cleanup(func() { _ = p.replayEmu.Close() })
	}
	p.replayEmu.Feed([]byte("\x1b[2J\x1b[H" + screen))
	atomic.StoreInt64(&p.LastActivityMs, time.Now().UnixMilli())
}

func TestConditionalSubmitClaudeLegacyEmptyDoesNotPanic(t *testing.T) {
	defer func() {
		if failure := recover(); failure != nil {
			t.Fatal("legacy empty/truncated composer panicked", failure)
		}
	}()
	_, _, known := conditionalClaudeComposer("│ > \n╰────╯\n")
	if known {
		t.Fatal("truncated legacy layout should fail closed")
	}
	_, draft, known := conditionalClaudeComposer("│ > │\n╰────╯\n")
	if !known || draft != "" {
		t.Fatal("known complete empty legacy layout refused", known, draft)
	}
}

func TestConditionalSubmitClaudeWindowTitleIsNotAComposerDraft(t *testing.T) {
	const footer = "\r\n ~ [Opus 4.8 (1M context) | ctx: 3%]\r\n"
	for _, draft := range []string{"", "operator draft"} {
		t.Run(draft, func(t *testing.T) {
			a, p, conn, _ := conditionalProviderFixture(t, "claude")
			conditionalClaudeReplay(t, p, "Previous response\r\n✻ Brewed for 1s\r\n❯ "+draft+footer)
			// Actual Claude updates its title with the cursor after ❯. Both
			// glyphs contain UTF8 0x9c, which must not terminate OSC early.
			p.replayEmu.Feed([]byte("\x1b[3;3H\x1b]0;✳ hidden-window-title\a\x1b]0;✓ hidden-window-title\a"))
			atomic.StoreInt64(&p.LastActivityMs, 0)
			result := a.runConditionalSubmit(context.Background(), p.ID, nil)
			if draft == "" && result.outcome != "ready" {
				t.Fatal("hidden window title made an empty composer non-idle", result)
			}
			if draft != "" && result.outcome != "not_sent" {
				t.Fatal("real operator draft must still be refused", result)
			}
			if len(conn.data()) != 0 {
				t.Fatal("read-only preflight unexpectedly sent input")
			}
		})
	}
}

func TestConditionalSubmitClaudeEchoAndTranscriptBoundary(t *testing.T) {
	const footer = "\r\n ~ [Opus 4.8 (1M context) | ctx: 3%]\r\n"
	for _, mode := range []string{"same_no_marker", "scrolled_no_marker", "new_streaming", "empty_echo", "foreign_echo"} {
		t.Run(mode, func(t *testing.T) {
			a, p, conn, req := conditionalProviderFixture(t, "claude")
			before := "Older transcript\r\nPrevious response\r\n"
			if mode == "empty_echo" {
				before += "✻ Brewed for 1s\r\n"
			}
			conditionalClaudeReplay(t, p, before+"❯"+footer)
			atomic.StoreInt64(&p.LastActivityMs, 0) // old idle transcript, no done marker
			a.conditionalSubmitEnterWait = func(context.Context, time.Duration) error {
				transcript, echo := before, req.Text
				switch mode {
				case "scrolled_no_marker":
					transcript = "Previous response\r\n"
				case "new_streaming":
					transcript += "New assistant streaming output\r\n"
				case "empty_echo":
					echo = ""
				case "foreign_echo":
					echo = "Different operator text"
				}
				conditionalClaudeReplay(t, p, transcript+"❯ "+echo+footer)
				return nil
			}
			result := a.runConditionalSubmit(context.Background(), p.ID, &req)
			if mode == "same_no_marker" || mode == "scrolled_no_marker" {
				if result.outcome != "transport_written" {
					t.Fatal("own echo makes unchanged idle transcript look active", result)
				}
			} else {
				if result.outcome != "unknown" || len(p.LastPrompts) != 0 {
					t.Fatal("unobserved body or changed transcript allowed Enter", result)
				}
				reader := bytes.NewReader(conn.data())
				_, _, err := ReadFrame(reader)
				if err != nil || reader.Len() != 0 {
					t.Fatal("Enter crossed an unconfirmed echo/transcript boundary", err)
				}
			}
		})
	}
}

func TestConditionalSubmitClaudeRenderedDraftAndOwnedWrappedEcho(t *testing.T) {
	const before = "Previous response\r\n✻ Brewed for 1s\r\n"
	const footer = "\r\n ~ [Opus 4.8 (1M context) | ctx: 3%]\r\n"
	for _, draft := range []string{"operator draft", "operator\r\ndraft continuation"} {
		t.Run("reject_"+draft, func(t *testing.T) {
			a, p, conn, req := conditionalProviderFixture(t, "claude")
			conditionalClaudeReplay(t, p, before+"❯ "+draft+footer)
			for _, candidate := range []*conditionalSubmitRequest{nil, &req} {
				result := a.runConditionalSubmit(context.Background(), p.ID, candidate)
				if result.outcome != "not_sent" || len(conn.data()) != 0 {
					t.Fatal("rendered Claude draft admitted after recorder reset", result)
				}
			}
		})
	}
	a, p, _, req := conditionalProviderFixture(t, "claude")
	conditionalClaudeReplay(t, p, before+"❯"+footer)
	a.conditionalSubmitEnterWait = func(context.Context, time.Duration) error {
		// A long single-line transport body wraps in the real rendered grid.
		conditionalClaudeReplay(t, p, before+"❯ RECORDED_HANDOFF\r\n fixture"+footer)
		return nil
	}
	result := a.runConditionalSubmit(context.Background(), p.ID, &req)
	if result.outcome != "transport_written" || len(p.LastPrompts) != 1 {
		t.Fatal("our own wrapped echo misclassified as a streaming turn", result)
	}
}

func TestConditionalSubmitHoldsInputLeaseAcrossPasteGap(t *testing.T) {
	a, p, conn, req, _ := conditionalFixture(t)
	entered, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	defer unblock()
	a.conditionalSubmitEnterWait = func(ctx context.Context, _ time.Duration) error {
		close(entered)
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	finished := make(chan conditionalSubmitResult, 1)
	go func() { finished <- a.runConditionalSubmit(context.Background(), p.ID, &req) }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("paste gap never started")
	}
	ordinary := make(chan error, 1)
	go func() { _, err := p.Write([]byte("operator")); ordinary <- err }()
	select {
	case err := <-ordinary:
		t.Fatal("ordinary input crossed held paste gap", err)
	case <-time.After(20 * time.Millisecond):
	}
	unblock()
	select {
	case result := <-finished:
		if result.outcome != "transport_written" {
			t.Fatal(result)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("submit stuck")
	}
	select {
	case err := <-ordinary:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("ordinary input stuck")
	}
	reader := bytes.NewReader(conn.data())
	for _, want := range []string{"\x1b[200~" + req.Text + "\x1b[201~", "\r", "operator"} {
		_, payload, err := ReadFrame(reader)
		if err != nil || string(payload) != want {
			t.Fatal(string(payload), err)
		}
	}
	if reader.Len() != 0 {
		t.Fatal("extra frames")
	}
}

func conditionalProviderFixture(t *testing.T, provider string) (*apiServer, *Pane, *conditionalTestConn, conditionalSubmitRequest) {
	t.Helper()
	a, p, conn, req, _ := conditionalFixture(t)
	if provider == "claude" {
		p.Agent, p.ClaudeSessionID = agentClaude, "claude-fixture-session"
		p.buffer = newTestRing()
		p.buffer.Write([]byte("❯\n ~ [Opus 4.8 (1M context) | ctx: 3%]\n"))
		original := a.conditionalSubmitProbe
		a.conditionalSubmitProbe = func(ctx context.Context, pid int) (conditionalSubmitProcess, error) {
			process, err := original(ctx, pid)
			process.Provider = provider
			return process, err
		}
		ready := a.runConditionalSubmit(context.Background(), p.ID, nil)
		if ready.outcome != "ready" {
			t.Fatal(ready)
		}
		req.conditionalSubmitCondition = ready.condition
	}
	return a, p, conn, req
}

func TestConditionalSubmitStopsEnterAfterBodyBoundaryChanges(t *testing.T) {
	for _, mode := range []string{"cancel", "deadline", "foreground_exit", "foreground_restart", "cwd", "session", "connection", "closing", "migrating", "respawning", "busy", "permission", "second_frame_error", "second_frame_short"} {
		t.Run(mode, func(t *testing.T) {
			a, p, conn, req, _ := conditionalFixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			a.conditionalSubmitEnterWait = func(context.Context, time.Duration) error {
				switch mode {
				case "cancel":
					cancel()
					return ctx.Err()
				case "deadline":
					deadlineCtx, deadlineCancel := context.WithTimeout(ctx, 10*time.Millisecond)
					defer deadlineCancel()
					return waitConditionalSubmitEnter(deadlineCtx, time.Second)
				case "foreground_exit":
					a.conditionalSubmitProbe = func(context.Context, int) (conditionalSubmitProcess, error) {
						return conditionalSubmitProcess{}, errConditionalProcessUnavailable
					}
				case "foreground_restart":
					original := a.conditionalSubmitProbe
					a.conditionalSubmitProbe = func(ctx context.Context, pid int) (conditionalSubmitProcess, error) {
						process, err := original(ctx, pid)
						process.StartTime = "99999"
						return process, err
					}
				case "cwd":
					original := a.conditionalSubmitProbe
					a.conditionalSubmitProbe = func(ctx context.Context, pid int) (conditionalSubmitProcess, error) {
						process, err := original(ctx, pid)
						process.Cwd += "/changed"
						return process, err
					}
				case "session":
					p.SetCodexThreadID("changed-after-body")
				case "connection":
					p.mu.Lock()
					p.agentConn = &conditionalTestConn{}
					p.mu.Unlock()
				case "closing":
					p.mu.Lock()
					p.userClosing = true
					p.mu.Unlock()
				case "migrating":
					p.codexLiveMigrating.Store(true)
				case "respawning":
					p.respawning.Store(true)
				case "busy":
					p.buffer.Write([]byte("Working (1s · esc to interrupt)\n"))
				case "permission":
					p.buffer.Write([]byte("❯ 1. Allow\n  2. Deny\n"))
				case "second_frame_error":
					conn.writeError = errors.New("second frame failed")
				case "second_frame_short":
					conn.short = true
				}
				return nil
			}
			result := a.runConditionalSubmit(ctx, p.ID, &req)
			if result.outcome != "unknown" || result.status != 502 || len(p.LastPrompts) != 0 || p.fleetTurnAdmitted.Load() || len(p.promptRec.buf) == 0 {
				t.Fatal("body-only delivery was hidden, committed, or declared not_sent", result, p.LastPrompts)
			}
			reader := bytes.NewReader(conn.data())
			_, body, err := ReadFrame(reader)
			if err != nil || string(body) != "\x1b[200~"+req.Text+"\x1b[201~" {
				t.Fatal("successful body was not retained", err)
			}
			if mode != "second_frame_short" && reader.Len() != 0 {
				t.Fatal("Enter crossed a changed boundary")
			}
			if strings.HasPrefix(mode, "second_frame_") && !conn.closed {
				t.Fatal("partial transport left usable")
			}
		})
	}
}

func TestConditionalSubmitBodyEchoIsNotTurnProgress(t *testing.T) {
	a, p, conn, req, _ := conditionalFixture(t)
	conn.beforeWrite = func() {
		if len(conn.data()) < 5+len("\x1b[200~"+req.Text+"\x1b[201~") {
			atomic.AddInt64(&p.TotalStrippedChars, 20)
		}
	}
	result := a.runConditionalSubmit(context.Background(), p.ID, &req)
	if result.outcome != "transport_written" || p.fleetTurnOutputWatermark.Load() != atomic.LoadInt64(&p.TotalStrippedChars) || conditionalPromptIdle(context.Background(), p, "codex") {
		t.Fatal("body echo falsely satisfied post-Enter progress", result)
	}
}

func TestConditionalSubmitSeparatesPTYBodyAndEnter(t *testing.T) {
	for _, provider := range []string{"codex", "claude"} {
		t.Run(provider, func(t *testing.T) {
			a, p, conn, req := conditionalProviderFixture(t, provider)
			result := a.runConditionalSubmit(context.Background(), p.ID, &req)
			if result.outcome != "transport_written" || result.bytes != len(req.Text)+1 {
				t.Fatal(result)
			}
			reader := bytes.NewReader(conn.data())
			kind, body, err := ReadFrame(reader)
			want := req.Text
			if provider == "codex" {
				want = "\x1b[200~" + want + "\x1b[201~"
			}
			if err != nil || kind != FrameData || string(body) != want {
				t.Fatalf("body must be its own PTY frame, not fused with Enter: %q; %v", body, err)
			}
			kind, enter, err := ReadFrame(reader)
			if err != nil || kind != FrameData || string(enter) != "\r" || reader.Len() != 0 {
				t.Fatalf("separate Enter missing: %q; %v", enter, err)
			}
			if len(conn.writesAt) != 4 || conn.writesAt[2].Sub(conn.writesAt[1]) < EnterGap(provider == "claude") {
				t.Fatal("Enter arrived inside the provider's paste-burst window", conn.writesAt)
			}
		})
	}
}

func TestConditionalSubmitRejectsRenderedDraftAfterRecorderReset(t *testing.T) {
	for _, screen := range []string{
		"› RECONFIRM_READY_20261001\n  do not start work\n\n Context 6% used · GPT-6-Sol xhigh · /fixture\n",
		"›\nsecond draft line\n gpt-6-sol · /fixture\n",
	} {
		a, p, conn, req, _ := conditionalFixture(t)
		p.buffer = newTestRing()
		p.buffer.Write([]byte(screen))
		// A fused body+CR made the recorder think this was committed, while
		// the real TUI retained it. Screen evidence must independently refuse.
		if len(p.promptRec.buf) != 0 {
			t.Fatal("fixture recorder should be empty")
		}
		for _, candidate := range []*conditionalSubmitRequest{nil, &req} {
			result := a.runConditionalSubmit(context.Background(), p.ID, candidate)
			if result.outcome != "not_sent" || len(conn.data()) != 0 {
				t.Fatal("rendered draft incorrectly admitted", result)
			}
		}
	}
}
