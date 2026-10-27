//go:build !linux

package main

// Until a creation-identity-bound tree verifier is wired on the platform,
// absence/leader exit cannot be reported as complete worker shutdown.
func retirementObserveProcessGroup(root string, expected retirementProcessIdentity) retirementProcessState {
	return retirementProcessUnknown
}
func retirementCaptureProcess(pid int) retirementProcessIdentity {
	return retirementProcessIdentity{PID: pid}
}

func retirementStopProcessGroup(expected retirementProcessIdentity) retirementProcessState {
	return retirementProcessUnknown
}
