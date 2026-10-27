package main

import (
	"fmt"
	"log/slog"
	"time"
)

// Workflow update-drain admission state machine. All state is in-memory only —
// never persisted — so a daemon restart forgets it and queued tasks can never
// be starved by stale drain state:
//
//	IDLE             — no window armed, no lease. Admission open.
//	WINDOW-ARMED     — a non-forced Apply found invocations in flight.
//	DEFERRED-BACKOFF — the deadline arrived while work was still healthy; the
//	                   window is cleared and admission is open until retry.
//	LEASE-HELD       — a genuine quiet instant was found; admission stays
//	                   paused through download, the pre-swap check, and swap.
//
// A deadline never hands an ordinary update a lease. It defers the attempt and
// reopens admission. A pre-swap straggler gets the same bounded window and can
// likewise defer; it is never intentionally swapped through.
type drainOutcome uint8

const (
	drainProceed drainOutcome = iota
	drainWait
	drainDefer
)

func drainDecision(now, start time.Time, window time.Duration, inflight int, inflightErr error) (drainOutcome, time.Time, string) {
	if inflightErr != nil || inflight <= 0 {
		return drainProceed, time.Time{}, ""
	}
	if start.IsZero() {
		return drainWait, now, fmt.Sprintf("waiting for %d in-flight workflow invocations to finish (drain deadline in %s)", inflight, window)
	}
	if now.Sub(start) >= window {
		return drainDefer, time.Time{}, fmt.Sprintf("drain deadline reached with %d invocations in flight — deferring update", inflight)
	}
	remaining := (window - now.Sub(start)).Round(time.Second)
	return drainWait, start, fmt.Sprintf("waiting for %d in-flight workflow invocations to finish (drain deadline in %s)", inflight, remaining)
}

type drainCheck struct {
	outcome        drainOutcome
	reason         string
	inflight       int
	deadlineMs     int64
	nextRetryAt    time.Time
	attempts       int
	publishTrainID string
}

