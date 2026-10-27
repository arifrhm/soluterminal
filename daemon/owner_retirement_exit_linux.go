//go:build linux

package main

import (
	"fmt"
	"golang.org/x/sys/unix"
)

func retainOwnerExitFence(proof ownerRetirementProof) (func() bool, func(), error) {
	var fds []int
	closeAll := func() {
		for _, fd := range fds {
			unix.Close(fd)
		}
	}
	if len(proof.ProcessIDs) == 0 || len(proof.ProcessIDs) > 64 {
		return nil, closeAll, fmt.Errorf("owner process chain is not bounded")
	}
	seen := map[int]bool{}
	for _, pid := range proof.ProcessIDs {
		if pid <= 0 || pid == proof.AgentPID || seen[pid] {
			closeAll()
			return nil, func() {}, fmt.Errorf("invalid owner process chain")
		}
		seen[pid] = true
		fd, err := unix.PidfdOpen(pid, 0)
		if err != nil {
			closeAll()
			return nil, func() {}, err
		}
		fds = append(fds, fd)
		if !turnProcessDescends("/proc", pid, proof.AgentPID) {
			closeAll()
			return nil, func() {}, fmt.Errorf("owner process ancestry changed")
		}
	}
	if !seen[proof.ProviderPID] {
		closeAll()
		return nil, func() {}, fmt.Errorf("provider missing from owner exit fence")
	}
	return func() bool {
		for _, fd := range fds {
			polls := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
			_, err := unix.Poll(polls, 0)
			if err != nil || polls[0].Revents&unix.POLLIN == 0 {
				return false
			}
		}
		return true
	}, closeAll, nil
}
