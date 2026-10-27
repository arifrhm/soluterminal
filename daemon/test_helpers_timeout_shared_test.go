package main

import (
	"sync"
	"testing"
	"time"
)

func newWfFakeClock() *fakeClockSource {
	return &fakeClockSource{now: time.Date(2026, 8, 10, 0, 0, 0, 0, time.UTC)}
}

// fakeClockSource is a manually-advanced clock: After registers a pending timer,
// Advance moves now and fires everything due. No real waiting anywhere.
type fakeClockSource struct {
	mu     sync.Mutex
	now    time.Time
	timers []*fakeTimer
}
type fakeTimer struct {
	at time.Time
	ch chan time.Time
}

func (c *fakeClockSource) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}
func (c *fakeClockSource) After(d time.Duration) <-chan time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	t := &fakeTimer{at: c.now.Add(d), ch: make(chan time.Time, 1)}
	if d <= 0 {
		t.ch <- c.now
		return t.ch
	}
	c.timers = append(c.timers, t)
	return t.ch
}
func (c *fakeClockSource) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	var pending []*fakeTimer
	var fire []*fakeTimer
	for _, t := range c.timers {
		if !t.at.After(c.now) {
			fire = append(fire, t)
		} else {
			pending = append(pending, t)
		}
	}
	c.timers = pending
	now := c.now
	c.mu.Unlock()
	for _, t := range fire {
		t.ch <- now
	}
}

// awaitTimer waits (real time, µs polls) until the watchdog goroutine has a
// pending After registered, so an Advance cannot race past a re-arm.
func (c *fakeClockSource) awaitTimer(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		c.mu.Lock()
		n := len(c.timers)
		c.mu.Unlock()
		if n > 0 {
			return
		}
		time.Sleep(50 * time.Microsecond)
	}
	t.Fatal("watchdog never armed a timer")
}
