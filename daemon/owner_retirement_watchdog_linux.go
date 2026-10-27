//go:build linux

package main

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"time"

	"golang.org/x/sys/unix"
)

const ownerWatchdogCommand = "__owner-retirement-watchdog"

// The watchdog owns SIGSTOP, so expiry cannot race a late parent SIGSTOP after
// the watchdog's SIGCONT. Its inherited socket EOF resumes children if the
// owning agent crashes. The agent's independent timer covers watchdog failure.
func init() {
	if len(os.Args) != 3 || os.Args[1] != ownerWatchdogCommand {
		return
	}
	deadline, err := strconv.ParseInt(os.Args[2], 10, 64)
	if err != nil {
		os.Exit(2)
	}
	kind, err := unix.GetsockoptInt(3, unix.SOL_SOCKET, unix.SO_TYPE)
	if err != nil || kind != unix.SOCK_SEQPACKET {
		os.Exit(2)
	}
	runOwnerFreezeWatchdog(3, time.Unix(0, deadline))
	os.Exit(0)
}

func runOwnerFreezeWatchdog(socket int, deadline time.Time) {
	runOwnerFreezeWatchdogSignals(socket, deadline, func(fd int, signal unix.Signal) error { return unix.PidfdSendSignal(fd, signal, nil, 0) })
}

// Dependency injection tests deadline/partial-failure ordering without any
// production fault flag or global hook.
func runOwnerFreezeWatchdogSignals(socket int, deadline time.Time, sendSignal func(int, unix.Signal) error) {
	if limit := time.Now().Add(ownerRetirementLeaseDuration); deadline.After(limit) {
		deadline = limit
	}
	var fds []int
	defer func() {
		for _, fd := range fds {
			_ = sendSignal(fd, unix.SIGCONT)
			_ = unix.Close(fd)
		}
	}()
	for {
		left := time.Until(deadline)
		if left <= 0 {
			return
		}
		polls := []unix.PollFd{{Fd: int32(socket), Events: unix.POLLIN}}
		if _, err := unix.Poll(polls, int((left+time.Millisecond-1)/time.Millisecond)); err != nil {
			if err == unix.EINTR {
				continue
			}
			return
		}
		if !time.Now().Before(deadline) || polls[0].Revents&(unix.POLLERR|unix.POLLHUP|unix.POLLNVAL) != 0 {
			return
		}
		var body [1]byte
		ancillary := make([]byte, unix.CmsgSpace(4))
		n, oobn, flags, _, err := unix.Recvmsg(socket, body[:], ancillary, unix.MSG_DONTWAIT)
		if err == unix.EINTR || err == unix.EAGAIN {
			continue
		}
		if err != nil || n != 1 || flags&unix.MSG_CTRUNC != 0 {
			return
		}
		if body[0] == 'K' {
			if oobn != 0 || len(fds) == 0 || !time.Now().Before(deadline) {
				return
			}
			// Commit and timeout share this one loop. Once accepted, SIGCONT
			// cannot race any remaining SIGKILL in the exact stopped chain.
			for _, fd := range fds {
				if !time.Now().Before(deadline) {
					_ = replyOwnerWatchdog(socket, []byte("retirement commit deadline elapsed"), deadline)
					return
				}
				if err := sendSignal(fd, unix.SIGKILL); err != nil && err != unix.ESRCH {
					_ = replyOwnerWatchdog(socket, []byte(err.Error()), deadline)
					return
				}
			}
			_ = replyOwnerWatchdog(socket, []byte{'K'}, deadline)
			return
		}
		if body[0] != 'S' || len(fds) >= 64 {
			return
		}
		messages, err := unix.ParseSocketControlMessage(ancillary[:oobn])
		if err != nil {
			return
		}
		var received []int
		for _, message := range messages {
			rights, e := unix.ParseUnixRights(&message)
			if e != nil {
				for _, fd := range received {
					unix.Close(fd)
				}
				return
			}
			received = append(received, rights...)
		}
		if len(received) != 1 {
			for _, fd := range received {
				unix.Close(fd)
			}
			return
		}
		fd := received[0]
		fds = append(fds, fd)
		if !time.Now().Before(deadline) {
			return
		}
		if err := sendSignal(fd, unix.SIGSTOP); err != nil {
			_ = replyOwnerWatchdog(socket, []byte(err.Error()), deadline)
			return
		}
		if err := replyOwnerWatchdog(socket, []byte{'S'}, deadline); err != nil {
			return
		}
	}

}

