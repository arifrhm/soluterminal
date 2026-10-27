//go:build !linux

package main

import (
	"fmt"
	"time"
)

func prepareOwnerIdleProof(ownerRetirementRequest, ownerRetirementInputFence, time.Time) (ownerRetirementFrozen, ownerRetirementProof, error) {
	return nil, ownerRetirementProof{}, fmt.Errorf("conditional owner retirement is unavailable on this platform; pane preserved")
}

func captureOwnerInputFence(string, ownerRetirementInputFence) ownerRetirementInputFence {
	return ownerRetirementInputFence{Seen: true, UnresolvedSubmission: true}
}
