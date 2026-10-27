package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestWSLPathProbeDeadlineReapsProcessGroup(t *testing.T) {
	for _, pipeOnly := range []bool{false, true} {
		t.Run(strconv.FormatBool(pipeOnly), func(t *testing.T) {
			pidFile := filepath.Join(t.TempDir(), "child.pid")
			script := "/bin/sleep 30 & echo $! > \"$2\"; wait"
			if pipeOnly {
				script = "/bin/sleep 30 & echo $! > \"$2\"; exit 0"
			}
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			installTestWSLPath(t, script)
			start := time.Now()
			_, err := runWSLPath(ctx, "-w", pidFile)
			if pipeOnly && !errors.Is(err, exec.ErrWaitDelay) || !pipeOnly && !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("timeout was not an explicit setup failure: %v", err)
			}
			if time.Since(start) > 3*time.Second {
				t.Fatal("fixture failed to return within deadline")
			}
			raw, readErr := os.ReadFile(pidFile)
			if readErr != nil {
				t.Fatal(readErr)
			}
			pid := strings.TrimSpace(string(raw))
			for deadline := time.Now().Add(time.Second); ; {
				stat, err := os.ReadFile("/proc/" + pid + "/stat")
				if os.IsNotExist(err) || err == nil && strings.HasPrefix(string(stat[strings.LastIndex(string(stat), ")")+1:]), " Z ") {
					break // Exited descendants may await init's reap.
				}
				if time.Now().After(deadline) {
					t.Fatalf("fixture descendant %s survived cleanup: %v", pid, err)
				}
				time.Sleep(10 * time.Millisecond)
			}
		})
	}
}
