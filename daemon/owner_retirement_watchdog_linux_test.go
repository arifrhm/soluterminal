//go:build linux

package main

import (
	"os"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestOwnerRetirementWatchdogCommitSerializesExpiryAndPartialFailure(t *testing.T) {
	for _, mode := range []string{"commit", "expires-mid-commit", "second-kill-fails"} {
		t.Run(mode, func(t *testing.T) {
			sockets, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_SEQPACKET|unix.SOCK_CLOEXEC, 0)
			if err != nil {
				t.Fatal(err)
			}
			client := os.NewFile(uintptr(sockets[0]), "watchdog-client")
			defer client.Close()
			defer unix.Close(sockets[1])
			// Transferred handles are valid, but injected signals never act on this
			// process. Real STOP/CONT/kill behavior is covered by the subprocess/PTY gates.
			fd, err := unix.PidfdOpen(os.Getpid(), 0)
			if err != nil {
				t.Fatal(err)
			}
			defer unix.Close(fd)
			deadline := time.Now().Add(300 * time.Millisecond)
			var mu sync.Mutex
			var signals []unix.Signal
			kills := 0
			complete := make(chan struct{})
			go func() {
				defer close(complete)
				runOwnerFreezeWatchdogSignals(sockets[1], deadline, func(_ int, signal unix.Signal) error {
					mu.Lock()
					signals = append(signals, signal)
					if signal == unix.SIGKILL {
						kills++
					}
					n := kills
					mu.Unlock()
					if signal == unix.SIGKILL && mode == "expires-mid-commit" && n == 1 {
						time.Sleep(time.Until(deadline) + 10*time.Millisecond)
					}
					if signal == unix.SIGKILL && mode == "second-kill-fails" && n == 2 {
						return unix.EPERM
					}
					return nil
				})
			}()
			if err := watchdogStop(client, fd, deadline); err != nil {
				t.Fatal(err)
			}
			if err := watchdogStop(client, fd, deadline); err != nil {
				t.Fatal(err)
			}
			err = watchdogCommit(client, deadline)
			if mode == "commit" && err != nil {
				t.Fatal(err)
			}
			if mode != "commit" && err == nil {
				t.Fatal("partial/expired commit reported success")
			}
			<-complete
			mu.Lock()
			defer mu.Unlock()
			expectedKills := 2
			if mode == "expires-mid-commit" {
				expectedKills = 1
			}
			if kills != expectedKills {
				t.Fatalf("unexpected kill count %d: %v", kills, signals)
			}
			resumed := false
			continues := 0
			for _, signal := range signals {
				if signal == unix.SIGCONT {
					resumed = true
					continues++
				}
				if resumed && signal == unix.SIGKILL {
					t.Fatal("kill raced a deadline resume")
				}
			}
			if continues != 2 {
				t.Fatalf("partial termination left survivors stopped: %v", signals)
			}
		})
	}
}

func TestOwnerRetirementWatchdogInterruptedExchangeDoesNotRepeatCommand(t *testing.T) {
	for _, command := range []byte{'S', 'K'} {
		t.Run(string(command), func(t *testing.T) {
			sends, submitted, waits, receives := 0, 0, 0, 0
			deadline := time.Now().Add(time.Second)
			err := requestOwnerWatchdog(command, deadline, ownerWatchdogTransport{
				send: func() error {
					sends++
					if sends == 1 {
						return unix.EINTR
					}
					submitted++
					return nil
				},
				wait: func(timeout int) (bool, error) {
					waits++
					if timeout <= 0 || timeout > 1000 {
						t.Fatalf("unbounded poll timeout: %d", timeout)
					}
					if waits == 1 {
						return false, unix.EINTR
					}
					return true, nil
				},
				receive: func(reply []byte) (int, error) {
					receives++
					switch receives {
					case 1:
						return 0, unix.EINTR
					case 2:
						return 0, unix.EAGAIN
					}
					reply[0] = command
					return 1, nil
				},
			})
			if err != nil || sends != 2 || submitted != 1 || waits != 4 || receives != 3 {
				t.Fatalf("interrupted exchange err=%v sends=%d submitted=%d waits=%d receives=%d", err, sends, submitted, waits, receives)
			}
		})
	}
}

func TestOwnerRetirementWatchdogInterruptionsKeepAbsoluteDeadline(t *testing.T) {
	for _, phase := range []string{"send", "wait", "receive"} {
		t.Run(phase, func(t *testing.T) {
			// Leave setup headroom; the injected interruption itself crosses the
			// exact deadline, so retry counts prove expiry without tight timing assertions.
			deadline := time.Now().Add(250 * time.Millisecond)
			sends, waits, receives := 0, 0, 0
			interrupt := func() error { time.Sleep(time.Until(deadline) + time.Millisecond); return unix.EINTR }
			err := requestOwnerWatchdog('K', deadline, ownerWatchdogTransport{
				send: func() error {
					sends++
					if sends > 1 {
						t.Fatal("send repeated after deadline")
					}
					if phase == "send" {
						return interrupt()
					}
					return nil
				},
				wait: func(int) (bool, error) {
					waits++
					if waits > 1 {
						t.Fatal("wait repeated after deadline")
					}
					if phase == "wait" {
						return false, interrupt()
					}
					return true, nil
				},
				receive: func([]byte) (int, error) {
					receives++
					if receives > 1 {
						t.Fatal("receive repeated after deadline")
					}
					return 0, interrupt()
				},
			})
			if err == nil || time.Now().Before(deadline) {
				t.Fatalf("interrupted %s exceeded permission boundary: %v", phase, err)
			}
			expectedWaits, expectedReceives := 1, 1
			if phase == "send" {
				expectedWaits, expectedReceives = 0, 0
			} else if phase == "wait" {
				expectedReceives = 0
			}
			if sends != 1 || waits != expectedWaits || receives != expectedReceives {
				t.Fatalf("unexpected retry after %s expiry: %d/%d/%d", phase, sends, waits, receives)
			}
		})
	}
}
