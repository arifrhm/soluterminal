package main

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestAutoPromptRawOutputHint(t *testing.T) {
	for _, tc := range []struct {
		name string
		text string
		want bool
	}{
		{"approval", "Do you want to proceed?\n❯ 1. Yes", true},
		{"ambiguous numbered menu", "Choose one:\n1. Open\n2. Exit", true},
		{"split payload retained in tail", "Do you want to proceed?\n\x1b[0m1. Yes", true},
		{"ordinary prose question", "Can you explain this?", false},
		{"ordinary numbered output", "1. Compile package", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := autoPromptRawOutputHint([]byte(tc.text)); got != tc.want {
				t.Fatalf("autoPromptRawOutputHint(%q) = %t, want %t", tc.text, got, tc.want)
			}
		})
	}
}

func TestAriaWatcherFastWakeDistinctMenuBypassesDebounce(t *testing.T) {
	menu := "Do you want to proceed?\n❯ 1. Yes\n  2. No\n"
	p := testPaneWithTail(t, "pane-fast-wake", "Fast wake", menu)
	p.AutoMode = "always"
	mgr := testManagerWithPane(p)
	bus := newUIBus()
	events := bus.subscribe()
	defer bus.unsubscribe(events)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go startAriaWatcher(ctx, mgr, bus, nil)

	mgr.enqueueAutoPromptWake(p.ID)
	waitFastObservation(t, events)
	firstPos := atomic.LoadInt64(&p.LastAckedChoicePromptPos)
	if firstPos <= 0 {
		t.Fatalf("first menu position = %d", firstPos)
	}

	// A redraw of the same menu is a wake hint, not another approval.
	mgr.enqueueAutoPromptWake(p.ID)
	assertNoFastObservation(t, events)

	// The pane stays in choice-prompt, so the activity watcher would not
	// publish a state edge. The new menu must still wake ARIA immediately,
	// even though less than the old 2s debounce has elapsed.
	next := "\nDo you want to run the next command?\n❯ 1. Yes\n  2. No\n"
	if _, err := p.buffer.Write([]byte(next)); err != nil {
		t.Fatal(err)
	}
	atomic.AddInt64(&p.TotalStrippedChars, int64(len(stripANSI([]byte(next)))))
	mgr.enqueueAutoPromptWake(p.ID)
	waitFastObservation(t, events)
	if pos := atomic.LoadInt64(&p.LastAckedChoicePromptPos); pos <= firstPos {
		t.Fatalf("new menu position = %d, first = %d", pos, firstPos)
	}
}

func TestAriaWatcherRawWakeRetainsBackToBackMenuWithinThrottle(t *testing.T) {
	menu := "Do you want to proceed?\n❯ 1. Yes\n  2. No\n"
	p := testPaneWithTail(t, "pane-raw-wake", "Raw wake", menu)
	p.AutoMode = "always"
	mgr := testManagerWithPane(p)
	bus := newUIBus()
	events := bus.subscribe()
	defer bus.unsubscribe(events)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go startAriaWatcher(ctx, mgr, bus, nil)

	p.scheduleRawAutoPromptWake(mgr)
	waitFastObservation(t, events)
	firstPos := atomic.LoadInt64(&p.LastAckedChoicePromptPos)

	// Menu B lands before the 500ms raw-output throttle expires. The old
	// scheduler dropped this hint and left B to the 2s/5s poll paths.
	next := "\nDo you want to run the next command?\n❯ 1. Yes\n  2. No\n"
	if _, err := p.buffer.Write([]byte(next)); err != nil {
		t.Fatal(err)
	}
	atomic.AddInt64(&p.TotalStrippedChars, int64(len(stripANSI([]byte(next)))))
	p.scheduleRawAutoPromptWake(mgr)
	waitFastObservation(t, events)
	if pos := atomic.LoadInt64(&p.LastAckedChoicePromptPos); pos <= firstPos {
		t.Fatalf("second raw menu position = %d, first = %d", pos, firstPos)
	}
}

func waitFastObservation(t *testing.T, events <-chan uiEvent) {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		select {
		case evt := <-events:
			if evt.Type == "aria-observation" {
				return
			}
		case <-deadline:
			t.Fatal("timed out waiting for fast ARIA observation")
		}
	}
}

func assertNoFastObservation(t *testing.T, events <-chan uiEvent) {
	t.Helper()
	timer := time.NewTimer(100 * time.Millisecond)
	defer timer.Stop()
	for {
		select {
		case evt := <-events:
			if evt.Type == "aria-observation" {
				t.Fatalf("duplicate fast observation for unchanged menu: %s", strings.TrimSpace(evt.Text))
			}
		case <-timer.C:
			return
		}
	}
}
