package main

import (
	"context"
	"errors"
	"sync"
)

func newRestartAdmissionBarrier() *restartAdmissionBarrier {
	return &restartAdmissionBarrier{changed: make(chan struct{})}
}

// restartAdmissionBarrier closes the race between the last in-flight sample
// and a model call starting. Callers first cross admission, then either release
// immediately after a durable invocation.started record or hold the crossing
// for the complete unrecorded call. A restart owner closes admission and waits
// for every pre-close crosser to become visible or finish.
type restartAdmissionBarrier struct {
	mu       sync.Mutex
	closed   bool
	crossing int
	changed  chan struct{}
}

func (b *restartAdmissionBarrier) ensureLocked() {
	if b.changed == nil {
		b.changed = make(chan struct{})
	}
}

func (b *restartAdmissionBarrier) signalLocked() {
	b.ensureLocked()
	close(b.changed)
	b.changed = make(chan struct{})
}

func (b *restartAdmissionBarrier) admit(ctx context.Context) (func(), error) {
	if b == nil {
		return func() {}, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	for {
		b.mu.Lock()
		b.ensureLocked()
		if !b.closed {
			b.crossing++
			b.mu.Unlock()
			var once sync.Once
			return func() {
				once.Do(func() {
					b.mu.Lock()
					if b.crossing > 0 {
						b.crossing--
					}
					b.signalLocked()
					b.mu.Unlock()
				})
			}, nil
		}
		changed := b.changed
		b.mu.Unlock()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-changed:
		}
	}
}

// close waits only until callers that crossed before closure have made their
// work durable or finished. The supplied context owns the deadline; on
// cancellation admission is reopened and no worker is terminated.
func (b *restartAdmissionBarrier) close(ctx context.Context) (func(), error) {
	if b == nil {
		return func() {}, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	b.mu.Lock()
	b.ensureLocked()
	if b.closed {
		b.mu.Unlock()
		return nil, errors.New("restart admission barrier already closed")
	}
	b.closed = true
	b.signalLocked()
	for b.crossing > 0 {
		changed := b.changed
		b.mu.Unlock()
		select {
		case <-ctx.Done():
			b.mu.Lock()
			b.closed = false
			b.signalLocked()
			b.mu.Unlock()
			return nil, ctx.Err()
		case <-changed:
		}
		b.mu.Lock()
	}
	b.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			b.mu.Lock()
			b.closed = false
			b.signalLocked()
			b.mu.Unlock()
		})
	}, nil
}

func (b *restartAdmissionBarrier) snapshot() (closed bool, crossing int) {
	if b == nil {
		return false, 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.closed, b.crossing
}
