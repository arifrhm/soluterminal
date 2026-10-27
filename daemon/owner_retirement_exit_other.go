//go:build !linux

package main

import "fmt"

func retainOwnerExitFence(ownerRetirementProof) (func() bool, func(), error) {
	return nil, func() {}, fmt.Errorf("exact process exit verification is unavailable on this platform")
}
