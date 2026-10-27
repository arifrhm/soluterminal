package main

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// Claude's AskUserQuestion takes a digit without Enter, and automation leaves
// question menus to the human. The hand-typed answer used to stay in the
// recorder as a "draft", holding the next approval for two minutes
// (2026-10-26: shinoque #5 05:19:07-05:20:57, launcher #1 06:05:42-06:07:38).
func TestHandAnsweredQuestionDoesNotHoldNextApproval(t *testing.T) {
	for name, steps := range map[string][]menuStep{
		"digit":         {{"1", ""}},
		"second option": {{"2", ""}},
		// The highlight leaves option 1, so only the dialog footer still says
		// a menu is open when the Enter arrives.
		"arrow then enter":   {{"\x1b[B", questionFocused(2, "")}, {"\r", ""}},
		"free text then esc": {{"3", questionFocused(3, "")}, {"use a flag", questionFocused(3, "use a flag")}, {"\x1b", ""}},
	} {
		t.Run(name, func(t *testing.T) {
			p, frames := newTypingHoldPane(t, "question-"+strings.ReplaceAll(name, " ", "-"))
			p.Cols, p.Rows = 160, 48
			a := testAriaWithPane(t, p)
			a.sweepStreaks = map[string]*sweepStreak{}
			a.evalLog = newAriaEvalLog()
			seedGridMenu(t, p, claudeAskUserQuestionWrappedScreen)
			if p.computeState() != StateChoicePrompt {
				t.Fatal("fixture is not a menu on screen")
			}
			typeMenuSteps(t, p, frames, steps)
			if pending := p.composerPendingLen(); pending != 0 {
				t.Fatalf("menu answer became a draft: %q", p.promptRec.buf)
			}
			atomic.StoreInt64(&p.LastInputMs, time.Now().Add(-ariaAutoTypingSettle-time.Second).UnixMilli())
			seedGridMenu(t, p, codexNarrowCommandMenu("git push"))
			a.directAutoFire(context.Background(), p.ID, p.Title)
			expectFrame(t, frames, "next approval without a two-minute hold", "\r")
		})
	}
}

// menuStep is a key and the screen the TUI paints after it ("" = composer).
type menuStep struct{ key, screen string }

const composerScreen = "● Thanks.\n\n❯ \n  ? for shortcuts"

func typeMenuSteps(t *testing.T, p *Pane, frames <-chan directAutoFrameRead, steps []menuStep) {
	t.Helper()
	for _, step := range steps {
		if _, err := p.Write([]byte(step.key)); err != nil {
			t.Fatal(err)
		}
		expectFrame(t, frames, "hand answer", step.key)
		screen := step.screen
		if screen == "" {
			screen = composerScreen
		}
		seedGridMenu(t, p, screen)
	}
}

// questionFocused repaints the question with the highlight on option n, and
// typed free text in its "Type something" row.
func questionFocused(n int, typed string) string {
	s := strings.Replace(claudeAskUserQuestionWrappedScreen, " ❯ 1. ", "   1. ", 1)
	switch n {
	case 2:
		s = strings.Replace(s, "   2. ", " ❯ 2. ", 1)
	case 3:
		row := "Type something."
		if typed != "" {
			row = typed
		}
		s = strings.Replace(s, "   3. Type something.", " ❯ 3. "+row, 1)
	}
	return s
}

// A draft typed before the menu keeps its protection and its age; neither a
// menu key nor an Enter inside the menu may submit, clear or renew it.
func TestHandAnsweredQuestionKeepsEarlierDraft(t *testing.T) {
	p, frames := newTypingHoldPane(t, "question-keeps-draft")
	p.Cols, p.Rows = 160, 48
	a := testAriaWithPane(t, p)
	a.sweepStreaks = map[string]*sweepStreak{}
	a.evalLog = newAriaEvalLog()
	if _, err := p.Write([]byte("real unfinished message")); err != nil {
		t.Fatal(err)
	}
	expectFrame(t, frames, "draft", "real unfinished message")
	_, draftAt := p.composerPendingSnapshot()
	seedGridMenu(t, p, claudeAskUserQuestionWrappedScreen)
	typeMenuSteps(t, p, frames, []menuStep{{"\x1b[B", questionFocused(2, "")}, {"\r", ""}})
	if pending, at := p.composerPendingSnapshot(); pending != len("real unfinished message") || at != draftAt {
		t.Fatalf("menu input changed the draft: pending=%d at=%d want %d", pending, at, draftAt)
	}
	if prompts := p.Snapshot().LastPrompts; len(prompts) != 0 {
		t.Fatalf("Enter in a menu submitted the composer draft: %q", prompts)
	}
	atomic.StoreInt64(&p.LastInputMs, time.Now().Add(-ariaAutoTypingSettle-time.Second).UnixMilli())
	seedGridMenu(t, p, codexNarrowCommandMenu("git push"))
	a.directAutoFire(context.Background(), p.ID, p.Title)
	expectNoFrame(t, frames, "real draft remains protected")
}