func startOwnerFreezeWatchdog(deadline time.Time) (*os.File, int, error) {
	sockets, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_SEQPACKET|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return nil, 0, err
	}
	parent, child := os.NewFile(uintptr(sockets[0]), "owner-retirement-watchdog"), os.NewFile(uintptr(sockets[1]), "owner-retirement-lifetime")
	command := exec.Command("/proc/self/exe", ownerWatchdogCommand, strconv.FormatInt(deadline.UnixNano(), 10))
	command.ExtraFiles = []*os.File{child}
	// No inherited stderr/stdout or credentials are needed by this tiny helper.
	if err := command.Start(); err != nil {
		parent.Close()
		child.Close()
		return nil, 0, err
	}
	child.Close()
	go func() { _ = command.Wait() }()
	return parent, command.Process.Pid, nil
}

func watchdogStop(socket *os.File, fd int, deadline time.Time) error {
	return watchdogRequest(socket, 'S', unix.UnixRights(fd), deadline)
}
func watchdogCommit(socket *os.File, deadline time.Time) error {
	return watchdogRequest(socket, 'K', nil, deadline)
}

// The seam tests signal interruption without global hooks or real-process
// signals. A successful send occurs once; ACK retries never repeat STOP/KILL.
type ownerWatchdogTransport struct {
	send    func() error
	wait    func(int) (bool, error)
	receive func([]byte) (int, error)
}

func watchdogRequest(socket *os.File, command byte, rights []byte, deadline time.Time) error {
	socketFD := int(socket.Fd())
	return requestOwnerWatchdog(command, deadline, ownerWatchdogTransport{
		send: func() error { return unix.Sendmsg(socketFD, []byte{command}, rights, nil, unix.MSG_DONTWAIT) },
		wait: func(timeoutMs int) (bool, error) {
			polls := []unix.PollFd{{Fd: int32(socketFD), Events: unix.POLLIN}}
			_, err := unix.Poll(polls, timeoutMs)
			return polls[0].Revents&unix.POLLIN != 0, err
		},
		receive: func(reply []byte) (int, error) {
			n, _, err := unix.Recvfrom(socketFD, reply, unix.MSG_DONTWAIT)
			return n, err
		},
	})
}

func replyOwnerWatchdog(socket int, body []byte, deadline time.Time) error {
	return sendOwnerWatchdogPacket(deadline, func() error {
		return unix.Sendmsg(socket, body, nil, nil, unix.MSG_DONTWAIT)
	})
}

func sendOwnerWatchdogPacket(deadline time.Time, send func() error) error {
	for {
		if !time.Now().Before(deadline) {
			return fmt.Errorf("retirement watchdog deadline elapsed")
		}
		err := send()
		if err == unix.EINTR {
			// No packet was submitted.
			continue
		}
		return err
	}
}

func requestOwnerWatchdog(command byte, deadline time.Time, transport ownerWatchdogTransport) error {
	if err := sendOwnerWatchdogPacket(deadline, transport.send); err != nil {
		return err
	}

	for {
		left := time.Until(deadline)
		if left <= 0 {
			return fmt.Errorf("retirement watchdog deadline elapsed")
		}
		ready, err := transport.wait(int((left + time.Millisecond - 1) / time.Millisecond))
		if err == unix.EINTR {
			continue
		}
		if err != nil {
			return err
		}
		if !time.Now().Before(deadline) {
			return fmt.Errorf("retirement watchdog deadline elapsed")
		}
		if !ready {
			return fmt.Errorf("retirement watchdog unavailable")
		}
		var reply [128]byte
		n, err := transport.receive(reply[:])
		if err == unix.EINTR || err == unix.EAGAIN {
			continue
		}
		if err != nil || n != 1 || reply[0] != command {
			return fmt.Errorf("retirement watchdog did not confirm operation: n=%d error=%v reply=%q", n, err, reply[:max(n, 0)])
		}
		if !time.Now().Before(deadline) {
			return fmt.Errorf("retirement watchdog deadline elapsed")
		}
		return nil
	}
}
