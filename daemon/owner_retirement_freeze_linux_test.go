//go:build linux

package main

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// A separate test process ensures this proof can stop only its one disposable
// child, never another parallel test's worker or a user's process.
func TestOwnerRetirementFreezeWatchdogRealProcess(t *testing.T) {
	if os.Getenv("SOLU_RETIRE_FREEZE_FIXTURE") != "1" {
		command := exec.Command(os.Args[0], "-test.run=^TestOwnerRetirementFreezeWatchdogRealProcess$", "-test.count=1")
		command.Env = append(os.Environ(), "SOLU_RETIRE_FREEZE_FIXTURE=1")
		if out, err := command.CombinedOutput(); err != nil {
			t.Fatalf("isolated process: %v\n%s", err, out)
		}
		return
	}
	child := exec.Command("/bin/sleep", "30")
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = child.Process.Kill(); _ = child.Wait() }()
	// Exercise the production lease. A 200 ms fixture also timed process
	// startup and /proc scans, failing before it could test watchdog expiry
	// on a loaded race runner.
	leaseDeadline := time.Now().Add(ownerRetirementLeaseDuration)
	frozen, err := freezeOwnerChildren(os.Getpid(), leaseDeadline)
	if err != nil {
		t.Fatal(err)
	}
	defer frozen.release()
	stat := func() string {
		raw, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(child.Process.Pid), "stat"))
		if err != nil {
			t.Fatal(err)
		}
		end := strings.LastIndexByte(string(raw), ')')
		return strings.Fields(string(raw)[end+1:])[0]
	}
	if state := stat(); state != "T" && state != "t" {
		t.Fatalf("child not stopped: %s", state)
	}
	for state := stat(); ; state = stat() {
		if state != "T" && state != "t" && state != "Z" {
			break
		}
		if time.Now().After(leaseDeadline.Add(2 * time.Second)) {
			t.Fatalf("watchdog did not preserve/resume child: %s", state)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := frozen.commit(); err == nil {
		t.Fatal("expired lease was allowed to kill the child")
	}
	if _, err := freezeOwnerChildren(child.Process.Pid, time.Now().Add(time.Second)); err == nil {
		t.Fatal("accepted a root other than the owning agent")
	}
}

func TestOwnerRetirementFreezeSurvivesAgentDeath(t *testing.T) {
	if os.Getenv("SOLU_RETIRE_CRASH_FIXTURE") == "1" {
		child := exec.Command("/bin/sleep", "30")
		if err := child.Start(); err != nil {
			t.Fatal(err)
		}
		frozen, err := freezeOwnerChildren(os.Getpid(), time.Now().Add(ownerRetirementLeaseDuration))
		if err != nil {
			child.Process.Kill()
			t.Fatal(err)
		}
		defer frozen.release()
		fmt.Printf("FROZEN:%d\n", child.Process.Pid)
		time.Sleep(10 * time.Second)
		t.Fatal("parent test did not terminate the owning agent")
	}
	command := exec.Command(os.Args[0], "-test.run=^TestOwnerRetirementFreezeSurvivesAgentDeath$", "-test.count=1")
	command.Env = append(os.Environ(), "SOLU_RETIRE_CRASH_FIXTURE=1")
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	defer command.Process.Kill()
	scanner := bufio.NewScanner(stdout)
	var childPID int
	for scanner.Scan() {
		if strings.HasPrefix(scanner.Text(), "FROZEN:") {
			childPID, _ = strconv.Atoi(strings.TrimPrefix(scanner.Text(), "FROZEN:"))
			break
		}
	}
	if childPID <= 0 {
		t.Fatal("fixture never stopped its child")
	}
	fd, err := unix.PidfdOpen(childPID, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(fd)
	defer unix.PidfdSendSignal(fd, unix.SIGKILL, nil, 0)
	if err := command.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = command.Wait()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		raw, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(childPID), "stat"))
		if err != nil {
			t.Fatal(err)
		}
		end := strings.LastIndexByte(string(raw), ')')
		fields := strings.Fields(string(raw)[end+1:])
		if len(fields) > 0 && fields[0] != "T" && fields[0] != "t" && fields[0] != "Z" {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("owning agent crash left its child stopped")
}

func TestOwnerRetirementWatchdogDiesAfterStopBeforeAck(t *testing.T) {
	switch os.Getenv("SOLU_RETIRE_ACK_CRASH_FIXTURE") {
	case "helper":
		body := make([]byte, 1)
		ancillary := make([]byte, unix.CmsgSpace(4))
		_, n, _, _, err := unix.Recvmsg(3, body, ancillary, 0)
		if err != nil {
			os.Exit(21)
		}
		messages, err := unix.ParseSocketControlMessage(ancillary[:n])
		if err != nil || len(messages) != 1 {
			os.Exit(22)
		}
		fds, err := unix.ParseUnixRights(&messages[0])
		if err != nil || len(fds) != 1 {
			os.Exit(23)
		}
		if unix.PidfdSendSignal(fds[0], unix.SIGSTOP, nil, 0) != nil {
			os.Exit(24)
		}
		os.Exit(25) // deliberately no ACK, defer, or SIGCONT
	case "owner":
		child := exec.Command("/bin/sleep", "30")
		if err := child.Start(); err != nil {
			t.Fatal(err)
		}
		defer func() { child.Process.Kill(); child.Wait() }()
		start := func(time.Time) (*os.File, int, error) {
			sockets, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_SEQPACKET|unix.SOCK_CLOEXEC, 0)
			if err != nil {
				return nil, 0, err
			}
			parent, childFD := os.NewFile(uintptr(sockets[0]), "parent"), os.NewFile(uintptr(sockets[1]), "child")
			command := exec.Command(os.Args[0], "-test.run=^TestOwnerRetirementWatchdogDiesAfterStopBeforeAck$", "-test.count=1")
			command.Env = append(os.Environ(), "SOLU_RETIRE_ACK_CRASH_FIXTURE=helper")
			command.ExtraFiles = []*os.File{childFD}
			if err := command.Start(); err != nil {
				parent.Close()
				childFD.Close()
				return nil, 0, err
			}
			childFD.Close()
			go func() { command.Wait() }()
			return parent, command.Process.Pid, nil
		}
		if frozen, err := freezeOwnerChildrenWithWatchdog(os.Getpid(), time.Now().Add(time.Second), start); err == nil {
			frozen.release()
			t.Fatal("helper crash accepted")
		}
		deadline := time.Now().Add(time.Second)
		for time.Now().Before(deadline) {
			raw, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(child.Process.Pid), "stat"))
			if err != nil {
				t.Fatal(err)
			}
			state := strings.Fields(string(raw)[strings.LastIndexByte(string(raw), ')')+1:])[0]
			if state != "T" && state != "t" && state != "Z" {
				return
			}
			time.Sleep(time.Millisecond)
		}
		t.Fatal("helper crash before ACK left child stopped")
	default:
		command := exec.Command(os.Args[0], "-test.run=^TestOwnerRetirementWatchdogDiesAfterStopBeforeAck$", "-test.count=1")
		command.Env = append(os.Environ(), "SOLU_RETIRE_ACK_CRASH_FIXTURE=owner")
		if out, err := command.CombinedOutput(); err != nil {
			t.Fatalf("isolated crash proof: %v\n%s", err, out)
		}
	}
}