// After the menu goes, typing is composer text again and holds as before;
// the menu keys typed earlier are not part of it.
func TestDraftTypedAfterQuestionStillHolds(t *testing.T) {
	p, frames := newTypingHoldPane(t, "question-then-draft")
	p.Cols, p.Rows = 160, 48
	seedGridMenu(t, p, claudeAskUserQuestionWrappedScreen)
	if _, err := p.Write([]byte("1")); err != nil {
		t.Fatal(err)
	}
	expectFrame(t, frames, "hand answer", "1")
	seedGridMenu(t, p, "● Thanks, shipping behind a flag.\n\n❯ \n  ? for shortcuts")
	if p.computeState() == StateChoicePrompt {
		t.Fatal("composer screen still reads as a menu")
	}
	if _, err := p.Write([]byte("next request")); err != nil {
		t.Fatal(err)
	}
	expectFrame(t, frames, "draft", "next request")
	if got := string(p.promptRec.buf); got != "next request" {
		t.Fatalf("draft = %q, want the composer text only", got)
	}
	if held, reason := autoFireUserHold(p, time.Now().Add(ariaAutoTypingSettle+time.Second)); !held || !strings.HasPrefix(reason, "composer has text (") {
		t.Fatalf("real draft lost protection: held=%v reason=%q", held, reason)
	}
}

// Composer text can look like a menu row ("❯ 1. fix the bug"). It never
// entered a menu, so it stays a draft that holds the next approval.
func TestListLikeComposerTextStillHolds(t *testing.T) {
	p, frames := newTypingHoldPane(t, "list-like-draft")
	p.Cols, p.Rows = 160, 48
	seedGridMenu(t, p, "● Done.\n\n❯ \n  ? for shortcuts")
	for _, key := range []string{"1", ". fix the bug"} {
		if _, err := p.Write([]byte(key)); err != nil {
			t.Fatal(err)
		}
		expectFrame(t, frames, "composer typing", key)
		seedGridMenu(t, p, "● Done.\n\n────────────\n❯ 1"+strings.TrimPrefix(key, "1")+"\n────────────\n  ? for shortcuts")
	}
	if got := string(p.promptRec.buf); got != "1. fix the bug" || p.composerPendingLen() != len(got) {
		t.Fatalf("list-like draft lost: buf=%q pending=%d", got, p.composerPendingLen())
	}
	if held, reason := autoFireUserHold(p, time.Now().Add(ariaAutoTypingSettle+time.Second)); !held || !strings.HasPrefix(reason, "composer has text (") {
		t.Fatalf("list-like draft lost protection: held=%v reason=%q", held, reason)
	}
}

// After a fast answer the recorder may still be in the menu. Typing on a
// screen without a menu footer is composer text again, even when a composer
// line looks like a menu row.
func TestTypingAfterAnswerOnListLikeComposerHolds(t *testing.T) {
	p, frames := newTypingHoldPane(t, "answer-then-list-like")
	p.Cols, p.Rows = 160, 48
	seedGridMenu(t, p, claudeAskUserQuestionWrappedScreen)
	typeMenuSteps(t, p, frames, []menuStep{{"1", "● Done.\n\n────────────\n❯ 1\n────────────\n  ? for shortcuts"}})
	if _, err := p.Write([]byte(". fix the bug")); err != nil {
		t.Fatal(err)
	}
	expectFrame(t, frames, "composer typing", ". fix the bug")
	if got := string(p.promptRec.buf); got != ". fix the bug" || p.composerPendingLen() != len(got) {
		t.Fatalf("composer text after the answer was not a draft: buf=%q pending=%d", got, p.composerPendingLen())
	}
}

func TestMenuDialogFooterOnScreen(t *testing.T) {
	for screen, want := range map[string]bool{
		claudeAskUserQuestionWrappedScreen:    true,
		questionFocused(3, "use a flag"):      true,
		codexNarrowCommandMenu("git push"):    true,
		composerScreen:                        false,
		"❯ 1. fix the bug\n  ? for shortcuts": false,
		"● Working… (esc to interrupt)":       false,
	} {
		if got := menuDialogFooterOnScreen(screen); got != want {
			t.Errorf("menuDialogFooterOnScreen(%q) = %v, want %v", screen[:min(len(screen), 40)], got, want)
		}
	}
}
