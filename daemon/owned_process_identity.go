package main

import "runtime"

func retirementPlatformSupported() bool { return runtime.GOOS == "linux" }

func retirementLaunchIdentity(pid int) *retirementProcessIdentity {
	if !retirementPlatformSupported() {
		return nil
	}
	identity := retirementCaptureProcess(pid)
	return &identity
}

// Launch identity is durable provenance. The coordinator signals owned runtime
// contexts or identity-checked platform handles, then re-observes every recorded
// group. Missing identity never authorizes a PID-only kill.
type retirementProcessIdentity struct {
	Scope    *processScopeIdentity `json:"scope,omitempty"`
	PID      int                   `json:"pid"`
	Start    string                `json:"start"`
	HostBoot string                `json:"hostBoot"`
	Group    int                   `json:"group"`
	Session  int                   `json:"session"`
}

type retirementProcessState string

const (
	retirementProcessAlive   retirementProcessState = "proven_alive"
	retirementProcessGone    retirementProcessState = "proven_gone"
	retirementProcessUnknown retirementProcessState = "unknown"
)
