package main

import (
	"time"
)

// clockSource abstracts time for the watchdog so its semantics are provable with
// a fake clock instead of real waiting.
type clockSource interface {
	Now() time.Time
	After(time.Duration) <-chan time.Time
}

type realClock struct{}

func (realClock) Now() time.Time { return time.Now() }

func (realClock) After(d time.Duration) <-chan time.Time { return time.After(d) }
