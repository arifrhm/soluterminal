//go:build linux

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

func retirementReadProcess(root string, pid int) (retirementProcessIdentity, error) {
	var result retirementProcessIdentity
	if pid <= 0 {
		return result, fmt.Errorf("missing process identity")
	}
	raw, err := os.ReadFile(filepath.Join(root, strconv.Itoa(pid), "stat"))
	if err != nil {
		return result, err
	}
	closing := strings.LastIndexByte(string(raw), ')')
	if closing < 0 {
		return result, fmt.Errorf("invalid process stat")
	}
	fields := strings.Fields(string(raw[closing+1:]))
	if len(fields) < 20 {
		return result, fmt.Errorf("incomplete process stat")
	}
	group, groupErr := strconv.Atoi(fields[2])
	session, sessionErr := strconv.Atoi(fields[3])
	if groupErr != nil || sessionErr != nil || group <= 0 || session <= 0 {
		return result, fmt.Errorf("invalid process group/session")
	}
	start, err := strconv.ParseUint(fields[19], 10, 64)
	if err != nil || start == 0 {
		return result, fmt.Errorf("invalid process start time")
	}
	return retirementProcessIdentity{PID: pid, Start: fmt.Sprintf("linux:%d", start), Group: group, Session: session}, nil
}

// root is /proc in production. Hermetic fixtures use their own private tree;
// tests never inspect or signal a production process.
func retirementObserveProcessGroup(root string, expected retirementProcessIdentity) retirementProcessState {
	if expected.PID <= 0 || expected.Start == "" || expected.HostBoot == "" || expected.Group != expected.PID || expected.Session <= 0 {
		return retirementProcessUnknown
	}
	raw, err := os.ReadFile(filepath.Join(root, "sys", "kernel", "random", "boot_id"))
	boot := strings.TrimSpace(string(raw))
	if err != nil || boot == "" {
		return retirementProcessUnknown
	}
	if boot != expected.HostBoot {
		// The identity was captured by this daemon into its local store under a
		// kernel that has since ended. No Linux process survives its kernel,
		// including setsid descendants that escaped the recorded group/session,
		// so a complete identity from an earlier boot is whole-tree exit. This is
		// the one observation that proves it; daemon restart alone never does.
		return retirementProcessGone
	}
	actual, err := retirementReadProcess(root, expected.PID)
	if err == nil {
		if actual.Start != expected.Start || actual.Group != expected.Group || actual.Session != expected.Session {
			return retirementProcessUnknown
		}
		return retirementProcessAlive
	}
	if !os.IsNotExist(err) {
		return retirementProcessUnknown
	}
	// A missing leader alone proves nothing about surviving children. Every
	// readable process in the recorded group must have disappeared too.
	entries, err := os.ReadDir(root)
	if err != nil {
		return retirementProcessUnknown
	}
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || pid <= 0 {
			continue
		}
		identity, err := retirementReadProcess(root, pid)
		if os.IsNotExist(err) {
			continue
		} // disappeared during enumeration
		if err != nil {
			return retirementProcessUnknown
		}
		if identity.Group == expected.Group || (expected.Session == expected.PID && identity.Session == expected.Session) {
			// Re-parenting retains process groups. A session mismatch could
			// instead be a recycled group: conservatively leave uncertainty.
			if identity.Session != expected.Session {
				return retirementProcessUnknown
			}
			return retirementProcessAlive
		}
	}
	// Recheck birth identity after enumeration. A recycled leader is unknown,
	// even when the recorded group no longer appeared in the first scan.
	if _, err := retirementReadProcess(root, expected.PID); !os.IsNotExist(err) {
		return retirementProcessUnknown
	}
	// Descendants can setsid and escape both the recorded group and session.
	// No group scan proves whole-tree exit without durable containment. Keep
	// the hold rather than turn a missing leader into retirement success.
	return retirementProcessUnknown
}

func retirementCaptureProcess(pid int) retirementProcessIdentity {
	identity, err := retirementReadProcess("/proc", pid)
	if err != nil {
		return retirementProcessIdentity{PID: pid}
	}
	identity.Scope = ownedScopeForPID(pid)
	boot, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err == nil {
		identity.HostBoot = strings.TrimSpace(string(boot))
	}
	return identity
}

// Signal only identity-bound pidfds. A bare PID or negative-PID group kill can
// race PID reuse. Holding a pidfd keeps the signal addressed to the inspected
// process even if its numeric PID is subsequently recycled. Surviving members
// are re-observed; unsupported kernels/permissions remain unknown.
func retirementStopProcessGroup(expected retirementProcessIdentity) retirementProcessState {
	if expected.Scope != nil {
		if !validLinuxProcessScopeIdentity(*expected.Scope) {
			return retirementProcessUnknown
		}
		boot, err := currentProcessScopeBoot()
		if err != nil {
			return retirementProcessUnknown
		}
		if expected.Scope.Boot != "" && expected.Scope.Boot != boot {
			return retirementProcessGone
		}
		if !processScopeHasContainment(*expected.Scope) {
			return retirementProcessUnknown
		}
		root, err := processScopeRoot()
		if err != nil {
			return retirementProcessUnknown
		}
		gone, err := stopOwnedProcessScope(&ownedProcessScope{identity: *expected.Scope, root: root}, true)
		if err != nil {
			return retirementProcessUnknown
		}
		if gone {
			return retirementProcessGone
		}
		return retirementProcessAlive
	}
	state := retirementObserveProcessGroup("/proc", expected)
	if state != retirementProcessAlive {
		return state
	}
	leaderFD, err := unix.PidfdOpen(expected.PID, 0)
	if err != nil {
		return retirementProcessUnknown
	}
	defer unix.Close(leaderFD)
	leader, err := retirementReadProcess("/proc", expected.PID)
	if err != nil || leader.Start != expected.Start || leader.Group != expected.Group || leader.Session != expected.Session {
		return retirementProcessUnknown
	}
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return retirementProcessUnknown
	}
	var members []retirementProcessIdentity
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || pid <= 0 {
			continue
		}
		identity, err := retirementReadProcess("/proc", pid)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return retirementProcessUnknown
		}
		if identity.Group == expected.Group || (expected.Session == expected.PID && identity.Session == expected.Session) {
			if identity.Session != expected.Session {
				return retirementProcessUnknown
			}
			identity.HostBoot = expected.HostBoot
			members = append(members, identity)
		}
	}
	for _, identity := range members {
		fd, err := unix.PidfdOpen(identity.PID, 0)
		if err == unix.ESRCH {
			continue
		}
		if err != nil {
			return retirementProcessUnknown
		}
		actual, readErr := retirementReadProcess("/proc", identity.PID)
		if os.IsNotExist(readErr) {
			unix.Close(fd)
			continue
		}
		// Re-read after opening the handle: no signal on ambiguous identity.
		if readErr != nil || actual.Start != identity.Start || actual.Group != identity.Group || actual.Session != identity.Session {
			unix.Close(fd)
			return retirementProcessUnknown
		}
		err = unix.PidfdSendSignal(fd, unix.SIGKILL, nil, 0)
		unix.Close(fd)
		if err != nil && err != unix.ESRCH {
			return retirementProcessUnknown
		}
	}
	return retirementObserveProcessGroup("/proc", expected)
}
