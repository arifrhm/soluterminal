package main

import (
	"time"
)

// systemWorkerProcess is the cached, read-only process identity for an invocation
// that is still live. The System sampler uses it to account for headless worker
// trees without probing or mutating engine state.
type systemWorkerProcess struct {
	TaskID, TaskTitle, Stage, Role, Runtime string
	Pid                                     int
	StartedAt                               time.Time
}