func TestOwnerRetirementParentTimerSurvivesWatchdogDeathAfterAck(t *testing.T) {
	if os.Getenv("SOLU_RETIRE_HELPER_AFTER_ACK") != "1" {
		command := exec.Command(os.Args[0], "-test.run=^TestOwnerRetirementParentTimerSurvivesWatchdogDeathAfterAck$", "-test.count=1")
		command.Env = append(os.Environ(), "SOLU_RETIRE_HELPER_AFTER_ACK=1")
		if out, err := command.CombinedOutput(); err != nil {
			t.Fatalf("isolated watchdog crash: %v\n%s", err, out)
		}
		return
	}
	child := exec.Command("/bin/sleep", "30")
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { child.Process.Kill(); child.Wait() }()
	leaseDeadline := time.Now().Add(ownerRetirementLeaseDuration)
	frozen, err := freezeOwnerChildren(os.Getpid(), leaseDeadline)
	if err != nil {
		t.Fatal(err)
	}
	defer frozen.release()
	fd, err := unix.PidfdOpen(frozen.watchdogPID, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(fd)
	if err := unix.PidfdSendSignal(fd, unix.SIGKILL, nil, 0); err != nil {
		t.Fatal(err)
	}
	deadline := leaseDeadline.Add(2 * time.Second)
	for time.Now().Before(deadline) {
		raw, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(child.Process.Pid), "stat"))
		if err != nil {
			t.Fatal(err)
		}
		state := strings.Fields(string(raw)[strings.LastIndexByte(string(raw), ')')+1:])[0]
		if state != "T" && state != "t" && state != "Z" {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("helper crash after ACK defeated independent parent timer")
}