func (u *Updater) checkWorkflowDrain(now time.Time) drainCheck {
	var (
		publishTrainID  string
		publishInflight bool
		publishErr      error
	)
	if u.releasePublishInflight != nil {
		func() {
			defer func() {
				if recovered := recover(); recovered != nil {
					publishErr = fmt.Errorf("release publish check panic: %v", recovered)
				}
			}()
			publishTrainID, publishInflight = u.releasePublishInflight()
		}()
	}
	if publishErr == nil && publishInflight {
		u.mu.Lock()
		u.drainStart = time.Time{}
		u.drainInflight = 0
		u.leaseHeld = false
		u.leaseSince = time.Time{}
		u.lastDrainDuration = 0
		u.publishDeferTrain = publishTrainID
		u.publishDeferredAt = now
		retryAt := now.Add(publishDeferRetry)
		if u.nextRetryTime.IsZero() || u.nextRetryTime.Before(retryAt) {
			u.nextRetryTime = retryAt
		}
		check := drainCheck{
			outcome:        drainDefer,
			reason:         fmt.Sprintf("release train %s publish in flight — deferring update until the train records its outcome", publishTrainID),
			nextRetryAt:    u.nextRetryTime,
			attempts:       u.deferAttempts,
			publishTrainID: publishTrainID,
		}
		u.mu.Unlock()
		slog.Info("release train publish in flight — ordinary update deferred",
			"train", publishTrainID, "nextRetryAt", check.nextRetryAt)
		if u.bus != nil {
			u.bus.publish(uiEvent{Type: "update-available"})
		}
		return check
	}

	var (
		inflight    int
		inflightErr error
	)
	if u.inflightCount != nil {
		func() {
			defer func() {
				if recovered := recover(); recovered != nil {
					inflightErr = fmt.Errorf("workflow inflight check panic: %v", recovered)
				}
			}()
			inflight = u.inflightCount()
		}()
	}

	u.mu.Lock()
	u.publishDeferTrain = ""
	u.publishDeferredAt = time.Time{}
	start := u.drainStart
	outcome, newStart, reason := drainDecision(now, start, u.drainWindow, inflight, inflightErr)
	if !start.IsZero() {
		u.lastDrainDuration = now.Sub(start)
	}
	u.drainStart = newStart
	if newStart.IsZero() {
		u.drainInflight = 0
	} else {
		u.drainInflight = inflight
	}
	check := drainCheck{outcome: outcome, reason: reason, inflight: inflight}
	leaseAcquired := false
	switch outcome {
	case drainProceed:
		// Hand the block from the window to the lease in the same critical
		// section. Admission can never momentarily reopen on a quiet proceed.
		if !u.leaseHeld {
			u.leaseHeld = true
			u.leaseSince = now
			leaseAcquired = true
		}
	case drainDefer:
		// A deadline defer deliberately acquires no lease. Clearing both pieces
		// here makes WorkflowDrainPending false as soon as Apply returns.
		u.leaseHeld = false
		u.leaseSince = time.Time{}
		// No swap belongs to this expired attempt. A later quiet Apply starts
		// with zero drain duration instead of inheriting this full window.
		u.lastDrainDuration = 0
		u.recordQuietDeferLocked(now, inflight)
		check.nextRetryAt = u.nextRetryTime
		check.attempts = u.deferAttempts
	case drainWait:
		check.deadlineMs = newStart.Add(u.drainWindow).UnixMilli()
	}
	u.mu.Unlock()

	if outcome == drainDefer {
		slog.Info("workflow drain deadline reached — ordinary update deferred",
			"inflight", inflight, "attempts", check.attempts, "nextRetryAt", check.nextRetryAt)
		if u.bus != nil {
			u.bus.publish(uiEvent{Type: "update-available"})
		}
	}
	if leaseAcquired {
		slog.Info("update admission lease acquired — pausing new workflow invocations for the apply attempt")
	}
	return check
}

func (u *Updater) recordQuietDeferLocked(now time.Time, inflight int) {
	u.deferAt = now
	u.deferInflight = inflight
	u.deferAttempts++
	backoff := quietBackoffBase
	for i := 1; i < u.deferAttempts && backoff < quietBackoffMax; i++ {
		backoff *= 2
		if backoff > quietBackoffMax {
			backoff = quietBackoffMax
		}
	}
	u.nextRetryTime = now.Add(backoff)
}

// releasePublishInflightNow reports an in-flight release publish with panic
// isolation. A roll-past apply must still refuse to swap the binary under a
// publish.sh run (incident rtr_26224e70); only the workflow drain is skipped.
func (u *Updater) releasePublishInflightNow() (string, bool) {
	if u.releasePublishInflight == nil {
		return "", false
	}
	var (
		trainID  string
		inflight bool
	)
	func() {
		defer func() {
			if recover() != nil {
				trainID, inflight = "", false
			}
		}()
		trainID, inflight = u.releasePublishInflight()
	}()
	return trainID, inflight
}

func (u *Updater) clearWorkflowDrainLocked() {
	u.drainStart = time.Time{}
	u.drainInflight = 0
	u.leaseHeld = false
	u.leaseSince = time.Time{}
	u.rollbackDrain = false
	u.localDrainCandidateID = ""
	u.deferAt = time.Time{}
	u.deferInflight = 0
	u.deferAttempts = 0
	u.nextRetryTime = time.Time{}
	u.publishDeferTrain = ""
	u.publishDeferredAt = time.Time{}
	u.lastDrainDuration = 0
}

func (u *Updater) releaseWorkflowLeaseLocked() {
	if u.leaseHeld {
		slog.Debug("update admission lease released", "heldFor", time.Since(u.leaseSince).Round(time.Millisecond))
	}
	u.leaseHeld = false
	u.leaseSince = time.Time{}
}
