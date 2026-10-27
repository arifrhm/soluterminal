package main

import (
	"errors"
	"io"
)

var errAutoModePolicyChanged = errors.New("auto-approval policy changed before write")

// An in-memory decision lease, not a persisted setting or an authority grant.
type paneAutoModePolicy struct {
	mode       string
	generation uint64
}

func (p *Pane) autoModePolicy() paneAutoModePolicy {
	p.mu.Lock()
	defer p.mu.Unlock()
	return paneAutoModePolicy{mode: p.AutoMode, generation: p.autoModeGeneration}
}

func (policy paneAutoModePolicy) matchesLocked(p *Pane) bool {
	return p.AutoMode == policy.mode && p.autoModeGeneration == policy.generation
}

func (policy paneAutoModePolicy) matches(p *Pane) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return policy.matchesLocked(p)
}

// Count actual transport bytes so a rejected frame or an expired deadline
// before the first byte does not disconnect an otherwise healthy pane.
type autoModeFrameWriter struct {
	io.Writer
	written int
}

func (w *autoModeFrameWriter) Write(b []byte) (int, error) {
	n, err := w.Writer.Write(b)
	w.written += n
	return n, err
}
